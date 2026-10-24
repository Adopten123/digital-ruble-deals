import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import type { APIRequestContext, APIResponse, PlaywrightWorkerArgs } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, E2E_BASE_URL, API_BASE_URL } from '../support.ts';

async function ok(response: APIResponse, status = 200) {
  expect(response.status(), await response.text()).toBe(status);
  return response.json();
}
async function error(response: APIResponse, status: number, code: string) {
  expect(response.status(), await response.text()).toBe(status);
  expect(await response.json()).toMatchObject({ error: { code }, request_id: expect.any(String) });
}
async function command(client: APIRequestContext, path: string, data: unknown, key = randomUUID()) {
  return client.post(path, {
    data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': key },
  });
}
async function account(client: APIRequestContext) { return (await ok(await client.get('/api/v1/organisations/me'))).account; }
async function read(client: APIRequestContext, id: string) { return ok(await client.get(`/api/v1/deals/${id}`)); }
async function registerSynthetic(client: APIRequestContext) {
  const data = {
    email: `signature-${randomUUID()}@demo.poaktu.local`, password: 'SignatureSyntheticOnly2026!',
    organisation: { name: `Подписи учебные ${randomUUID()}`, kind: 'COMPANY' },
  };
  for (let attempt = 0; attempt < 2; attempt++) {
    const response = await client.post('/api/v1/auth/register', {
      headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) }, data,
    });
    if (response.status() !== 429 || attempt === 1) return ok(response, 201);
    // The fixture shares the real auth budget with earlier tests. Retry only an
    // explicit refusal, with the same registration data and the server's delay.
    await error(response, 429, 'RATE_LIMITED');
    const seconds = Number(response.headers()['retry-after']);
    expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true);
    test.setTimeout(test.info().timeout + seconds * 1000);
    await test.step(`respect signature fixture auth budget: wait ${seconds}s before one retry`, async () => { await delay(seconds * 1000); });
  }
  throw new Error('Synthetic signature registration did not run');
}
async function laboratory(playwright: PlaywrightWorkerArgs['playwright']) {
  const customer = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  const operator = await apiFor(playwright, 'operator');
  const supplier = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  const outsider = await apiFor(playwright, 'outsider');
  const clients = [customer, operator, supplier, outsider];
  try {
    const identity = await registerSynthetic(customer);
    const supplierIdentity = await registerSynthetic(supplier);
    const initial = await account(customer);
    expect(initial).toMatchObject({ total_kopecks: '0', reserved_kopecks: '0', available_kopecks: '0' });
    await ok(await command(operator, `/api/v1/demo/accounts/${initial.id}/credits`, {
      amount_kopecks: '100000', reason: 'Изолированный API-тест подписей на учебных средствах',
    }), 201);
    return { customer, operator, supplier, outsider, identity, supplierIdentity, close: () => Promise.all(clients.map(client => client.dispose())) };
  } catch (failure) { await Promise.all(clients.map(client => client.dispose())); throw failure; }
}
type Lab = Awaited<ReturnType<typeof laboratory>>;
type DocumentBinding = { deal_version: number; document_version: number; document_sha256: string };
type DocumentDeal = { id: string; deal_version: number; document: { id: string; document_version: number; sha256: string } };
type Signature = DocumentBinding & { id: string; organisation_id: string; user_id: string; document_id: string; created_at: string };
const body = (deal: DocumentDeal): DocumentBinding => ({
  deal_version: deal.deal_version, document_version: deal.document.document_version, document_sha256: deal.document.sha256,
});
const sign = (client: APIRequestContext, deal: DocumentDeal, key = randomUUID(), data: unknown = body(deal)) =>
  command(client, `/api/v1/documents/${deal.document.id}/sign`, data, key);
