import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import type { APIRequestContext, APIResponse, PlaywrightWorkerArgs } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, API_BASE_URL, E2E_BASE_URL } from '../support.ts';

type AuditEvent = {
  id: string; deal_id: string | null; deal_version: number | null; type: string;
  actor_user_id: string | null; actor_organization_id: string | null; actor_source: string | null;
  request_id: string; created_at: string; result: 'SUCCEEDED' | 'REJECTED' | 'RECORDED'; error_code: string | null;
  details: Record<string, string | number>;
};
type Notice = { id: string; created_at: string; read_at: string | null; event: AuditEvent };
type Notices = { items: Notice[]; next_cursor: string | null; unread_count: number };
const path = '/api/v1/notifications';
const detailKeys = new Set(['operation', 'status', 'document_id', 'document_version', 'document_sha256', 'reason', 'decision',
  'cancellation_request_id', 'reserve_id', 'payment_id', 'event_id', 'amount_kopecks', 'currency', 'account_id', 'transaction_id']);

async function ok(response: APIResponse, status = 200) {
  expect(response.status(), await response.text()).toBe(status);
  return response.json();
}
async function error(response: APIResponse, status: number, code: string) {
  expect(response.status(), await response.text()).toBe(status);
  const value = await response.json();
  expect(value).toMatchObject({ error: { code, message: expect.any(String) }, request_id: expect.any(String) });
  return value;
}
async function command(client: APIRequestContext, route: string, data: unknown, key = randomUUID(), method = 'POST') {
  return client.fetch(route, { method, data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': key } });
}
async function page(client: APIRequestContext, params: Record<string, string | number> = {}): Promise<Notices> {
  const response = await client.get(path, { params });
  expect(response.headers()['cache-control']).toContain('no-store');
  expect(response.headers()['x-content-type-options']).toBe('nosniff');
  const result = await ok(response) as Notices;
  expect(Object.keys(result).sort()).toEqual(['items', 'next_cursor', 'unread_count']);
  expect(Number.isSafeInteger(result.unread_count) && result.unread_count >= 0).toBe(true);
  return result;
}
async function all(client: APIRequestContext, unread?: 'true' | 'false') {
  const items: Notice[] = [], seen = new Set<string>();
  let cursor: string | null = null;
  do {
    const result = await page(client, { limit: 100, ...(unread ? { unread } : {}), ...(cursor ? { cursor } : {}) });
    items.push(...result.items); cursor = result.next_cursor;
    if (cursor) { expect(seen.has(cursor)).toBe(false); seen.add(cursor); }
    expect(seen.size, 'bounded synthetic notification fixture').toBeLessThan(10);
  } while (cursor);
  return items;
}
async function delivered(client: APIRequestContext, count: number) {
  await expect.poll(async () => (await all(client)).length, { timeout: 20_000, intervals: [100, 250, 500] }).toBe(count);
  return all(client);
}
async function mark(client: APIRequestContext, id: string, expected?: string) {
  const response = await client.post(path + '/' + id + '/read', {
    headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), ...(expected ? { 'X-Expected-User-ID': expected } : {}) },
  });
  expect(response.status(), await response.text()).toBe(204);
  expect(await response.body()).toHaveLength(0);
}
function safe(notice: Notice) {
  expect(Object.keys(notice).sort()).toEqual(['created_at', 'event', 'id', 'read_at']);
  expect(notice.id).toEqual(expect.any(String));
  expect(notice.created_at).toMatch(/Z$/); expect(Number.isNaN(Date.parse(notice.created_at))).toBe(false);
  if (notice.read_at !== null) {
    expect(notice.read_at).toMatch(/Z$/); expect(Number.isNaN(Date.parse(notice.read_at))).toBe(false);
  }
  const event = notice.event;
  expect(Object.keys(event).sort()).toEqual(['actor_organization_id', 'actor_source', 'actor_user_id', 'created_at', 'deal_id',
    'deal_version', 'details', 'error_code', 'id', 'request_id', 'result', 'type']);
  expect(event.created_at).toMatch(/Z$/);
  expect(Number(event.actor_user_id !== null) + Number(event.actor_source !== null)).toBe(1);
  expect(event.result).toBe('SUCCEEDED'); expect(event.error_code).toBeNull();
  for (const [key, value] of Object.entries(event.details)) {
    expect(detailKeys.has(key), 'unapproved projected field: ' + key).toBe(true);
    expect(['string', 'number']).toContain(typeof value);
  }
}
async function register(client: APIRequestContext) {
  const data = { email: 'notifications-' + randomUUID() + '@demo.poaktu.local', password: 'NotificationsOnly2026!',
    organisation: { name: 'Учебные уведомления ' + randomUUID(), kind: 'COMPANY' } };
  for (let attempt = 0; attempt < 2; attempt++) {
    const response = await client.post('/api/v1/auth/register', { data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) } });
    if (response.status() !== 429 || attempt === 1) return ok(response, 201);
    await error(response, 429, 'RATE_LIMITED');
    const seconds = Number(response.headers()['retry-after']);
    expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true);
    test.setTimeout(test.info().timeout + seconds * 1000);
    await test.step('respect explicit auth Retry-After before one fresh-fixture retry', () => delay(seconds * 1000));
  }
  throw new Error('No registration result');
}
async function laboratory(playwright: PlaywrightWorkerArgs['playwright']) {
  const empty = { cookies: [], origins: [] };
  const customer = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: empty });
  const supplier = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: empty });
  const operator = await apiFor(playwright, 'operator'), outsider = await apiFor(playwright, 'outsider');
  const clients = [customer, supplier, operator, outsider];
  try {
    const customerIdentity = await register(customer), supplierIdentity = await register(supplier);
    return { customer, supplier, operator, outsider, customerIdentity, supplierIdentity, close: () => Promise.all(clients.map(client => client.dispose())) };
  } catch (failure) { await Promise.all(clients.map(client => client.dispose())); throw failure; }
}
type Lab = Awaited<ReturnType<typeof laboratory>>;
async function draft(lab: Lab, key = randomUUID(), overrides = {}) {
  return ok(await postDraft(lab.customer, draftInput({ supplier_party_id: lab.supplierIdentity.organisation.id,
    amount_kopecks: '30000', ...overrides }), key), 201);
}
async function account(client: APIRequestContext) { return (await ok(await client.get('/api/v1/organisations/me'))).account; }
async function balances(lab: Lab) { return Promise.all([account(lab.customer), account(lab.supplier)]); }
async function generated(lab: Lab, deal: any) {
  return (await ok(await command(lab.customer, '/api/v1/deals/' + deal.id + '/documents/generate',
    { expected_deal_version: deal.deal_version, template_id: 'printing-acceptance-v1' }), 201)).deal;
}
function acceptance(deal: any) {
  return { deal_version: deal.deal_version, document_id: deal.document.id,
    document_version: deal.document.document_version, document_sha256: deal.document.sha256 };
}
async function agreed(lab: Lab, deal: any) {
  deal = await generated(lab, deal);
  for (const client of [lab.customer, lab.supplier]) await ok(await command(client, '/api/v1/deals/' + deal.id + '/agree', acceptance(deal)));
  return (await ok(await lab.customer.get('/api/v1/deals/' + deal.id)));
}
async function audit(lab: Lab, id: string) {
  const response = await ok(await lab.customer.get('/api/v1/deals/' + id + '/audit', { params: { limit: 100 } }));
  expect(response.next_cursor).toBeNull();
  return response.items as AuditEvent[];
}

