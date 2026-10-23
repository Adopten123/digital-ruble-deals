import { randomUUID } from 'node:crypto';
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
  return client.post(path, { data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': key } });
}
async function account(client: APIRequestContext) { return (await ok(await client.get('/api/v1/organisations/me'))).account; }
function exactBalances(value: { total_kopecks: string; reserved_kopecks: string; available_kopecks: string }, total: bigint, reserved: bigint) {
  expect(value).toMatchObject({ total_kopecks: String(total), reserved_kopecks: String(reserved), available_kopecks: String(total - reserved) });
  expect(total >= reserved && reserved >= 0n).toBe(true);
}
async function laboratory(playwright: PlaywrightWorkerArgs['playwright']) {
  const customer = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  const operator = await apiFor(playwright, 'operator');
  const supplier = await apiFor(playwright, 'supplier');
  const outsider = await apiFor(playwright, 'outsider');
  const clients = [customer, operator, supplier, outsider];
  try {
    const identity = await ok(await customer.post('/api/v1/auth/register', {
      headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(customer) },
      data: { email: `reserve-${randomUUID()}@demo.poaktu.local`, password: 'ReserveSyntheticOnly2026!', organisation: { name: `Резерв учебный ${randomUUID()}`, kind: 'COMPANY' } },
    }), 201);
    const initial = await account(customer);
    exactBalances(initial, 0n, 0n);
    const path = `/api/v1/demo/accounts/${initial.id}`;
    return {
      customer, operator, supplier, outsider, identity, accountID: initial.id,
      credit: (amount: string, key = randomUUID()) => command(operator, `${path}/credits`, { amount_kopecks: amount, reason: 'Изолированный API-тест на учебных средствах' }, key),
      debit: (amount: string, key = randomUUID()) => command(operator, `${path}/debits`, { amount_kopecks: amount, reason: 'Изолированный API-тест доступного остатка' }, key),
      close: () => Promise.all(clients.map(client => client.dispose())),
    };
  } catch (failure) { await Promise.all(clients.map(client => client.dispose())); throw failure; }
}
type Lab = Awaited<ReturnType<typeof laboratory>>;
async function agreed(lab: Lab, amount = '30000') {
  const draft = await ok(await postDraft(lab.customer, draftInput({ amount_kopecks: amount })), 201);
  const generated = await ok(await command(lab.customer, `/api/v1/deals/${draft.id}/documents/generate`, { expected_deal_version: draft.deal_version, template_id: 'printing-acceptance-v1' }), 201);
  const deal = generated.deal;
  const input = { deal_version: deal.deal_version, document_id: deal.document.id, document_version: deal.document.document_version, document_sha256: deal.document.sha256 };
  await ok(await command(lab.customer, `/api/v1/deals/${deal.id}/agree`, input));
  return ok(await command(lab.supplier, `/api/v1/deals/${deal.id}/agree`, input));
}
const reserve = (lab: Lab, deal: { id: string; deal_version: number }, key = randomUUID()) => command(lab.customer, `/api/v1/deals/${deal.id}/reserve`, { deal_version: deal.deal_version }, key);
const read = (client: APIRequestContext, id: string) => client.get(`/api/v1/deals/${id}`).then(response => ok(response));