async function generated(lab: Lab, deadline?: string) {
  const draft = await ok(await postDraft(lab.customer, draftInput({ supplier_party_id: lab.supplierIdentity.organisation.id, amount_kopecks: '30000', ...(deadline ? { deadline_at: deadline } : {}) })), 201);
  return (await ok(await command(lab.customer, `/api/v1/deals/${draft.id}/documents/generate`, {
    expected_deal_version: draft.deal_version, template_id: 'printing-acceptance-v1',
  }), 201)).deal;
}
async function agree(lab: Lab, deal: DocumentDeal) {
  const input = { ...body(deal), document_id: deal.document.id };
  await ok(await command(lab.customer, `/api/v1/deals/${deal.id}/agree`, input));
  return ok(await command(lab.supplier, `/api/v1/deals/${deal.id}/agree`, input));
}
async function fund(lab: Lab, deal: DocumentDeal) {
  return (await ok(await command(lab.customer, `/api/v1/deals/${deal.id}/reserve`, { deal_version: deal.deal_version }), 201)).deal;
}
async function funded(lab: Lab) { return fund(lab, await agree(lab, await generated(lab))); }
async function balances(lab: Lab) { return Promise.all([account(lab.customer), account(lab.supplier)]); }
async function settled(lab: Lab, deal: DocumentDeal, before: any[]) {
  await expect.poll(async () => (await read(lab.customer, deal.id)).status, { timeout: 20_000, intervals: [100, 250, 500] }).toBe('SETTLED');
  const current = await read(lab.customer, deal.id);
  expect(current).toMatchObject({ deal_version: deal.deal_version, reserve: { status: 'CONSUMED', amount_kopecks: '30000' }, payment_id: expect.any(String) });
  const after = await balances(lab);
  expect(after[0]).toEqual({ ...before[0], total_kopecks: (BigInt(before[0].total_kopecks) - 30000n).toString(), reserved_kopecks: (BigInt(before[0].reserved_kopecks) - 30000n).toString() });
  expect(after[1]).toEqual({ ...before[1], total_kopecks: (BigInt(before[1].total_kopecks) + 30000n).toString(), available_kopecks: (BigInt(before[1].available_kopecks) + 30000n).toString() });
  return current;
}
function bound(signature: Signature, deal: DocumentDeal, organisationID: string, userID: string) {
  expect(signature).toEqual({
    id: expect.any(String), organisation_id: organisationID, user_id: userID,
    ...body(deal), document_id: deal.document.id, created_at: expect.any(String),
  });
  expect(Number.isFinite(Date.parse(signature.created_at))).toBe(true);
}

test('two exact party signatures publish READY before the verified EDO delivery consumes the reserve once', async ({ playwright, actors }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await funded(lab), before = await balances(lab);
    const first = await ok(await sign(lab.supplier, deal), 201);
    bound(first.signature, deal, lab.supplierIdentity.organisation.id, lab.supplierIdentity.user.id);
    expect(first.deal).toMatchObject({
      id: deal.id, deal_version: deal.deal_version, status: 'FUNDED', document: deal.document,
      reserve: deal.reserve, agreements: deal.agreements, payment_id: null, signatures: [first.signature],
    });
    expect(await balances(lab)).toEqual(before);
    const second = await ok(await sign(lab.customer, deal), 201);
    bound(second.signature, deal, lab.identity.organisation.id, lab.identity.user.id);
    expect(second.signature.id).not.toBe(first.signature.id);
    expect(second.deal).toMatchObject({ status: 'READY_TO_SETTLE', deal_version: deal.deal_version, reserve: deal.reserve, payment_id: null });
    expect(second.deal.signatures).toHaveLength(2);
    expect(second.deal.signatures).toEqual(expect.arrayContaining([first.signature, second.signature]));
    const current = await settled(lab, deal, before);
    expect(current.signatures).toEqual(second.deal.signatures);
    const list = await ok(await lab.customer.get('/api/v1/deals', { params: { status: 'SETTLED', q: deal.id } }));
    expect(list.items).toHaveLength(1);
    expect(list.items[0]).toEqual(current);
    await error(await command(lab.customer, `/api/v1/deals/${deal.id}/rework`, { deal_version: deal.deal_version, reason: 'Подписи не открывают редактирование' }), 409, 'INVALID_STATE');
    await error(await command(lab.customer, `/api/v1/deals/${deal.id}/documents/generate`, { expected_deal_version: deal.deal_version, template_id: 'printing-acceptance-v1' }), 409, 'INVALID_STATE');
  } finally { await lab.close(); }
});

test('same-key signature replay retains its historical response while a new key returns the existing fact and current deal', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await funded(lab), key = randomUUID(), before = await balances(lab);
    const first = await sign(lab.customer, deal, key);
    const original = await ok(first, 201), bytes = await first.body();
    await error(await sign(lab.customer, deal, key, { ...body(deal), document_sha256: '0'.repeat(64) }), 409, 'IDEMPOTENCY_CONFLICT');
    const second = await ok(await sign(lab.supplier, deal), 201);
    const replay = await sign(lab.customer, deal, key);
    expect(replay.status()).toBe(201);
    expect(await replay.body()).toEqual(bytes);
    expect((await replay.json()).deal.status).toBe('FUNDED');
    const current = await settled(lab, deal, before);
    const newKey = await ok(await sign(lab.customer, deal), 201);
    expect(newKey.signature).toEqual(original.signature);
    expect(newKey.deal).toEqual(current);
    expect(newKey.deal.signatures).toHaveLength(2);
    expect(await read(lab.customer, deal.id)).toEqual(current);
  } finally { await lab.close(); }
});