test('notifications are durable safe recipient facts and another participant cannot read the recipient row', async ({ playwright }) => {
  const lab = await laboratory(playwright), anonymous = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    expect(await page(lab.customer)).toEqual({ items: [], next_cursor: null, unread_count: 0 });
    const hidden = 'PRIVATE-DESCRIPTION-' + randomUUID(), before = await balances(lab);
    const deal = await draft(lab, randomUUID(), { description: hidden });
    const own = await delivered(lab.customer, 1), partner = await delivered(lab.supplier, 1);
    safe(own[0]); safe(partner[0]);
    expect(own[0]).toMatchObject({ read_at: null, event: { deal_id: deal.id, deal_version: 1, type: 'DEAL_CREATED',
      actor_user_id: lab.customerIdentity.user.id, actor_organization_id: lab.customerIdentity.organisation.id, actor_source: null } });
    expect(own[0].event).toEqual(partner[0].event); expect(own[0].id).not.toBe(partner[0].id);
    expect(JSON.stringify(own)).not.toContain(hidden);
    expect(JSON.stringify(own)).not.toContain(lab.customerIdentity.user.email);
    expect(await page(lab.customer)).toEqual({ items: own, next_cursor: null, unread_count: 1 });
    expect(await page(lab.operator)).toEqual({ items: [], next_cursor: null, unread_count: 0 });
    for (const client of [lab.supplier, lab.outsider, lab.operator]) {
      const headers = { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) };
      const privateError = await error(await client.post(path + '/' + own[0].id + '/read', { headers }), 404, 'RESOURCE_NOT_FOUND');
      const absentError = await error(await client.post(path + '/missing-' + randomUUID() + '/read', { headers }), 404, 'RESOURCE_NOT_FOUND');
      expect(privateError.error).toEqual(absentError.error);
    }
    await error(await anonymous.get(path), 401, 'AUTH_REQUIRED');
    await error(await anonymous.post(path + '/' + own[0].id + '/read', {
      headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(anonymous) },
    }), 401, 'AUTH_REQUIRED');
    expect(await balances(lab)).toEqual(before);
    expect(await all(lab.customer)).toEqual(own);
  } finally { await lab.close(); await anonymous.dispose(); }
});