test('reserve locks the exact full amount without transferring funds and freezes the agreed document', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const amount = '9007199254740993';
    const supplierBefore = await account(lab.supplier);
    await ok(await lab.credit(amount), 201);
    const deal = await agreed(lab, amount);
    const funded = await ok(await reserve(lab, deal), 201);
    expect(funded.deal).toMatchObject({ id: deal.id, deal_version: deal.deal_version, status: 'FUNDED', document: deal.document, agreements: deal.agreements, payment_id: null, signatures: [] });
    expect(funded.reserve).toMatchObject({ id: expect.any(String), deal_id: deal.id, deal_version: deal.deal_version, account_id: lab.accountID, amount_kopecks: amount, currency: 'RUB', status: 'ACTIVE' });
    expect(funded.deal.reserve).toEqual(funded.reserve);
    expect(await read(lab.supplier, deal.id)).toEqual(funded.deal);
    exactBalances(await account(lab.customer), BigInt(amount), BigInt(amount));
    expect(await account(lab.supplier)).toEqual(supplierBefore);
    await error(await command(lab.customer, `/api/v1/deals/${deal.id}/rework`, { deal_version: deal.deal_version, reason: 'После резерва запрещено' }), 409, 'INVALID_STATE');
    await error(await command(lab.customer, `/api/v1/deals/${deal.id}/documents/generate`, { expected_deal_version: deal.deal_version, template_id: 'printing-acceptance-v1' }), 409, 'INVALID_STATE');
    await error(await lab.debit('1'), 422, 'INSUFFICIENT_AVAILABLE_FUNDS');
    expect(await read(lab.customer, deal.id)).toEqual(funded.deal);
  } finally { await lab.close(); }
});

test('insufficient funds are saved as the old result; a later credit requires a new reserve key', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await agreed(lab), key = randomUUID();
    const refused = await reserve(lab, deal, key);
    await error(refused, 422, 'INSUFFICIENT_AVAILABLE_FUNDS');
    const originalBytes = await refused.body();
    exactBalances(await account(lab.customer), 0n, 0n);
    expect((await read(lab.customer, deal.id)).reserve).toBeNull();
    await ok(await lab.credit('30000'), 201);
    const replay = await reserve(lab, deal, key);
    expect(replay.status()).toBe(422);
    expect(await replay.body()).toEqual(originalBytes);
    exactBalances(await account(lab.customer), 30000n, 0n);
    await error(await command(lab.customer, `/api/v1/deals/${deal.id}/reserve`, { deal_version: deal.deal_version + 1 }, key), 409, 'IDEMPOTENCY_CONFLICT');
    await ok(await reserve(lab, deal), 201);
    exactBalances(await account(lab.customer), 30000n, 30000n);
  } finally { await lab.close(); }
});

test('two independent deals compete for one account and cannot reserve the same available funds twice', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    await ok(await lab.credit('50000'), 201);
    const first = await agreed(lab), second = await agreed(lab);
    const token = await csrf(lab.customer);
    const results = await Promise.all([first, second].map(deal => lab.customer.post(`/api/v1/deals/${deal.id}/reserve`, { data: { deal_version: deal.deal_version }, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': token, 'Idempotency-Key': randomUUID() } })));
    expect(results.map(result => result.status()).sort()).toEqual([201, 422]);
    await error(results.find(result => result.status() === 422)!, 422, 'INSUFFICIENT_AVAILABLE_FUNDS');
    const cards = await Promise.all([first, second].map(deal => read(lab.customer, deal.id)));
    expect(cards.filter(deal => deal.reserve?.status === 'ACTIVE')).toHaveLength(1);
    expect(cards.map(deal => deal.status).sort()).toEqual(['AGREED', 'FUNDED']);
    exactBalances(await account(lab.customer), 50000n, 30000n);
    await error(await lab.debit('20001'), 422, 'INSUFFICIENT_AVAILABLE_FUNDS');
    await ok(await lab.debit('20000'), 201);
    exactBalances(await account(lab.customer), 30000n, 30000n);
  } finally { await lab.close(); }
});

test('a reserve competes atomically with an operator debit and all later debits still respect active reserves', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    await ok(await lab.credit('50000'), 201);
    const deal = await agreed(lab);
    const [reserveResponse, debitResponse] = await Promise.all([reserve(lab, deal), lab.debit('30000')]);
    expect([reserveResponse.status(), debitResponse.status()].sort()).toEqual([201, 422]);
    await error(reserveResponse.status() === 422 ? reserveResponse : debitResponse, 422, 'INSUFFICIENT_AVAILABLE_FUNDS');
    const held = reserveResponse.status() === 201 ? 30000n : 0n;
    const total = reserveResponse.status() === 201 ? 50000n : 20000n;
    exactBalances(await account(lab.customer), total, held);
    const responses = await Promise.all(Array.from({ length: 8 }, () => lab.debit('5000')));
    expect(responses.filter(response => response.status() === 201)).toHaveLength(4);
    for (const response of responses.filter(response => response.status() !== 201)) await error(response, 422, 'INSUFFICIENT_AVAILABLE_FUNDS');
    exactBalances(await account(lab.customer), held, held);
    const current = await read(lab.customer, deal.id);
    expect(current.reserve?.status ?? null).toBe(held ? 'ACTIVE' : null);
  } finally { await lab.close(); }
});

