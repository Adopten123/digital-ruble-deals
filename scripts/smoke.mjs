import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';

// Creates only synthetic demo users and drafts. Never resets data or moves funds.
const base = (process.env.BASE_URL ?? 'http://localhost:8080').replace(/\/$/, '');
const origin = process.env.APP_ORIGIN ?? base;
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(base).hostname), 'Smoke target must be loopback');

class Client {
  cookies = new Map();
  csrf = '';

  async request(path, { method = 'GET', body, headers = {}, expected = 200 } = {}) {
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
    const text = await response.text();
    const json = text ? JSON.parse(text) : null;
    assert.equal(response.status, expected, `${method} ${path}: ${json?.error?.code ?? response.status}`);
    return { response, json };
  }

  async refreshCSRF() {
    const { json } = await this.request('/api/v1/auth/csrf');
    assert.equal(typeof json.csrf_token, 'string');
    this.csrf = json.csrf_token;
    return this.csrf;
  }

  async login(email) {
    await this.refreshCSRF();
    const result = await this.request('/api/v1/auth/login', {
      method: 'POST', body: { email, password: 'PoaktuDemo2026!' },
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
        assert.equal(body.stage, 'deal_drafts');
        return;
      }
    } catch (error) {
      if (attempt === 29) throw error;
    }
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  throw new Error('Deal drafts API did not become ready');
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
if (stateFile && existsSync(stateFile)) {
  const saved = JSON.parse(readFileSync(stateFile, 'utf8'));
  assert.equal(saved.kind, 'poaktu-deal-drafts-smoke-v1');
  const { json: persisted } = await customer.request(`/api/v1/deals/${encodeURIComponent(saved.id)}`);
  assert.equal(persisted.title, saved.body.title);
  const { json: replayed } = await customer.request('/api/v1/deals', {
    method: 'POST', body: saved.body, headers: { 'Idempotency-Key': saved.key }, expected: 201,
  });
  assert.equal(replayed.id, saved.id, 'Restart must preserve idempotent create results');
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
if (stateFile && !existsSync(stateFile)) {
  writeFileSync(stateFile, JSON.stringify({ kind: 'poaktu-deal-drafts-smoke-v1', id: draft.id, key: dealKey, body: dealBody }), { flag: 'wx', mode: 0o600 });
}

for (const client of [unauthenticated, customer, supplier, operator]) {
  const revoked = new Client();
  revoked.cookies = new Map(client.cookies);
  await client.request('/api/v1/auth/logout', { method: 'POST', expected: 204 });
  await revoked.request('/api/v1/auth/me', { expected: 401 });
}

console.log('Deal drafts smoke passed: auth/CSRF, scoped create/list/card, replay/conflict/concurrent create, unchanged balances, operator isolation, persisted draft replay when state file exists, server logout.');