test('mark-read is CSRF and identity protected, empty-body only and parallel retries preserve the first timestamp and money', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    await draft(lab); const notices = await delivered(lab.customer, 1), notice = notices[0];
    await delivered(lab.supplier, 1);
    const before = await balances(lab), auditBefore = await audit(lab, notice.event.deal_id!);
    const token = await csrf(lab.customer), base = { Origin: E2E_BASE_URL, 'X-CSRF-Token': token };
    for (const headers of [
      { ...base, Origin: '' }, { ...base, Origin: 'https://untrusted.invalid' }, { ...base, 'X-CSRF-Token': '' },
      { ...base, 'X-CSRF-Token': await csrf(lab.supplier) }, { ...base, 'Sec-Fetch-Site': 'cross-site' },
    ]) await error(await lab.customer.post(path + '/' + notice.id + '/read', { headers }), 403, 'CSRF_INVALID');
    await error(await lab.customer.post(path + '/' + notice.id + '/read', {
      headers: { ...base, 'X-Expected-User-ID': lab.supplierIdentity.user.id },
    }), 409, 'SESSION_CHANGED');
    for (const data of ['', '{}', 'null', ' ', '[]'].slice(1)) {
      await error(await lab.customer.post(path + '/' + notice.id + '/read', { headers: base, data }), 400, 'INVALID_REQUEST');
    }
    await error(await lab.customer.post(path + '/' + notice.id + '/read?unexpected=yes', { headers: base }), 400, 'INVALID_REQUEST');
    expect((await page(lab.customer)).unread_count).toBe(1);
    await mark(lab.customer, notice.id, lab.customerIdentity.user.id);
    const saved = (await all(lab.customer))[0]; safe(saved); expect(saved.read_at).not.toBeNull();
    const responses = await Promise.all(Array.from({ length: 6 }, () => lab.customer.post(path + '/' + notice.id + '/read',
      { headers: { ...base, 'X-Expected-User-ID': lab.customerIdentity.user.id } })));
    for (const response of responses) { expect(response.status()).toBe(204); expect(await response.body()).toHaveLength(0); }
    expect(await page(lab.customer)).toEqual({ items: [saved], next_cursor: null, unread_count: 0 });
    expect(await page(lab.customer, { unread: 'true' })).toEqual({ items: [], next_cursor: null, unread_count: 0 });
    expect(await page(lab.customer, { unread: 'false' })).toEqual({ items: [saved], next_cursor: null, unread_count: 0 });
    expect((await page(lab.supplier)).unread_count).toBe(1);
    expect((await all(lab.supplier))[0].read_at).toBeNull();
    expect(await balances(lab)).toEqual(before);
    expect(await audit(lab, notice.event.deal_id!)).toEqual(auditBefore);
  } finally { await lab.close(); }
});