test('parallel duplicates replay the identical reserve and a different key cannot add another hold', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    await ok(await lab.credit('100000'), 201);
    const deal = await agreed(lab), key = randomUUID();
    const results = await Promise.all(Array.from({ length: 8 }, () => reserve(lab, deal, key)));
    for (const result of results) expect(result.status(), await result.text()).toBe(201);
    const bytes = await results[0].body();
    for (const result of results) expect(await result.body()).toEqual(bytes);
    const original = await results[0].json();
    await error(await reserve(lab, deal), 409, 'INVALID_STATE');
    await error(await command(lab.customer, `/api/v1/deals/${deal.id}/reserve`, { deal_version: deal.deal_version + 1 }, key), 409, 'IDEMPOTENCY_CONFLICT');
    expect((await read(lab.customer, deal.id)).reserve.id).toBe(original.reserve.id);
    exactBalances(await account(lab.customer), 100000n, 30000n);
  } finally { await lab.close(); }
});

test('rework and reserve serialize on the same deal so only one transition wins', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    await ok(await lab.credit('50000'), 201);
    const deal = await agreed(lab);
    const [reserved, reworked] = await Promise.all([reserve(lab, deal), command(lab.supplier, `/api/v1/deals/${deal.id}/rework`, { deal_version: deal.deal_version, reason: 'Гонка доработки и резерва' })]);
    const current = await read(lab.customer, deal.id);
    if (reserved.status() === 201) {
      await error(reworked, 409, 'INVALID_STATE');
      expect(current).toMatchObject({ status: 'FUNDED', deal_version: deal.deal_version, reserve: { status: 'ACTIVE' } });
      exactBalances(await account(lab.customer), 50000n, 30000n);
    } else {
      await ok(reworked);
      await error(reserved, 409, 'VERSION_CONFLICT');
      expect(current).toMatchObject({ status: 'DRAFT', deal_version: deal.deal_version + 1, document: null, agreements: [], reserve: null });
      exactBalances(await account(lab.customer), 50000n, 0n);
    }
  } finally { await lab.close(); }
});

