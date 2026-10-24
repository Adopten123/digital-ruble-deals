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
  return client.post(path, { data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': key } });
}
async function register(client: APIRequestContext) {
  const data = { email: 'settlement-' + randomUUID() + '@demo.poaktu.local', password: 'SettlementOnlyPassword2026!',
    organisation: { name: 'Расчёт учебный ' + randomUUID(), kind: 'COMPANY' } };
  for (let attempt = 0; attempt < 2; attempt++) {
    const response = await client.post('/api/v1/auth/register', { data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) } });
    if (response.status() !== 429 || attempt === 1) return ok(response, 201);
    await error(response, 429, 'RATE_LIMITED');
    const seconds = Number(response.headers()['retry-after']);
    expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true);
    test.setTimeout(test.info().timeout + seconds * 1000);
    await test.step('respect explicit auth Retry-After before one fixture retry', () => delay(seconds * 1000));
  }
  throw new Error('No registration result');
}
async function laboratory(playwright: PlaywrightWorkerArgs['playwright'], credit = '100000') {
  const customer = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  const supplier = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  const operator = await apiFor(playwright, 'operator'), outsider = await apiFor(playwright, 'outsider');
  const clients = [customer, supplier, operator, outsider];
  try {
    const customerIdentity = await register(customer), supplierIdentity = await register(supplier);
    const account = (await ok(await customer.get('/api/v1/organisations/me'))).account;
    await ok(await command(operator, '/api/v1/demo/accounts/' + account.id + '/credits',
      { amount_kopecks: credit, reason: 'Изолированная проверка расчёта, не реальные средства' }), 201);
    return { customer, supplier, operator, outsider, customerIdentity, supplierIdentity, close: () => Promise.all(clients.map(c => c.dispose())) };
  } catch (e) { await Promise.all(clients.map(c => c.dispose())); throw e; }
}
type Lab = Awaited<ReturnType<typeof laboratory>>;
async function read(lab: Lab, id: string) { return ok(await lab.customer.get('/api/v1/deals/' + id)); }
async function account(client: APIRequestContext) { return (await ok(await client.get('/api/v1/organisations/me'))).account; }
async function balances(lab: Lab) { return Promise.all([account(lab.customer), account(lab.supplier)]); }
function signature(deal: any) { return { deal_version: deal.deal_version, document_version: deal.document.document_version, document_sha256: deal.document.sha256 }; }
async function draft(lab: Lab, deadline?: string) {
  return ok(await postDraft(lab.customer, draftInput({ supplier_party_id: lab.supplierIdentity.organisation.id, amount_kopecks: '30000',
    ...(deadline ? { deadline_at: deadline } : {}) })), 201);
}
async function funded(lab: Lab, deadline?: string) {
  const created = await draft(lab, deadline);
  const generated = await ok(await command(lab.customer, '/api/v1/deals/' + created.id + '/documents/generate',
    { expected_deal_version: created.deal_version, template_id: 'printing-acceptance-v1' }), 201);
  const binding = { ...signature(generated.deal), document_id: generated.document.id };
  for (const client of [lab.customer, lab.supplier]) await ok(await command(client, '/api/v1/deals/' + created.id + '/agree', binding));
  return (await ok(await command(lab.customer, '/api/v1/deals/' + created.id + '/reserve', { deal_version: generated.deal.deal_version }), 201)).deal;
}
const path = (deal: any) => '/api/v1/deals/' + deal.id;
const decisionPath = (deal: any, request: any, action: string) => path(deal) + '/cancellation-requests/' + request.id + '/' + action;
async function requestCancellation(lab: Lab, deal: any, client = lab.customer, key = randomUUID()) {
  return ok(await command(client, path(deal) + '/cancellation-requests', { deal_version: deal.deal_version, reason: 'Согласованное завершение учебной сделки' }, key), 201);
}
async function sendEvent(lab: Lab, deal: any, template: string, replayID?: string, key = randomUUID()) {
  const input = { deal_id: deal.id, template, ...(replayID ? { replay_event_id: replayID } : {}) };
  return ok(await command(lab.operator, '/api/v1/demo/edo/events', input, key), 202);
}
async function delivered(lab: Lab, eventID: string) {
  await expect.poll(async () => (await ok(await lab.operator.get('/api/v1/demo/edo/events/' + eventID))).status,
    { timeout: 20_000, intervals: [100, 250, 500] }).toBe('DELIVERED');
  return ok(await lab.operator.get('/api/v1/demo/edo/events/' + eventID));
}
async function signBoth(lab: Lab, deal: any) {
  const input = signature(deal), route = '/api/v1/documents/' + deal.document.id + '/sign';
  const first = await ok(await command(lab.customer, route, input), 201);
  expect(first.deal).toMatchObject({ status: 'FUNDED', payment_id: null, reserve: { status: 'ACTIVE' } });
  expect(first.deal.signatures).toHaveLength(1);
  const second = await ok(await command(lab.supplier, route, input), 201);
  expect(second.deal).toMatchObject({ status: 'READY_TO_SETTLE', payment_id: null, reserve: { status: 'ACTIVE' } });
  await expect.poll(async () => (await read(lab, deal.id)).status, { timeout: 20_000, intervals: [100, 250, 500] }).toBe('SETTLED');
  return read(lab, deal.id);
}