test('concurrent keys from one party and simultaneous signatures from both parties create exactly two facts', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await funded(lab), before = await balances(lab), key = randomUUID();
    const token = await csrf(lab.customer);
    const repeats = await Promise.all(Array.from({ length: 6 }, () => lab.customer.post(`/api/v1/documents/${deal.document.id}/sign`, {
      data: body(deal), headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': token, 'Idempotency-Key': key },
    })));
    for (const response of repeats) expect(response.status(), await response.text()).toBe(201);
    const original = await repeats[0].body();
    for (const response of repeats) expect(await response.body()).toEqual(original);
    expect((await read(lab.customer, deal.id)).signatures).toHaveLength(1);
    const commands = await Promise.all(Array.from({ length: 6 }, (_, index) => sign(index % 2 ? lab.supplier : lab.customer, deal)));
    const outcomes = [];
    for (const response of commands) outcomes.push(await ok(response, 201));
    const current = await settled(lab, deal, before);
    expect(current.signatures).toHaveLength(2);
    expect(new Set(current.signatures.map((signature: Signature) => signature.organisation_id)).size).toBe(2);
    expect(new Set(outcomes.map(outcome => outcome.signature.id)).size).toBe(2);
  } finally { await lab.close(); }
});

test('two first signatures racing on an unsigned funded deal both survive without changing the agreed version', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await funded(lab), before = await balances(lab);
    const [customer, supplier] = await Promise.all([csrf(lab.customer), csrf(lab.supplier)]);
    const participants: [APIRequestContext, string][] = [[lab.customer, customer], [lab.supplier, supplier]];
    const responses = await Promise.all(participants.map(([client, token]) =>
      client.post(`/api/v1/documents/${deal.document.id}/sign`, {
        data: body(deal), headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': token, 'Idempotency-Key': randomUUID() },
      })));
    const results = [];
    for (const response of responses) results.push(await ok(response, 201));
    expect(results.map(result => result.deal.status).sort()).toEqual(['FUNDED', 'READY_TO_SETTLE']);
    const current = await settled(lab, deal, before);
    expect(current.signatures).toHaveLength(2);
    expect(current.signatures).toEqual(expect.arrayContaining(results.map(result => result.signature)));
  } finally { await lab.close(); }
});

test('a pre-reserve refusal remains the saved result after funding and requires a new signature key', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const attached = await generated(lab), key = randomUUID();
    const refused = await sign(lab.customer, attached, key);
    await error(refused, 409, 'INVALID_STATE');
    const original = await refused.body();
    const deal = await fund(lab, await agree(lab, attached)), before = await balances(lab);
    const replay = await sign(lab.customer, deal, key);
    expect(replay.status()).toBe(409);
    expect(await replay.body()).toEqual(original);
    expect((await read(lab.customer, deal.id)).signatures).toEqual([]);
    const signed = await ok(await sign(lab.customer, deal), 201);
    expect(signed.deal).toMatchObject({ status: 'FUNDED', signatures: [signed.signature], reserve: deal.reserve, payment_id: null });
    expect(await balances(lab)).toEqual(before);
  } finally { await lab.close(); }
});

test('document binding and party privacy reject mismatched versions, hashes and foreign document routes before writing a signature', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await funded(lab), other = await funded(lab), before = await balances(lab);
    await error(await sign(lab.customer, deal, randomUUID(), { ...body(deal), deal_version: deal.deal_version - 1 }), 409, 'VERSION_CONFLICT');
    await error(await sign(lab.customer, deal, randomUUID(), { ...body(deal), document_version: deal.document.document_version + 1 }), 409, 'DOCUMENT_MISMATCH');
    await error(await sign(lab.customer, deal, randomUUID(), { ...body(deal), document_sha256: '0'.repeat(64) }), 409, 'DOCUMENT_MISMATCH');
    await error(await sign(lab.customer, other, randomUUID(), body(deal)), 409, 'DOCUMENT_MISMATCH');
    await error(await command(lab.customer, `/api/v1/documents/${randomUUID()}/sign`, body(deal)), 404, 'RESOURCE_NOT_FOUND');
    for (const client of [lab.outsider, lab.operator]) await error(await sign(client, deal), 404, 'RESOURCE_NOT_FOUND');
    expect((await read(lab.customer, deal.id)).signatures).toEqual([]);
    expect((await read(lab.supplier, other.id)).signatures).toEqual([]);
    expect(await balances(lab)).toEqual(before);
  } finally { await lab.close(); }
});

