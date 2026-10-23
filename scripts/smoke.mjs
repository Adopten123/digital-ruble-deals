import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';

// Creates synthetic users, documents, holds and balanced demo ledger movements.
// Uses fresh zero-balance organizations; never resets data or spends the shared seed.
const base = (process.env.BASE_URL ?? 'http://localhost:8080').replace(/\/$/, '');
const origin = process.env.APP_ORIGIN ?? base;
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(base).hostname), 'Smoke target must be loopback');

class Client {
  cookies = new Map();
  csrf = '';

  async request(path, { method = 'GET', body, headers = {}, expected = 200, binary = false } = {}) {
    const mutation = !['GET', 'HEAD'].includes(method);
    const response = await fetch(`${base}${path}`, {
      method,
      redirect: 'error',
      headers: {
        ...(this.cookies.size ? { Cookie: [...this.cookies].map(([k, v]) => `${k}=${v}`).join('; ') } : {}),
        ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        ...(mutation ? { Origin: origin, 'X-CSRF-Token': this.csrf } : {}),
        ...headers,
      },
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    });
    for (const cookie of response.headers.getSetCookie()) {
      const first = cookie.split(';', 1)[0];
      const separator = first.indexOf('=');
      const name = first.slice(0, separator);
      const value = first.slice(separator + 1);
      if (/max-age=0/i.test(cookie) || !value) this.cookies.delete(name);
      else this.cookies.set(name, value);
    }
    const bytes = binary ? Buffer.from(await response.arrayBuffer()) : null;
    const text = binary ? '' : await response.text();
    const json = text ? JSON.parse(text) : null;
    assert.equal(response.status, expected, `${method} ${path}: ${json?.error?.code ?? response.status}`);
    return { response, json, bytes };
  }

  async refreshCSRF() {
    const { json } = await this.request('/api/v1/auth/csrf');
    assert.equal(typeof json.csrf_token, 'string');
    this.csrf = json.csrf_token;
    return this.csrf;
  }

  async login(email, password = 'PoaktuDemo2026!') {
    await this.refreshCSRF();
    const result = await this.request('/api/v1/auth/login', {
      method: 'POST', body: { email, password },
    });
    await this.refreshCSRF();
    return result;
  }
}