test('one cancellation vote holds funds and the other organisation releases exactly once with durable historical replay', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await funded(lab), before = await balances(lab), key = randomUUID();
    const originalResponse = await command(lab.customer, path(deal) + '/cancellation-requests',
      { deal_version: deal.deal_version, reason: 'Согласованное завершение учебной сделки' }, key);
    const original = await ok(originalResponse, 201), bytes = await originalResponse.body(), request = original.cancellation_request;
    expect(original.deal).toMatchObject({ status: 'CANCELLATION_PENDING', reserve: deal.reserve, payment_id: null });
    expect(request).toMatchObject({ status: 'PENDING', initiator_organisation_id: lab.customerIdentity.organisation.id, decision: null });
    expect(await balances(lab)).toEqual(before);
    const ownRepeat = await requestCancellation(lab, deal);
    expect(ownRepeat.cancellation_request.id).toBe(request.id);
    await error(await command(lab.customer, decisionPath(deal, request, 'accept'), { deal_version: deal.deal_version }), 409, 'CANCELLATION_SELF_APPROVAL');
    for (const client of [lab.outsider, lab.operator]) await error(await command(client, decisionPath(deal, request, 'accept'), { deal_version: deal.deal_version }), 404, 'RESOURCE_NOT_FOUND');
    const acceptKey = randomUUID(), accepted = await ok(await command(lab.supplier, decisionPath(deal, request, 'accept'), { deal_version: deal.deal_version }, acceptKey));
    expect(accepted).toMatchObject({ cancellation_request: { id: request.id, status: 'ACCEPTED' },
      deal: { status: 'REFUNDED', reserve: { status: 'RELEASED' }, payment_id: null, cancellation_request: null } });
    expect(await balances(lab)).toEqual([{ ...before[0], reserved_kopecks: '0', available_kopecks: before[0].total_kopecks }, before[1]]);
    expect(await ok(await command(lab.supplier, decisionPath(deal, request, 'accept'), { deal_version: deal.deal_version }, acceptKey))).toEqual(accepted);
    const oldReplay = await command(lab.customer, path(deal) + '/cancellation-requests',
      { deal_version: deal.deal_version, reason: 'Согласованное завершение учебной сделки' }, key);
    expect(await oldReplay.body()).toEqual(bytes);
    expect((await read(lab, deal.id)).status).toBe('REFUNDED');
    await error(await command(lab.customer, path(deal) + '/disputes', { deal_version: deal.deal_version, reason: 'Поздний спор' }), 409, 'INVALID_STATE');
  } finally { await lab.close(); }
});

test('two organisations can cancel an unfunded draft without creating money movements or a reserve', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await draft(lab), before = await balances(lab), requested = await requestCancellation(lab, deal);
    const accepted = await ok(await command(lab.supplier, decisionPath(deal, requested.cancellation_request, 'accept'), { deal_version: deal.deal_version }));
    expect(accepted.deal).toMatchObject({ status: 'CANCELLED', reserve: null, payment_id: null, signatures: [], deal_version: deal.deal_version });
    expect(await balances(lab)).toEqual(before);
    await error(await command(lab.customer, path(deal) + '/cancellation-requests', { deal_version: deal.deal_version, reason: 'Снова' }), 409, 'INVALID_STATE');
  } finally { await lab.close(); }
});