test('strict filters and signed pagination bind user, unread mode and limit while counts ignore the page filter', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    for (let i = 0; i < 5; i++) await draft(lab);
    const original = await delivered(lab.customer, 5); await delivered(lab.supplier, 5);
    const first = await page(lab.customer, { limit: 2 });
    expect(first.items.map(item => item.id)).toEqual(original.slice(0, 2).map(item => item.id));
    expect(first.unread_count).toBe(5); expect(first.next_cursor).toEqual(expect.any(String));
    await mark(lab.customer, first.items[0].id);
    expect(await page(lab.customer, { unread: 'false', limit: 1 })).toMatchObject({ items: [expect.objectContaining({ id: first.items[0].id })], unread_count: 4 });
    expect((await page(lab.customer, { unread: 'true', limit: 1 })).unread_count).toBe(4);
    for (const suffix of ['?unread=', '?unread=1', '?unread=True', '?unread=all', '?unread=true&unread=false',
      '?limit=0', '?limit=101', '?limit=01', '?limit=NaN', '?limit=', '?limit=1&limit=2', '?cursor=', '?unexpected=value', '?cursor=%ZZ']) {
      await error(await lab.customer.get(path + suffix), 400, 'INVALID_REQUEST');
    }
    const rebound: Record<string, string | number>[] = [
      { limit: 3, cursor: first.next_cursor! }, { limit: 2, cursor: first.next_cursor! + 'x' },
      { limit: 2, cursor: first.next_cursor!, unread: 'true' }, { limit: 2, cursor: first.next_cursor!, unread: 'false' },
    ];
    for (const params of rebound) await error(await lab.customer.get(path, { params }), 400, 'INVALID_REQUEST');
    await error(await lab.supplier.get(path, { params: { limit: 2, cursor: first.next_cursor! } }), 400, 'INVALID_REQUEST');
    await error(await lab.operator.get(path, { params: { limit: 2, cursor: first.next_cursor! } }), 400, 'INVALID_REQUEST');
    await draft(lab); await delivered(lab.customer, 6);
    const ids = first.items.map(item => item.id); let cursor = first.next_cursor;
    while (cursor) {
      const next = await page(lab.customer, { limit: 2, cursor });
      expect(next.unread_count).toBe(5); ids.push(...next.items.map(item => item.id)); cursor = next.next_cursor;
      expect(ids.length).toBeLessThanOrEqual(5);
    }
    expect(ids).toEqual(original.map(item => item.id)); expect(new Set(ids).size).toBe(5);
    expect((await page(lab.customer)).items.map(item => item.id)).toHaveLength(6);
  } finally { await lab.close(); }
});

test('new-key agreement repeats notify once per saved consent and technical PDF or PATCH events do not become messages', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const created = await draft(lab);
    const edited = await ok(await command(lab.customer, '/api/v1/deals/' + created.id,
      { expected_version: 1, description: 'Уточнены учебные условия без уведомления' }, randomUUID(), 'PATCH'));
    const deal = await generated(lab, edited), agreePath = '/api/v1/deals/' + deal.id + '/agree', input = acceptance(deal);
    const firstKey = randomUUID();
    const first = await ok(await command(lab.customer, agreePath, input, firstKey));
    await ok(await command(lab.customer, agreePath, input, firstKey));
    for (let i = 0; i < 3; i++) await ok(await command(lab.customer, agreePath, input));
    await ok(await command(lab.supplier, agreePath, input));
    for (let i = 0; i < 3; i++) await ok(await command(lab.supplier, agreePath, input));
    const reworked = await ok(await command(lab.supplier, '/api/v1/deals/' + deal.id + '/rework',
      { deal_version: deal.deal_version, reason: 'Вернуть макет\nСохранить точный PDF в истории' }));
    expect(reworked.status).toBe('DRAFT');
    const notices = await delivered(lab.customer, 4), partner = await delivered(lab.supplier, 4);
    for (const rows of [notices, partner]) {
      rows.forEach(safe);
      expect(rows.filter(item => item.event.type === 'AGREE_DEAL')).toHaveLength(2);
      expect(new Set(rows.filter(item => item.event.type === 'AGREE_DEAL').map(item => item.event.actor_organization_id)).size).toBe(2);
      expect(rows.filter(item => item.event.type === 'DEAL_REWORKED')).toHaveLength(1);
      expect(rows.some(item => ['PATCH_DEAL', 'GENERATE_DOCUMENT', 'UPLOAD_DOCUMENT', 'EDO_EVENT_READY'].includes(item.event.type))).toBe(false);
      expect(rows.find(item => item.event.type === 'DEAL_REWORKED')?.event.details.reason).toBe('Вернуть макет\nСохранить точный PDF в истории');
    }
    expect((await audit(lab, deal.id)).filter(item => item.type === 'AGREE_DEAL').length).toBeGreaterThan(2);
    expect(await ok(await command(lab.customer, agreePath, input, firstKey))).toEqual(first);
    expect(await all(lab.customer)).toEqual(notices);
  } finally { await lab.close(); }
});