test('an earlier PDF version cannot be signed after the replacement was agreed and funded', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const old = await generated(lab);
    const newer = (await ok(await command(lab.customer, `/api/v1/deals/${old.id}/documents/generate`, {
      expected_deal_version: old.deal_version, template_id: 'printing-acceptance-v1',
    }), 201)).deal;
    const deal = await fund(lab, await agree(lab, newer)), before = await balances(lab);
    expect(deal.document.id).toBe(old.document.id);
    expect(deal.document.document_version).toBe(old.document.document_version + 1);
    await error(await sign(lab.supplier, deal, randomUUID(), {
      ...body(deal), document_version: old.document.document_version, document_sha256: old.document.sha256,
    }), 409, 'DOCUMENT_MISMATCH');
    const accepted = await ok(await sign(lab.supplier, deal), 201);
    expect(accepted.signature.document_version).toBe(deal.document.document_version);
    expect(accepted.deal.signatures).toHaveLength(1);
    expect(await balances(lab)).toEqual(before);
  } finally { await lab.close(); }
});

test('signature commands require authenticated CSRF transport, an exact actor and a strict body without role overrides', async ({ playwright, actors }) => {
  const lab = await laboratory(playwright);
  const anonymous = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  try {
    const deal = await funded(lab), path = `/api/v1/documents/${deal.document.id}/sign`, data = body(deal);
    await error(await command(anonymous, path, data), 401, 'AUTH_REQUIRED');
    await error(await lab.customer.post(path, { data, headers: { Origin: E2E_BASE_URL, 'Idempotency-Key': randomUUID() } }), 403, 'CSRF_INVALID');
    const token = await csrf(lab.customer), headers = { Origin: E2E_BASE_URL, 'X-CSRF-Token': token, 'Idempotency-Key': randomUUID() };
    expect((await lab.customer.post(path, { data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': token } })).status()).toBe(400);
    await error(await lab.customer.post(path, { data, headers: { ...headers, 'X-Expected-User-ID': actors.supplier.user.id } }), 409, 'SESSION_CHANGED');
    for (const invalid of [
      { ...data, organisation_id: actors.supplier.organisation!.id }, { ...data, user_id: actors.supplier.user.id },
      { ...data, system_role: 'DEMO_OPERATOR' }, { ...data, deal_version: 0 },
      { ...data, document_version: '1' }, { ...data, document_sha256: 'invalid' },
    ]) expect((await command(lab.customer, path, invalid)).status()).toBe(400);
    expect((await read(lab.customer, deal.id)).signatures).toEqual([]);
    const signed = await ok(await sign(lab.customer, deal), 201);
    bound(signed.signature, deal, lab.identity.organisation.id, lab.identity.user.id);
  } finally { await anonymous.dispose(); await lab.close(); }
});

test('a new HTTP client recovers the original persisted signature response and leaves the completed pair unchanged', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  let reconnected: APIRequestContext | undefined;
  try {
    const deal = await funded(lab), key = randomUUID(), before = await balances(lab);
    const committed = await sign(lab.customer, deal, key), result = await ok(committed, 201), bytes = await committed.body();
    // Reconnection is covered here; an actual lost network reply and process restart are tested separately.
    reconnected = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: await lab.customer.storageState() });
    const completed = await ok(await sign(lab.supplier, deal), 201);
    const replay = await sign(reconnected, deal, key);
    expect(replay.status()).toBe(201);
    expect(await replay.body()).toEqual(bytes);
    expect((await replay.json()).signature).toEqual(result.signature);
    const current = await settled(lab, deal, before);
    expect(await read(reconnected, deal.id)).toEqual(current);
    expect(current.signatures).toEqual(completed.deal.signatures);
  } finally { await reconnected?.dispose(); await lab.close(); }
});

test('expiry blocks the missing second signature without releasing funds or erasing the first signature', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const attached = await generated(lab, new Date(Date.now() + 5_000).toISOString());
    const deal = await fund(lab, await agree(lab, attached)), before = await balances(lab), key = randomUUID();
    const first = await sign(lab.supplier, deal, key), original = await ok(first, 201), bytes = await first.body();
    await expect.poll(async () => (await read(lab.customer, deal.id)).status, { timeout: 10_000, intervals: [100, 200, 500] }).toBe('REVIEW_REQUIRED');
    await error(await sign(lab.customer, deal), 422, 'DEADLINE_PASSED');
    const current = await read(lab.customer, deal.id);
    expect(current).toMatchObject({ status: 'REVIEW_REQUIRED', signatures: [original.signature], reserve: deal.reserve, payment_id: null });
    const replay = await sign(lab.supplier, deal, key);
    expect(replay.status()).toBe(201);
    expect(await replay.body()).toEqual(bytes);
    const existingFact = await ok(await sign(lab.supplier, deal), 201);
    expect(existingFact.signature).toEqual(original.signature);
    expect(existingFact.deal).toEqual(current);
    expect(await balances(lab)).toEqual(before);
  } finally { await lab.close(); }
});