test('rejecting cancellation keeps a permanent review reason and a new request cannot restart payment', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await funded(lab), before = await balances(lab), first = await requestCancellation(lab, deal, lab.supplier);
    const rejected = await ok(await command(lab.customer, decisionPath(deal, first.cancellation_request, 'reject'),
      { deal_version: deal.deal_version, reason: 'Нужен разбор результата работ' }));
    expect(rejected.deal).toMatchObject({ status: 'REVIEW_REQUIRED', cancellation_request: null, review_reasons: ['CANCELLATION_REJECTED'], reserve: deal.reserve });
    expect(await balances(lab)).toEqual(before);
    const next = await requestCancellation(lab, deal);
    expect(next.cancellation_request.id).not.toBe(first.cancellation_request.id);
    expect(next.deal).toMatchObject({ status: 'REVIEW_REQUIRED', review_reasons: ['CANCELLATION_REJECTED'] });
    await error(await command(lab.supplier, '/api/v1/documents/' + deal.document.id + '/sign', signature(deal)), 409, 'INVALID_STATE');
    const done = await ok(await command(lab.supplier, decisionPath(deal, next.cancellation_request, 'accept'), { deal_version: deal.deal_version }));
    expect(done.deal.status).toBe('REFUNDED');
  } finally { await lab.close(); }
});

test('a dispute and an active cancellation are independent blockers and releasing one deal leaves the other hold intact', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const a = await funded(lab), b = await funded(lab), before = await balances(lab);
    const dispute = await ok(await command(lab.customer, path(a) + '/disputes', { deal_version: a.deal_version, reason: 'Учебный спор до расчёта' }), 201);
    expect(dispute.deal.status).toBe('DISPUTED');
    expect(await balances(lab)).toEqual(before);
    const bRequest = await requestCancellation(lab, b);
    const bDispute = await ok(await command(lab.supplier, path(b) + '/disputes', { deal_version: b.deal_version, reason: 'Спор не снимает запрос отмены' }), 201);
    expect(bDispute.deal).toMatchObject({ status: 'CANCELLATION_PENDING', dispute: { id: expect.any(String) }, cancellation_request: { id: bRequest.cancellation_request.id } });
    const aRequest = await requestCancellation(lab, a);
    await ok(await command(lab.supplier, decisionPath(a, aRequest.cancellation_request, 'accept'), { deal_version: a.deal_version }));
    expect(await account(lab.customer)).toMatchObject({ total_kopecks: '100000', reserved_kopecks: '30000', available_kopecks: '70000' });
    expect((await read(lab, b.id)).reserve.status).toBe('ACTIVE');
    await ok(await command(lab.supplier, decisionPath(b, bRequest.cancellation_request, 'accept'), { deal_version: b.deal_version }));
    expect(await account(lab.customer)).toMatchObject({ total_kopecks: '100000', reserved_kopecks: '0', available_kopecks: '100000' });
    expect(await account(lab.supplier)).toEqual(before[1]);
  } finally { await lab.close(); }
});

test('automatic EDO delivery pays a full own reserve with zero available and valid new events or replay cannot pay twice', async ({ playwright }) => {
  const lab = await laboratory(playwright, '30000');
  try {
    const deal = await funded(lab);
    expect(await account(lab.customer)).toMatchObject({ total_kopecks: '30000', reserved_kopecks: '30000', available_kopecks: '0' });
    const settled = await signBoth(lab, deal);
    expect(settled).toMatchObject({ payment_id: expect.any(String), reserve: { id: deal.reserve.id, status: 'CONSUMED' } });
    expect(await account(lab.customer)).toMatchObject({ total_kopecks: '0', reserved_kopecks: '0', available_kopecks: '0' });
    expect(await account(lab.supplier)).toMatchObject({ total_kopecks: '30000', reserved_kopecks: '0', available_kopecks: '30000' });
    const before = await balances(lab), job = await sendEvent(lab, deal, 'valid'), processed = await delivered(lab, job.event_id);
    expect(processed).toMatchObject({ http_status: 200, error_code: null, result: { result: 'already_settled', payment_id: settled.payment_id } });
    const replay = await sendEvent(lab, deal, 'replay', job.event_id);
    const replayed = await delivered(lab, replay.event_id);
    expect(replayed.job_id).not.toBe(processed.job_id);
    expect(replayed.result).toEqual(processed.result);
    const conflict = await sendEvent(lab, deal, 'same_id_different_body', job.event_id);
    expect(await delivered(lab, conflict.event_id)).toMatchObject({ http_status: 409, error_code: 'EVENT_ID_CONFLICT' });
    expect(await balances(lab)).toEqual(before);
    expect((await read(lab, deal.id)).payment_id).toBe(settled.payment_id);
    for (const route of ['/disputes', '/cancellation-requests']) await error(await command(lab.customer, path(deal) + route,
      { deal_version: deal.deal_version, reason: 'Расчёт уже завершён' }), 409, 'INVALID_STATE');
  } finally { await lab.close(); }
});