test('current and historical party scope revokes delivered old rows and does not hand the former round to a replacement', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  const replacement = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  try {
    const replacementIdentity = await register(replacement), created = await draft(lab);
    const former = (await delivered(lab.supplier, 1))[0];
    const doc = await generated(lab, created);
    await ok(await command(lab.supplier, '/api/v1/deals/' + created.id + '/agree', acceptance(doc)));
    await delivered(lab.supplier, 2);
    const changed = await ok(await command(lab.customer, '/api/v1/deals/' + created.id,
      { expected_version: doc.deal_version, supplier_party_id: replacementIdentity.organisation.id }, randomUUID(), 'PATCH'));
    expect(await page(lab.supplier)).toEqual({ items: [], next_cursor: null, unread_count: 0 });
    await error(await lab.supplier.post(path + '/' + former.id + '/read', {
      headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(lab.supplier) },
    }), 404, 'RESOURCE_NOT_FOUND');
    expect(await page(replacement)).toEqual({ items: [], next_cursor: null, unread_count: 0 });
    await ok(await command(lab.customer, '/api/v1/deals/' + created.id + '/cancellation-requests',
      { deal_version: changed.deal_version, reason: 'Новый раунд с новым исполнителем' }), 201);
    const current = await delivered(replacement, 1);
    safe(current[0]);
    expect(current[0]).toMatchObject({ event: { type: 'CANCELLATION_REQUESTED', deal_id: created.id, deal_version: changed.deal_version } });
    expect(JSON.stringify(current)).not.toContain(doc.document.id); expect(JSON.stringify(current)).not.toContain(doc.document.sha256);
    await mark(replacement, current[0].id);
    expect((await page(replacement)).unread_count).toBe(0);
    expect(await page(lab.supplier)).toEqual({ items: [], next_cursor: null, unread_count: 0 });
    const own = await delivered(lab.customer, 3);
    expect(own.some(item => item.event.id === former.event.id)).toBe(true);
    expect(own.find(item => item.event.type === 'CANCELLATION_REQUESTED')?.event.deal_version).toBe(changed.deal_version);
  } finally { await lab.close(); await replacement.dispose(); }
});