test('roles, document/version prerequisites and the operator account catalogue are enforced by the server', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  const anonymous = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    const draft = await ok(await postDraft(lab.customer), 201);
    await error(await reserve(lab, draft), 409, 'INVALID_STATE');
    const deal = await agreed(lab);
    await error(await command(lab.supplier, `/api/v1/deals/${deal.id}/reserve`, { deal_version: deal.deal_version }), 403, 'FORBIDDEN');
    await error(await command(lab.outsider, `/api/v1/deals/${deal.id}/reserve`, { deal_version: deal.deal_version }), 404, 'RESOURCE_NOT_FOUND');
    await error(await command(lab.operator, `/api/v1/deals/${deal.id}/reserve`, { deal_version: deal.deal_version }), 404, 'RESOURCE_NOT_FOUND');
    await error(await command(lab.customer, `/api/v1/deals/${deal.id}/reserve`, { deal_version: deal.deal_version - 1 }), 409, 'VERSION_CONFLICT');
    await error(await anonymous.get('/api/v1/demo/accounts'), 401, 'AUTH_REQUIRED');
    await error(await lab.customer.get('/api/v1/demo/accounts'), 403, 'FORBIDDEN');
    await error(await command(lab.customer, `/api/v1/demo/accounts/${lab.accountID}/credits`, { amount_kopecks: '1', reason: 'Подмена роли' }), 403, 'FORBIDDEN');
    const found = await ok(await lab.operator.get('/api/v1/demo/accounts', { params: { q: lab.accountID, limit: 1 } }));
    expect(found.items).toHaveLength(1);
    expect(found.items[0]).toMatchObject({ id: lab.accountID, organisation: { id: lab.identity.organisation.id }, total_kopecks: '0', available_kopecks: '0', reserved_kopecks: '0' });
    expect(found.items[0]).not.toHaveProperty('email');
    const first = await ok(await lab.operator.get('/api/v1/demo/accounts', { params: { limit: 1 } }));
    expect(first.next_cursor).toEqual(expect.any(String));
    const second = await ok(await lab.operator.get('/api/v1/demo/accounts', { params: { limit: 1, cursor: first.next_cursor } }));
    expect(second.items[0].id).not.toBe(first.items[0].id);
    expect((await lab.operator.get('/api/v1/demo/accounts', { params: { limit: 2, cursor: first.next_cursor } })).status()).toBe(400);
    expect((await lab.operator.get('/api/v1/demo/accounts', { params: { q: 'changed', limit: 1, cursor: first.next_cursor } })).status()).toBe(400);
    expect((await lab.operator.get('/api/v1/demo/accounts?limit=0')).status()).toBe(400);
    exactBalances(await account(lab.customer), 0n, 0n);
  } finally { await anonymous.dispose(); await lab.close(); }
});

test('operator commands keep exact amounts, durable outcomes and authenticated transport without overflowing balances', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const key = randomUUID(), credited = await lab.credit('70000', key);
    const outcome = await ok(credited, 201);
    expect(outcome.transaction_id).toEqual(expect.any(String));
    expect(await ok(await lab.credit('70000', key), 201)).toEqual(outcome);
    await error(await lab.credit('70001', key), 409, 'IDEMPOTENCY_CONFLICT');
    exactBalances(await account(lab.customer), 70000n, 0n);
    const refusedKey = randomUUID(), refused = await lab.debit('70001', refusedKey);
    await error(refused, 422, 'INSUFFICIENT_AVAILABLE_FUNDS');
    await ok(await lab.credit('1'), 201);
    expect(await (await lab.debit('70001', refusedKey)).body()).toEqual(await refused.body());
    await error(await lab.credit('9223372036854775807'), 422, 'AMOUNT_OVERFLOW');
    exactBalances(await account(lab.customer), 70001n, 0n);
    for (const amount of [0, '0', '-1', '01', '1.5', '9223372036854775808']) {
      expect((await command(lab.operator, `/api/v1/demo/accounts/${lab.accountID}/debits`, { amount_kopecks: amount, reason: 'Некорректная сумма' })).status()).toBe(400);
    }
    await error(await command(lab.operator, `/api/v1/demo/accounts/${lab.accountID}/credits`, { amount_kopecks: '1', reason: '  ' }), 400, 'INVALID_REQUEST');
    const token = await csrf(lab.operator), path = `/api/v1/demo/accounts/${lab.accountID}/credits`;
    await error(await lab.operator.post(path, { data: { amount_kopecks: '1', reason: 'Без CSRF' }, headers: { Origin: E2E_BASE_URL, 'Idempotency-Key': randomUUID() } }), 403, 'CSRF_INVALID');
    expect((await lab.operator.post(path, { data: { amount_kopecks: '1', reason: 'Без ключа' }, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': token } })).status()).toBe(400);
    await error(await lab.operator.post(path, { data: { amount_kopecks: '1', reason: 'Смена аккаунта' }, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': token, 'Idempotency-Key': randomUUID(), 'X-Expected-User-ID': lab.identity.user.id } }), 409, 'SESSION_CHANGED');
    await ok(await lab.debit('70001'), 201);
    exactBalances(await account(lab.customer), 0n, 0n);
  } finally { await lab.close(); }
});