test('operator negative templates validate source and every binding even after settlement and never reverse or repeat money', async ({ playwright }) => {
  const lab = await laboratory(playwright, '30000');
  try {
    const deal = await funded(lab), settled = await signBoth(lab, deal), before = await balances(lab);
    for (const template of ['wrong_amount', 'wrong_parties', 'wrong_document', 'wrong_document_version', 'wrong_document_hash', 'wrong_deal_version', 'single_signature', 'invalid_hmac']) {
      const job = await sendEvent(lab, deal, template), result = await delivered(lab, job.event_id);
      expect(result.http_status).toBe(template === 'invalid_hmac' ? 403 : 422);
      expect(result.error_code).toBe(template === 'invalid_hmac' ? 'EDO_SOURCE_INVALID' : template === 'single_signature' ? 'SIGNATURES_MISSING' : 'EVENT_BINDING_MISMATCH');
    }
    expect(await balances(lab)).toEqual(before);
    expect((await read(lab, deal.id)).payment_id).toBe(settled.payment_id);
  } finally { await lab.close(); }
});

test('one real signature cannot be promoted by operator booleans and browser transport cannot call the internal processor', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await funded(lab);
    await ok(await command(lab.customer, '/api/v1/documents/' + deal.document.id + '/sign', signature(deal)), 201);
    const before = await balances(lab), job = await sendEvent(lab, deal, 'single_signature');
    expect(await delivered(lab, job.event_id)).toMatchObject({ http_status: 422, error_code: 'SIGNATURES_MISSING' });
    const input = { deal_id: deal.id, template: 'valid' };
    for (const client of [lab.customer, lab.supplier, lab.outsider]) {
      await error(await command(client, '/api/v1/demo/edo/events', input), 403, 'FORBIDDEN');
      await error(await client.get('/api/v1/demo/edo/events/' + job.event_id), 403, 'FORBIDDEN');
    }
    await error(await lab.operator.post('/api/v1/demo/edo/events', { data: input, headers: { Origin: E2E_BASE_URL, 'Idempotency-Key': randomUUID() } }), 403, 'CSRF_INVALID');
    const internal = await lab.operator.post('/internal/edo/events', { data: input });
    expect([403, 404]).toContain(internal.status());
    expect((await read(lab, deal.id)).status).toBe('FUNDED');
    expect(await balances(lab)).toEqual(before);
  } finally { await lab.close(); }
});

test('expiry keeps the active cancellation and reserve until the other party explicitly accepts', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await funded(lab, new Date(Date.now() + 4_000).toISOString()), before = await balances(lab);
    const requested = await requestCancellation(lab, deal);
    await expect.poll(async () => (await read(lab, deal.id)).status, { timeout: 10_000, intervals: [100, 250, 500] }).toBe('REVIEW_REQUIRED');
    const current = await read(lab, deal.id);
    expect(current).toMatchObject({ review_reasons: ['DEADLINE_PASSED'], cancellation_request: { id: requested.cancellation_request.id }, reserve: deal.reserve });
    expect(await balances(lab)).toEqual(before);
    const accepted = await ok(await command(lab.supplier, decisionPath(deal, requested.cancellation_request, 'accept'), { deal_version: deal.deal_version }));
    expect(accepted.deal.status).toBe('REFUNDED');
  } finally { await lab.close(); }
});