test('operator credit and debit notify only account owners and read flags cannot change balances or create new audit', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const ownAccount = await account(lab.customer), creditKey = randomUUID();
    const creditInput = { amount_kopecks: '50000', reason: 'Только учебные средства для уведомлений' };
    const credit = await ok(await command(lab.operator, '/api/v1/demo/accounts/' + ownAccount.id + '/credits', creditInput, creditKey), 201);
    const first = await delivered(lab.customer, 1); safe(first[0]);
    expect(first[0]).toMatchObject({ event: { type: 'DEMO_CREDITED', deal_id: null, deal_version: null,
      details: { account_id: ownAccount.id, transaction_id: credit.transaction_id } } });
    const debitKey = randomUUID(), debitInput = { amount_kopecks: '10000', reason: 'Учебное служебное списание' };
    const debit = await ok(await command(lab.operator, '/api/v1/demo/accounts/' + ownAccount.id + '/debits', debitInput, debitKey), 201);
    await delivered(lab.customer, 2);
    await ok(await command(lab.operator, '/api/v1/demo/accounts/' + ownAccount.id + '/credits', creditInput, creditKey), 201);
    await ok(await command(lab.operator, '/api/v1/demo/accounts/' + ownAccount.id + '/debits', debitInput, debitKey), 201);
    await error(await command(lab.operator, '/api/v1/demo/accounts/' + ownAccount.id + '/debits',
      { amount_kopecks: '99999999', reason: 'Сохранённый отказ не является уведомлением' }), 422, 'INSUFFICIENT_AVAILABLE_FUNDS');
    await ok(await command(lab.operator, '/api/v1/demo/accounts/' + ownAccount.id + '/credits',
      { amount_kopecks: '1', reason: 'Последующая реальная запись как барьер доставки' }), 201);
    const notices = await delivered(lab.customer, 3);
    notices.forEach(safe);
    expect(notices.map(item => item.event.type).sort()).toEqual(['DEMO_CREDITED', 'DEMO_CREDITED', 'DEMO_DEBITED'].sort());
    expect(notices.filter(item => item.event.details.transaction_id === credit.transaction_id)).toHaveLength(1);
    expect(notices.filter(item => item.event.details.transaction_id === debit.transaction_id)).toHaveLength(1);
    expect(await page(lab.supplier)).toEqual({ items: [], next_cursor: null, unread_count: 0 });
    expect(await page(lab.operator)).toEqual({ items: [], next_cursor: null, unread_count: 0 });
    const before = await balances(lab);
    expect(before[0]).toMatchObject({ total_kopecks: '40001', reserved_kopecks: '0', available_kopecks: '40001' });
    const auditBefore = await ok(await lab.operator.get('/api/v1/demo/audit', { params: { limit: 100 } }));
    for (const notice of notices) await mark(lab.customer, notice.id);
    expect((await page(lab.customer)).unread_count).toBe(0);
    expect(await balances(lab)).toEqual(before);
    expect(await ok(await lab.operator.get('/api/v1/demo/audit', { params: { limit: 100 } }))).toEqual(auditBefore);
  } finally { await lab.close(); }
});

test('dispute and both closed cancellation decisions deliver their immutable reasons to both parties without replay duplicates', async ({ playwright }) => {
  const lab = await laboratory(playwright);
  try {
    const deal = await agreed(lab, await draft(lab)), base = '/api/v1/deals/' + deal.id, before = await balances(lab);
    const reason = '=Учебная причина, "кавычки"\nБез настоящих расчётов', requestKey = randomUUID();
    const body = { deal_version: deal.deal_version, reason };
    const requested = await ok(await command(lab.customer, base + '/cancellation-requests', body, requestKey), 201);
    await ok(await command(lab.supplier, base + '/disputes', { deal_version: deal.deal_version, reason: 'Нужно обсудить результат' }), 201);
    await ok(await command(lab.supplier, base + '/cancellation-requests/' + requested.cancellation_request.id + '/reject',
      { deal_version: deal.deal_version, reason: 'Отказ стороны от отмены\nДругая причина' }));
    const second = await ok(await command(lab.customer, base + '/cancellation-requests',
      { deal_version: deal.deal_version, reason: 'Стороны согласовали завершение' }), 201);
    const acceptKey = randomUUID(), acceptPath = base + '/cancellation-requests/' + second.cancellation_request.id + '/accept';
    const accepted = await ok(await command(lab.supplier, acceptPath, { deal_version: deal.deal_version }, acceptKey));
    expect(accepted.deal.status).toBe('CANCELLED');
    const notices = await delivered(lab.customer, 8), partner = await delivered(lab.supplier, 8);
    for (const rows of [notices, partner]) {
      rows.forEach(safe);
      expect(rows.filter(item => item.event.type === 'DISPUTE_OPENED')).toHaveLength(1);
      expect(rows.filter(item => item.event.type === 'CANCELLATION_REQUESTED')).toHaveLength(2);
      expect(rows.find(item => item.event.type === 'CANCELLATION_REQUESTED' && item.event.details.reason === reason)).toBeDefined();
      expect(rows.find(item => item.event.type === 'CANCELLATION_REJECTED')?.event).toMatchObject({
        result: 'SUCCEEDED', details: { decision: 'REJECTED', reason: 'Отказ стороны от отмены\nДругая причина' } });
      expect(rows.find(item => item.event.type === 'CANCELLATION_ACCEPTED')?.event.details.cancellation_request_id).toBe(second.cancellation_request.id);
    }
    expect(await ok(await command(lab.customer, base + '/cancellation-requests', body, requestKey), 201)).toEqual(requested);
    expect(await ok(await command(lab.supplier, acceptPath, { deal_version: deal.deal_version }, acceptKey))).toEqual(accepted);
    expect(await all(lab.customer)).toEqual(notices);
    expect(await balances(lab)).toEqual(before);
  } finally { await lab.close(); }
});