async function ready() {
  for (let attempt = 0; attempt < 30; attempt++) {
    try {
      const response = await fetch(`${base}/readyz`, { redirect: 'error' });
      if (response.status === 200) {
        const body = await response.json();
        assert.equal(body.contour, 'test');
        assert.equal(body.stage, 'test_signatures');
        return;
      }
    } catch (error) {
      if (attempt === 29) throw error;
    }
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  throw new Error('Test signature API did not become ready');
}

await ready();
const unauthenticated = new Client();
const { json: noSession } = await unauthenticated.request('/api/v1/auth/me', { expected: 401 });
assert.equal(noSession.error.code, 'AUTH_REQUIRED');
assert.equal(typeof noSession.request_id, 'string');
const preAuthToken = await unauthenticated.refreshCSRF();
assert.equal(unauthenticated.cookies.has('pa_session'), false);

const { json: badOrigin } = await unauthenticated.request('/api/v1/auth/login', {
  method: 'POST', headers: { Origin: 'https://not-poaktu.invalid' },
  body: { email: 'customer@demo.poaktu.local', password: 'PoaktuDemo2026!' }, expected: 403,
});
assert.equal(badOrigin.error.code, 'CSRF_INVALID');

const suffix = randomUUID();
const registration = {
  email: `smoke-${suffix}@demo.poaktu.local`, password: 'SmokeOnlyPassword2026!',
  organisation: { name: `Учебная smoke-организация ${suffix}`, kind: 'COMPANY' },
};
const { json: roleInjection } = await unauthenticated.request('/api/v1/auth/register', {
  method: 'POST', body: { ...registration, system_role: 'DEMO_OPERATOR' }, expected: 400,
});
assert.equal(roleInjection.error.code, 'INVALID_REQUEST');
const { json: registered, response: registrationResponse } = await unauthenticated.request('/api/v1/auth/register', {
  method: 'POST', body: registration, expected: 201,
});
assert.equal(registered.user.system_role, 'USER');
assert.equal(registered.organisation.role, 'ORG_OWNER');
assert.match(registered.session_expires_at, /Z$/);
const sessionCookie = registrationResponse.headers.getSetCookie().find(value => value.startsWith('pa_session='));
assert(sessionCookie);
assert.match(sessionCookie, /HttpOnly/i);
assert.match(sessionCookie, /SameSite=Lax/i);
assert.doesNotMatch(sessionCookie, /;\s*Secure/i); // Explicit local HTTP test contour.

const { json: staleCSRF } = await unauthenticated.request('/api/v1/auth/logout', {
  method: 'POST', headers: { 'X-CSRF-Token': preAuthToken }, expected: 403,
});
assert.equal(staleCSRF.error.code, 'CSRF_INVALID');
await unauthenticated.refreshCSRF();
const { json: own } = await unauthenticated.request('/api/v1/organisations/me');
assert.equal(own.organisation.id, registered.organisation.id);
for (const key of ['total_kopecks', 'reserved_kopecks', 'available_kopecks']) assert.equal(own.account[key], '0');

const { json: catalog } = await unauthenticated.request('/api/v1/organisations?limit=1');
assert.equal(catalog.items.length, 1);
assert.deepEqual(Object.keys(catalog.items[0]).sort(), ['id', 'is_demo', 'kind', 'name']);
assert.equal(typeof catalog.next_cursor, 'string');
const { json: next } = await unauthenticated.request(`/api/v1/organisations?limit=1&cursor=${encodeURIComponent(catalog.next_cursor)}`);
assert.notEqual(next.items[0].id, catalog.items[0].id);

const customer = new Client();
const { json: customerIdentity } = await customer.login('customer@demo.poaktu.local');
assert.notEqual(customerIdentity.organisation.id, own.organisation.id);
const { json: customerAccount } = await customer.request('/api/v1/organisations/me');
assert.equal(customerAccount.account.total_kopecks, '50000000');
assert.equal(customerAccount.account.reserved_kopecks, '0');
assert.equal(customerAccount.account.available_kopecks, '50000000');

const supplier = new Client();
await supplier.login('supplier@demo.poaktu.local');
const { json: supplierAccount } = await supplier.request('/api/v1/organisations/me');
assert.equal(supplierAccount.account.total_kopecks, '0');
assert.notEqual(supplierAccount.organisation.id, customerIdentity.organisation.id);

const operator = new Client();
const { json: operatorIdentity } = await operator.login('operator@demo.poaktu.local');
assert.equal(operatorIdentity.user.system_role, 'DEMO_OPERATOR');
assert.equal(operatorIdentity.organisation, null);
const { json: operatorAccount } = await operator.request('/api/v1/organisations/me', { expected: 403 });
assert.equal(operatorAccount.error.code, 'FORBIDDEN');

const stateFile = process.env.SMOKE_STATE_FILE;
let previousState;
if (stateFile && existsSync(stateFile)) {
  const saved = JSON.parse(readFileSync(stateFile, 'utf8'));
  assert(['poaktu-deal-drafts-smoke-v1', 'poaktu-deal-reserves-smoke-v2', 'poaktu-test-signatures-smoke-v3'].includes(saved.kind));
  previousState = saved;
  const { json: persisted } = await customer.request(`/api/v1/deals/${encodeURIComponent(saved.id)}`);
  assert.equal(persisted.title, saved.body.title);
  const { json: replayed } = await customer.request('/api/v1/deals', {
    method: 'POST', body: saved.body, headers: { 'Idempotency-Key': saved.key }, expected: 201,
  });
  assert.equal(replayed.id, saved.id, 'Restart must preserve idempotent create results');
  if (saved.money) {
    assert.match(saved.money.email, /^smoke-[a-f0-9-]+@demo\.poaktu\.local$/);
    const previousCustomer = new Client();
    await previousCustomer.login(saved.money.email, 'SmokeOnlyPassword2026!');
    const { json: account } = await previousCustomer.request('/api/v1/organisations/me');
    assert.equal(account.account.id, saved.money.account_id);
    assert.equal(account.account.total_kopecks, '30000');
    assert.equal(account.account.reserved_kopecks, '30000');
    assert.equal(account.account.available_kopecks, '0');
    const { json: card } = await previousCustomer.request(`/api/v1/deals/${saved.money.deal_id}`);
    assert.equal(card.status, saved.money.signatures ? 'READY_TO_SETTLE' : 'FUNDED');
    assert.equal(card.agreements.length, 2);
    assert.deepEqual(card.reserve, saved.money.reserve_result.reserve);
    const { bytes } = await previousCustomer.request(`/api/v1/documents/${card.document.id}/download?version=${card.document.document_version}`, { binary: true });
    assert.equal(createHash('sha256').update(bytes).digest('hex'), card.document.sha256, 'Restart preserves exact private PDF bytes');
    const { json: reserveReplay } = await previousCustomer.request(`/api/v1/deals/${card.id}/reserve`, {
      method: 'POST', body: { deal_version: card.deal_version }, headers: { 'Idempotency-Key': saved.money.reserve_key }, expected: 201,
    });
    assert.deepEqual(reserveReplay, saved.money.reserve_result, 'Restart must replay the original reserve without a second hold');
    const { json: creditReplay } = await operator.request(`/api/v1/demo/accounts/${saved.money.account_id}/credits`, {
      method: 'POST', body: { amount_kopecks: '50000', reason: 'Учебное зачисление для smoke резервирования' }, headers: { 'Idempotency-Key': saved.money.credit_key }, expected: 201,
    });
    assert.equal(creditReplay.transaction_id, saved.money.credit_transaction_id, 'Restart must not credit again');
    const { json: debitReplay } = await operator.request(`/api/v1/demo/accounts/${saved.money.account_id}/debits`, {
      method: 'POST', body: { amount_kopecks: '20000', reason: 'Учебное списание только доступного остатка' }, headers: { 'Idempotency-Key': saved.money.debit_key }, expected: 201,
    });
    assert.equal(debitReplay.transaction_id, saved.money.debit_transaction_id, 'Restart must not debit again');
    if (saved.money.signatures) {
      const signed = saved.money.signatures;
      assert.deepEqual(card.signatures, signed.second_result.deal.signatures, 'Restart preserves the two exact signature facts');
      for (const [client, key, original] of [[previousCustomer, signed.customer_key, signed.first_result], [supplier, signed.supplier_key, signed.second_result]]) {
        const { json: replay } = await client.request('/api/v1/documents/' + card.document.id + '/sign', {
          method: 'POST', body: signed.input, headers: { 'Idempotency-Key': key }, expected: 201,
        });
        assert.deepEqual(replay, original, 'Restart preserves the original historical signature response');
      }
      const { json: current } = await previousCustomer.request('/api/v1/deals/' + card.id);
      assert.equal(current.status, 'READY_TO_SETTLE');
      assert.deepEqual(current.signatures, card.signatures, 'Replays do not append signatures or regress the current state');
      assert.deepEqual(current.reserve, card.reserve);
      assert.equal(current.payment_id, null);
    }
    const { json: afterReplay } = await previousCustomer.request('/api/v1/organisations/me');
    assert.deepEqual(afterReplay.account, account.account);
    await previousCustomer.request('/api/v1/auth/logout', { method: 'POST', expected: 204 });
  }
}

const dealBody = {
  supplier_party_id: supplierAccount.organisation.id,
  title: `Учебная печатная сделка ${suffix}`,
  description: 'Печать на упаковке заказчика. Синтетический черновик проверки API.',
  amount_kopecks: '30000000', currency: 'RUB',
  deadline_at: new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString(),
};
const dealKey = randomUUID();
const create = (body = dealBody, key = dealKey, expected = 201) => customer.request('/api/v1/deals', {
  method: 'POST', body, headers: { 'Idempotency-Key': key }, expected,
});
const { json: missingKey } = await customer.request('/api/v1/deals', {
  method: 'POST', body: dealBody, expected: 400,
});
assert.equal(missingKey.error.code, 'IDEMPOTENCY_KEY_REQUIRED');
const { json: injectedCustomer } = await create({ ...dealBody, customer_party_id: own.organisation.id }, dealKey, 400);
assert.equal(injectedCustomer.error.code, 'INVALID_REQUEST');
const { json: draft } = await create();
assert.equal(draft.status, 'DRAFT');
assert.equal(draft.deal_version, 1);
assert.equal(draft.customer_party_id, customerIdentity.organisation.id);
assert.equal(draft.supplier_party_id, supplierAccount.organisation.id);
assert.equal(draft.amount_kopecks, '30000000');
assert.equal(draft.customer.name, customerAccount.organisation.name);
assert.equal(draft.supplier.name, supplierAccount.organisation.name);
for (const field of ['document', 'reserve', 'payment_id', 'dispute', 'cancellation_request']) assert.equal(draft[field], null);
for (const field of ['agreements', 'signatures']) assert.deepEqual(draft[field], []);
const { json: replay } = await create();
assert.deepEqual(replay, draft);
const { json: normalizedReplay } = await create({ ...dealBody, title: `  ${dealBody.title}  ` });
assert.deepEqual(normalizedReplay, draft);
const { json: keyConflict } = await create({ ...dealBody, title: `${dealBody.title} исправление` }, dealKey, 409);
assert.equal(keyConflict.error.code, 'IDEMPOTENCY_CONFLICT');
const concurrentKey = randomUUID();
const concurrentBody = { ...dealBody, title: `Второй черновик ${suffix}` };
const simultaneous = await Promise.all([create(concurrentBody, concurrentKey), create(concurrentBody, concurrentKey)]);
assert.equal(simultaneous[0].json.id, simultaneous[1].json.id);
assert.notEqual(simultaneous[0].json.id, draft.id);

for (const client of [customer, supplier]) {
  const { json: card } = await client.request(`/api/v1/deals/${draft.id}`);
  assert.equal(card.id, draft.id);
  assert.equal(card.amount_kopecks, '30000000');
}
for (const client of [unauthenticated, operator]) {
  const { json: inaccessible } = await client.request(`/api/v1/deals/${draft.id}`, { expected: 404 });
  assert.equal(inaccessible.error.code, 'RESOURCE_NOT_FOUND');
}
await operator.request('/api/v1/deals', { expected: 403 });
await operator.request('/api/v1/deals', {
  method: 'POST', body: dealBody, headers: { 'Idempotency-Key': randomUUID() }, expected: 403,
});
const query = `q=${encodeURIComponent(suffix)}&status=DRAFT&role=customer&limit=1`;
const { json: page } = await customer.request(`/api/v1/deals?${query}`);
assert.equal(page.items.length, 1);
assert.equal(typeof page.next_cursor, 'string');
const { json: page2 } = await customer.request(`/api/v1/deals?${query}&cursor=${encodeURIComponent(page.next_cursor)}`);
assert.equal(page2.items.length, 1);
assert.notEqual(page2.items[0].id, page.items[0].id);
const { json: supplierDeals } = await supplier.request(`/api/v1/deals?q=${encodeURIComponent(suffix)}&role=supplier`);
assert.equal(supplierDeals.items.length, 2);
const { json: outsiderDeals } = await unauthenticated.request(`/api/v1/deals?q=${encodeURIComponent(suffix)}`);
assert.deepEqual(outsiderDeals.items, []);
const { json: invalidAmount } = await create({ ...dealBody, amount_kopecks: '030000000' }, randomUUID(), 400);
assert.equal(invalidAmount.error.code, 'INVALID_AMOUNT');
const { json: expired } = await create({ ...dealBody, deadline_at: '2020-01-01T00:00:00Z' }, randomUUID(), 422);
assert.equal(expired.error.code, 'DEADLINE_PASSED');
const { json: accountAfterDrafts } = await customer.request('/api/v1/organisations/me');
assert.deepEqual(accountAfterDrafts.account, customerAccount.account, 'Drafts must not reserve or transfer funds');
// Full implemented path: register -> credit -> draft -> PDF -> two consents ->
// reserve -> debit only available -> two signatures -> private PDF/read/replay after restart.
const moneyCreateKey = randomUUID();
const { json: moneyDraft } = await unauthenticated.request('/api/v1/deals', {
  method: 'POST', body: { ...dealBody, title: `Учебный резерв ${suffix}`, amount_kopecks: '30000' }, headers: { 'Idempotency-Key': moneyCreateKey }, expected: 201,
});
const { json: moneyDocument } = await unauthenticated.request(`/api/v1/deals/${moneyDraft.id}/documents/generate`, {
  method: 'POST', body: { expected_deal_version: moneyDraft.deal_version, template_id: 'printing-acceptance-v1' }, headers: { 'Idempotency-Key': randomUUID() }, expected: 201,
});
const agreement = { deal_version: moneyDocument.deal.deal_version, document_id: moneyDocument.document.id, document_version: moneyDocument.document.document_version, document_sha256: moneyDocument.document.sha256 };
for (const client of [unauthenticated, supplier]) await client.request(`/api/v1/deals/${moneyDraft.id}/agree`, { method: 'POST', body: agreement, headers: { 'Idempotency-Key': randomUUID() } });
const creditKey = randomUUID(), debitKey = randomUUID(), reserveKey = randomUUID();
const { json: credit } = await operator.request(`/api/v1/demo/accounts/${own.account.id}/credits`, {
  method: 'POST', body: { amount_kopecks: '50000', reason: 'Учебное зачисление для smoke резервирования' }, headers: { 'Idempotency-Key': creditKey }, expected: 201,
});
assert.equal(credit.account.total_kopecks, '50000');
const { json: reserved } = await unauthenticated.request(`/api/v1/deals/${moneyDraft.id}/reserve`, {
  method: 'POST', body: { deal_version: agreement.deal_version }, headers: { 'Idempotency-Key': reserveKey }, expected: 201,
});
assert.equal(reserved.deal.status, 'FUNDED');
assert.equal(reserved.deal.deal_version, agreement.deal_version);
assert.equal(reserved.reserve.amount_kopecks, '30000');
const { json: refusal } = await operator.request(`/api/v1/demo/accounts/${own.account.id}/debits`, {
  method: 'POST', body: { amount_kopecks: '20001', reason: 'Нельзя расходовать резерв' }, headers: { 'Idempotency-Key': randomUUID() }, expected: 422,
});
assert.equal(refusal.error.code, 'INSUFFICIENT_AVAILABLE_FUNDS');
const { json: debit } = await operator.request(`/api/v1/demo/accounts/${own.account.id}/debits`, {
  method: 'POST', body: { amount_kopecks: '20000', reason: 'Учебное списание только доступного остатка' }, headers: { 'Idempotency-Key': debitKey }, expected: 201,
});
assert.equal(debit.account.total_kopecks, '30000');
assert.equal(debit.account.reserved_kopecks, '30000');
assert.equal(debit.account.available_kopecks, '0');
const debitSnapshot = Object.fromEntries(['id', 'currency', 'total_kopecks', 'reserved_kopecks', 'available_kopecks'].map(field => [field, debit.account[field]]));
const signatureInput = { deal_version: agreement.deal_version, document_version: agreement.document_version, document_sha256: agreement.document_sha256 };
const customerSignatureKey = randomUUID(), supplierSignatureKey = randomUUID();
const signPath = '/api/v1/documents/' + moneyDocument.document.id + '/sign';
const { json: firstSignature } = await unauthenticated.request(signPath, {
  method: 'POST', body: signatureInput, headers: { 'Idempotency-Key': customerSignatureKey }, expected: 201,
});
assert.equal(firstSignature.deal.status, 'FUNDED');
assert.equal(firstSignature.deal.signatures.length, 1);
assert.equal(firstSignature.signature.organisation_id, own.organisation.id);
assert.equal(firstSignature.signature.document_sha256, agreement.document_sha256);
const { json: firstAccount } = await unauthenticated.request('/api/v1/organisations/me');
assert.deepEqual(firstAccount.account, debitSnapshot, 'First signature must not change any money');
const { json: secondSignature } = await supplier.request(signPath, {
  method: 'POST', body: signatureInput, headers: { 'Idempotency-Key': supplierSignatureKey }, expected: 201,
});
assert.equal(secondSignature.deal.status, 'READY_TO_SETTLE');
assert.equal(secondSignature.deal.signatures.length, 2);
assert.equal(secondSignature.signature.organisation_id, supplierAccount.organisation.id);
assert.deepEqual(secondSignature.deal.reserve, reserved.reserve);
assert.equal(secondSignature.deal.payment_id, null);
const { json: signatureReplay } = await unauthenticated.request(signPath, {
  method: 'POST', body: signatureInput, headers: { 'Idempotency-Key': customerSignatureKey }, expected: 201,
});
assert.deepEqual(signatureReplay, firstSignature, 'First signature replay is the original FUNDED response');
const { json: signedCard } = await unauthenticated.request('/api/v1/deals/' + moneyDraft.id);
assert.equal(signedCard.status, 'READY_TO_SETTLE');
assert.deepEqual(signedCard.signatures, secondSignature.deal.signatures);
const { json: signedAccount } = await unauthenticated.request('/api/v1/organisations/me');
assert.deepEqual(signedAccount.account, debitSnapshot, 'Two signatures do not consume or release the reserve');
const { json: supplierAfter } = await supplier.request('/api/v1/organisations/me');
assert.deepEqual(supplierAfter.account, supplierAccount.account, 'Reserve, signatures and operator debit must not pay the supplier');
const signatures = { input: signatureInput, customer_key: customerSignatureKey, supplier_key: supplierSignatureKey, first_result: firstSignature, second_result: secondSignature };
const moneyState = { signatures, email: registration.email, account_id: own.account.id, deal_id: moneyDraft.id, reserve_key: reserveKey, reserve_result: reserved, credit_key: creditKey, credit_transaction_id: credit.transaction_id, debit_key: debitKey, debit_transaction_id: debit.transaction_id };
if (stateFile && (!previousState?.money || !previousState.money.signatures)) {
  const saved = { ...(previousState ?? { id: draft.id, key: dealKey, body: dealBody }), kind: 'poaktu-test-signatures-smoke-v3', money: moneyState };
  writeFileSync(stateFile, JSON.stringify(saved), { flag: previousState ? 'w' : 'wx', mode: 0o600 });
}

for (const client of [unauthenticated, customer, supplier, operator]) {
  const revoked = new Client();
  revoked.cookies = new Map(client.cookies);
  await client.request('/api/v1/auth/logout', { method: 'POST', expected: 204 });
  await revoked.request('/api/v1/auth/me', { expected: 401 });
}

console.log('Test signature smoke passed: auth/CSRF, scoped drafts, private PDF, two explicit consents, atomic reserve, two exact test signatures and READY_TO_SETTLE without payout, balanced demo credit/debit, protected available funds, unchanged seed/supplier balances, restart persistence and exact command replay when state file exists, server logout.');