test('a real settlement delivers two signature facts and one source payment to each party without paying or notifying twice', async ({ playwright }) => {
  test.setTimeout(60_000);
  const lab = await laboratory(playwright);
  try {
    const ownAccount = await account(lab.customer);
    await ok(await command(lab.operator, '/api/v1/demo/accounts/' + ownAccount.id + '/credits',
      { amount_kopecks: '30000', reason: 'Изолированная проверка уведомления о расчёте' }), 201);
    const deal = await agreed(lab, await draft(lab)), base = '/api/v1/deals/' + deal.id;
    const funded = await ok(await command(lab.customer, base + '/reserve', { deal_version: deal.deal_version }), 201);
    const input = { deal_version: deal.deal_version, document_version: deal.document.document_version, document_sha256: deal.document.sha256 };
    const signPath = '/api/v1/documents/' + deal.document.id + '/sign', signKey = randomUUID();
    const first = await ok(await command(lab.customer, signPath, input, signKey), 201);
    await ok(await command(lab.supplier, signPath, input), 201);
    await expect.poll(async () => (await ok(await lab.customer.get(base))).status,
      { timeout: 20_000, intervals: [100, 250, 500] }).toBe('SETTLED');
    const settled = await ok(await lab.customer.get(base)), own = await delivered(lab.customer, 8), partner = await delivered(lab.supplier, 7);
    for (const rows of [own, partner]) {
      rows.forEach(safe);
      expect(rows.filter(item => item.event.type === 'DOCUMENT_SIGNED')).toHaveLength(2);
      expect(rows.filter(item => item.event.type === 'DEAL_RESERVED')).toHaveLength(1);
      expect(rows.find(item => item.event.type === 'DEAL_RESERVED')?.event.details.reserve_id).toBe(funded.reserve.id);
      expect(rows.filter(item => item.event.type === 'DEAL_SETTLED')).toHaveLength(1);
      expect(rows.find(item => item.event.type === 'DEAL_SETTLED')?.event).toMatchObject({ actor_user_id: null,
        actor_organization_id: null, actor_source: 'edo-emulator', deal_version: deal.deal_version,
        details: { payment_id: settled.payment_id, amount_kopecks: '30000' } });
      expect(rows.some(item => ['EDO_EVENT_READY', 'EDO_EVENT_PROCESSED', 'EDO_DELIVERY_REQUESTED'].includes(item.event.type))).toBe(false);
    }
    expect(await ok(await command(lab.customer, signPath, input, signKey), 201)).toEqual(first);
    expect((await ok(await command(lab.customer, signPath, input), 201)).signature.id).toBe(first.signature.id);
    const before = await balances(lab);
    expect(before[0]).toMatchObject({ total_kopecks: '0', reserved_kopecks: '0', available_kopecks: '0' });
    expect(before[1]).toMatchObject({ total_kopecks: '30000', reserved_kopecks: '0', available_kopecks: '30000' });
    const job = await ok(await command(lab.operator, '/api/v1/demo/edo/events', { deal_id: deal.id, template: 'valid' }), 202);
    await expect.poll(async () => (await ok(await lab.operator.get('/api/v1/demo/edo/events/' + job.event_id))).status,
      { timeout: 20_000, intervals: [100, 250, 500] }).toBe('DELIVERED');
    expect((await ok(await lab.operator.get('/api/v1/demo/edo/events/' + job.event_id))).result.result).toBe('already_settled');
    expect(await all(lab.customer)).toEqual(own); expect(await all(lab.supplier)).toEqual(partner);
    const payment = own.find(item => item.event.type === 'DEAL_SETTLED')!;
    await mark(lab.customer, payment.id);
    expect((await page(lab.customer)).unread_count).toBe(7);
    expect((await page(lab.supplier)).unread_count).toBe(7);
    expect(await balances(lab)).toEqual(before);
  } finally { await lab.close(); }
});
