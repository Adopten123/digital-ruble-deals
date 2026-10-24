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
type AuditPage = { items: AuditEvent[]; next_cursor: string | null };
const columns = ['id', 'deal_id', 'deal_version', 'type', 'actor_user_id', 'actor_organization_id', 'actor_source',
  'request_id', 'created_at', 'result', 'error_code', 'operation', 'status', 'document_id', 'document_version',
  'document_sha256', 'reason', 'decision', 'cancellation_request_id', 'reserve_id', 'payment_id', 'event_id',
  'amount_kopecks', 'currency', 'account_id', 'transaction_id'];
const eventKeys = [...columns.slice(0, 11), 'details'].sort();
const detailKeys = new Set(columns.slice(11));
const route = (id: string) => '/api/v1/deals/' + id + '/audit';

async function ok(response: APIResponse, status = 200) {
  expect(response.status(), await response.text()).toBe(status);
  return response.json();
}
async function error(response: APIResponse, status: number, code: string) {
  expect(response.status(), await response.text()).toBe(status);
  const body = await response.json();
  expect(body).toMatchObject({ error: { code, message: expect.any(String) }, request_id: expect.any(String) });
  return body;
}
async function command(client: APIRequestContext, path: string, data: unknown, key = randomUUID(), method = 'POST') {
  return client.fetch(path, { method, data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': key } });
}
async function page(client: APIRequestContext, path: string, params: Record<string, string | number> = {}): Promise<AuditPage> {
  const response = await client.get(path, { params });
  expect(response.headers()['cache-control']).toContain('no-store');
  return ok(response);
}
async function all(client: APIRequestContext, id: string, params: Record<string, string | number> = {}) {
  const result: AuditEvent[] = [], cursors = new Set<string>();
  let cursor: string | null = null;
  do {
    const response = await page(client, route(id), { ...params, limit: 100, ...(cursor ? { cursor } : {}) });
    result.push(...response.items);
    cursor = response.next_cursor;
    if (cursor) { expect(cursors.has(cursor), 'cursor must advance').toBe(false); cursors.add(cursor); }
    expect(cursors.size, 'bounded synthetic fixture').toBeLessThan(10);
  } while (cursor);
  return result;
}
function safeEvent(event: AuditEvent) {
  expect(Object.keys(event).sort()).toEqual(eventKeys);
  expect(event).toMatchObject({ id: expect.any(String), type: expect.stringMatching(/^[A-Z][A-Z0-9_]{0,63}$/),
    request_id: expect.any(String), created_at: expect.stringMatching(/Z$/), details: expect.any(Object) });
  expect(Number.isNaN(Date.parse(event.created_at))).toBe(false);
  expect(['SUCCEEDED', 'REJECTED', 'RECORDED']).toContain(event.result);
  expect(Number(event.actor_user_id !== null) + Number(event.actor_source !== null), 'one actor kind').toBe(1);
  for (const [key, value] of Object.entries(event.details)) {
    expect(detailKeys.has(key), 'unapproved detail field: ' + key).toBe(true);
    expect(['string', 'number']).toContain(typeof value);
  }
}
function csvRows(source: string): string[][] {
  expect(source.charCodeAt(0), 'UTF-8 CSV has a BOM').toBe(0xfeff);
  const rows: string[][] = [], row: string[] = [];
  let field = '', quoted = false;
  for (let i = 1; i < source.length; i++) {
    const value = source[i];
    if (value === '"') {
      if (quoted && source[i + 1] === '"') { field += '"'; i++; }
      else { quoted = !quoted; }
    } else if (!quoted && value === ',') { row.push(field); field = ''; }
    else if (!quoted && value === '\r' && source[i + 1] === '\n') {
      row.push(field); rows.push([...row]); row.length = 0; field = ''; i++;
    } else { field += value; }
  }
  expect(quoted, 'CSV quote is closed').toBe(false);
  expect(field, 'CSV ends with CRLF').toBe('');
  expect(row).toEqual([]);
  for (const record of rows) expect(record.length).toBe(columns.length);
  return rows;
}
function csvSafe(value: unknown) {
  const text = value == null ? '' : String(value);
  return /^[\s\u0000-\u001f\u007f]*[=+\-@]/u.test(text) ? "'" + text : text;
}
function expectedRow(event: AuditEvent) {
  const record: Record<string, unknown> = { ...event, ...event.details };
  return columns.map(column => csvSafe(record[column]));
}
async function exportsMatch(client: APIRequestContext, id: string, params: Record<string, string> = {}) {
  const expected = await all(client, id, params);
  const json = await client.get(route(id) + '/export', { params: { format: 'json', ...params } });
  expect(json.status(), await json.text()).toBe(200);
  expect(json.headers()['content-type']).toContain('application/json');
  expect(json.headers()['content-disposition']).toMatch(/^attachment;/);
  expect(json.headers()['cache-control']).toContain('no-store');
  expect(json.headers()['x-content-type-options']).toBe('nosniff');
  expect(await json.json()).toEqual({ items: expected });
  const csv = await client.get(route(id) + '/export', { params: { format: 'csv', ...params } });
  expect(csv.status(), await csv.text()).toBe(200);
  expect(csv.headers()['content-type']).toContain('text/csv');
  expect(csv.headers()['content-disposition']).toMatch(/^attachment;/);
  expect(csv.headers()['cache-control']).toContain('no-store');
  expect(csv.headers()['x-content-type-options']).toBe('nosniff');
  const rows = csvRows((await csv.body()).toString('utf8'));
  expect(rows.shift()).toEqual(columns);
  expect(rows).toEqual(expected.map(expectedRow));
  return expected;
}

test('audit is a safe participant projection and operator uses a separate authorised journal', async ({ playwright, actors }) => {
  const customer = await apiFor(playwright, 'customer'), supplier = await apiFor(playwright, 'supplier');
  const outsider = await apiFor(playwright, 'outsider'), operator = await apiFor(playwright, 'operator');
  const anonymous = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    const hiddenText = 'PRIVATE-DESCRIPTION-' + randomUUID();
    const draft = await ok(await postDraft(customer, draftInput({ description: hiddenText })), 201);
    const own = await page(customer, route(draft.id));
    expect(own.next_cursor).toBeNull();
    expect(own.items).toHaveLength(1);
    const event = own.items[0]; safeEvent(event);
    expect(event).toMatchObject({ deal_id: draft.id, deal_version: 1, type: 'DEAL_CREATED', result: 'SUCCEEDED',
      actor_user_id: actors.customer.user.id, actor_organization_id: actors.customer.organisation!.id,
      actor_source: null, error_code: null });
    expect(JSON.stringify(own)).not.toContain(hiddenText);
    expect(await page(supplier, route(draft.id))).toEqual(own);
    expect(await exportsMatch(customer, draft.id)).toEqual(own.items);
    for (const client of [outsider, operator]) {
      for (const suffix of ['', '/export?format=json', '/export?format=csv']) {
        const privateError = await error(await client.get(route(draft.id) + suffix), 404, 'RESOURCE_NOT_FOUND');
        const absentError = await error(await client.get(route('missing-' + randomUUID()) + suffix), 404, 'RESOURCE_NOT_FOUND');
        expect(privateError.error).toEqual(absentError.error);
      }
    }
    for (const suffix of ['', '/export?format=json', '/export?format=csv']) {
      await error(await anonymous.get(route(draft.id) + suffix), 401, 'AUTH_REQUIRED');
    }
    for (const client of [customer, supplier, outsider]) await error(await client.get('/api/v1/demo/audit'), 403, 'FORBIDDEN');
    await error(await anonymous.get('/api/v1/demo/audit'), 401, 'AUTH_REQUIRED');
    const journal = await page(operator, '/api/v1/demo/audit', { deal_id: draft.id });
    expect(journal.items).toEqual(own.items);
    expect(await page(operator, '/api/v1/demo/audit', { deal_id: 'missing-' + randomUUID() })).toEqual({ items: [], next_cursor: null });
  } finally { await Promise.all([customer, supplier, outsider, operator, anonymous].map(client => client.dispose())); }
});

test('supplier replacement revokes the former party and hides the former round from the new party and export', async ({ playwright, actors }) => {
  const customer = await apiFor(playwright, 'customer'), former = await apiFor(playwright, 'supplier');
  const current = await apiFor(playwright, 'outsider'), operator = await apiFor(playwright, 'operator');
  try {
    const draft = await ok(await postDraft(customer), 201);
    const generated = await ok(await command(customer, '/api/v1/deals/' + draft.id + '/documents/generate',
      { expected_deal_version: 1, template_id: 'printing-acceptance-v1' }), 201);
    const prior = await all(former, draft.id);
    expect(prior.length).toBeGreaterThan(1);
    const changed = await ok(await command(customer, '/api/v1/deals/' + draft.id,
      { expected_version: generated.deal.deal_version, supplier_party_id: actors.outsider.organisation!.id }, randomUUID(), 'PATCH'));
    for (const suffix of ['', '/export?format=json', '/export?format=csv']) await error(await former.get(route(draft.id) + suffix), 404, 'RESOURCE_NOT_FOUND');
    const visible = await exportsMatch(current, draft.id);
    expect(visible.length).toBeGreaterThan(0);
    expect(visible.every(event => event.deal_version === changed.deal_version)).toBe(true);
    const text = JSON.stringify(visible);
    expect(text).not.toContain(generated.document.id);
    expect(text).not.toContain(generated.document.sha256);
    for (const old of prior) expect(visible.map(event => event.id)).not.toContain(old.id);
    const full = await all(customer, draft.id);
    expect(full.map(event => event.id)).toEqual(expect.arrayContaining(prior.map(event => event.id)));
    expect(full.length).toBe(prior.length + visible.length);
    expect((await page(operator, '/api/v1/demo/audit', { deal_id: draft.id, limit: 100 })).items).toEqual(full);
  } finally { await Promise.all([customer, former, current, operator].map(client => client.dispose())); }
});

test('cancellation decisions retain multiline Cyrillic text, escape CSV formula cells and replay creates no extra history', async ({ playwright }) => {
  const customer = await apiFor(playwright, 'customer'), supplier = await apiFor(playwright, 'supplier');
  try {
    const createKey = randomUUID(), input = draftInput(), createResponse = await postDraft(customer, input, createKey);
    const draft = await ok(createResponse, 201), base = '/api/v1/deals/' + draft.id;
    const firstReason = '=СУММ(1;2), "учебный акт"\nВторая строка\tи табуляция\rТретья строка';
    const rejectReason = '+Разногласие, "сумма"\r\nСледующая строка';
    const secondReason = '-Причина запроса; без реального расчёта';
    const secondReject = '@Уточнение условий, "кириллица"';
    const requestBody = { deal_version: 1, reason: firstReason }, requestKey = randomUUID();
    const requestResponse = await command(customer, base + '/cancellation-requests', requestBody, requestKey);
    const requested = await ok(requestResponse, 201), requestBytes = await requestResponse.body();
    const afterRequest = await all(customer, draft.id);
    expect(afterRequest).toHaveLength(2);
    expect(await (await command(customer, base + '/cancellation-requests', requestBody, requestKey)).body()).toEqual(requestBytes);
    expect((await ok(await command(customer, base + '/cancellation-requests', requestBody), 201)).cancellation_request.id).toBe(requested.cancellation_request.id);
    expect(await all(customer, draft.id)).toEqual(afterRequest);
    const rejectPath = base + '/cancellation-requests/' + requested.cancellation_request.id + '/reject';
    const rejectKey = randomUUID(), rejectBody = { deal_version: 1, reason: rejectReason };
    await ok(await command(supplier, rejectPath, rejectBody, rejectKey));
    const next = await ok(await command(supplier, base + '/cancellation-requests', { deal_version: 1, reason: secondReason }), 201);
    await ok(await command(customer, base + '/cancellation-requests/' + next.cancellation_request.id + '/reject',
      { deal_version: 1, reason: secondReject }));
    const complete = await exportsMatch(customer, draft.id);
    expect(complete).toHaveLength(5);
    for (const event of complete) safeEvent(event);
    const requestedEvents = complete.filter(event => event.type === 'CANCELLATION_REQUESTED');
    const rejectedEvents = complete.filter(event => event.type === 'CANCELLATION_REJECTED');
    expect(requestedEvents.map(event => event.details.reason).sort()).toEqual([firstReason, secondReason].sort());
    expect(rejectedEvents.map(event => event.details.reason).sort()).toEqual([rejectReason, secondReject].sort());
    for (const event of rejectedEvents) expect(event).toMatchObject({ result: 'SUCCEEDED', error_code: null, details: { decision: 'REJECTED' } });
    expect(await exportsMatch(customer, draft.id, { type: 'CANCELLATION_REQUESTED' })).toEqual(requestedEvents);
    expect(await exportsMatch(customer, draft.id, { type: 'CANCELLATION_REJECTED',
      from: rejectedEvents[0].created_at, to: rejectedEvents[0].created_at })).toEqual([rejectedEvents[0]]);
    expect(await exportsMatch(customer, draft.id, { to: '2000-01-01T00:00:00Z' })).toEqual([]);
    expect(await exportsMatch(customer, draft.id, { type: 'UNKNOWN_BUT_LEGAL' })).toEqual([]);
    expect(await ok(await postDraft(customer, input, createKey), 201)).toEqual(draft);
    await ok(await command(supplier, rejectPath, rejectBody, rejectKey));
    expect(await all(customer, draft.id)).toEqual(complete);
    const invalidKey = randomUUID(), invalidBody = { deal_version: 999, reason: 'Неактуальная версия' };
    await error(await command(customer, base + '/cancellation-requests', invalidBody, invalidKey), 409, 'VERSION_CONFLICT');
    const refused = await all(customer, draft.id);
    expect(refused).toHaveLength(6);
    expect(refused[0]).toMatchObject({ type: 'REQUEST_CANCELLATION_REJECTED', result: 'REJECTED', error_code: 'VERSION_CONFLICT' });
    await error(await command(customer, base + '/cancellation-requests', invalidBody, invalidKey), 409, 'VERSION_CONFLICT');
    expect(await all(customer, draft.id)).toEqual(refused);
  } finally { await Promise.all([customer.dispose(), supplier.dispose()]); }
});

test('signed keyset pagination excludes later events and rejects another identity, deal or filter without duplicates', async ({ playwright }) => {
  const customer = await apiFor(playwright, 'customer'), supplier = await apiFor(playwright, 'supplier'), operator = await apiFor(playwright, 'operator');
  try {
    let draft = await ok(await postDraft(customer), 201);
    for (let i = 0; i < 5; i++) draft = await ok(await command(customer, '/api/v1/deals/' + draft.id,
      { expected_version: draft.deal_version, description: 'Новая редакция ' + i }, randomUUID(), 'PATCH'));
    const before = await all(customer, draft.id), first = await page(customer, route(draft.id), { limit: 2 });
    expect(before).toHaveLength(6);
    expect(first.items).toEqual(before.slice(0, 2));
    expect(first.next_cursor).toEqual(expect.any(String));
    await ok(await command(customer, '/api/v1/deals/' + draft.id,
      { expected_version: draft.deal_version, description: 'После первой страницы' }, randomUUID(), 'PATCH'));
    const combined = [...first.items], seen = new Set([first.next_cursor]);
    let cursor = first.next_cursor;
    while (cursor) {
      const next = await page(customer, route(draft.id), { limit: 2, cursor });
      combined.push(...next.items);
      cursor = next.next_cursor;
      if (cursor) { expect(seen.has(cursor)).toBe(false); seen.add(cursor); }
      expect(seen.size).toBeLessThan(5);
    }
    expect(combined).toEqual(before);
    expect(new Set(combined.map(event => event.id)).size).toBe(combined.length);
    expect(await all(customer, draft.id)).toHaveLength(7);
    const rebound: Record<string, string | number>[] = [
      { limit: 2, cursor: first.next_cursor! + 'x' },
      { limit: 3, cursor: first.next_cursor! },
      { limit: 2, cursor: first.next_cursor!, type: 'DEAL_CREATED' },
      { limit: 2, cursor: first.next_cursor!, from: '2020-01-01T00:00:00Z' },
    ];
    for (const params of rebound) await error(await customer.get(route(draft.id), { params }), 400, 'INVALID_REQUEST');
    await error(await supplier.get(route(draft.id), { params: { limit: 2, cursor: first.next_cursor! } }), 400, 'INVALID_REQUEST');
    const other = await ok(await postDraft(customer), 201);
    await error(await customer.get(route(other.id), { params: { limit: 2, cursor: first.next_cursor! } }), 400, 'INVALID_REQUEST');
    await error(await operator.get('/api/v1/demo/audit', { params: { deal_id: draft.id, limit: 2, cursor: first.next_cursor! } }), 400, 'INVALID_REQUEST');
    const operatorFirst = await page(operator, '/api/v1/demo/audit', { deal_id: draft.id, limit: 1 });
    expect(operatorFirst.next_cursor).toEqual(expect.any(String));
    await error(await operator.get('/api/v1/demo/audit', { params: { deal_id: other.id, limit: 1, cursor: operatorFirst.next_cursor! } }), 400, 'INVALID_REQUEST');
  } finally { await Promise.all([customer, supplier, operator].map(client => client.dispose())); }
});

test('history filters and export formats are strict and operator-only unbound refusals do not leak to a deal', async ({ playwright }) => {
  const customer = await apiFor(playwright, 'customer'), operator = await apiFor(playwright, 'operator');
  try {
    const draft = await ok(await postDraft(customer), 201);
    for (const suffix of [
      '?limit=0', '?limit=101', '?limit=01', '?limit=1e1', '?limit=', '?cursor=', '?type=', '?type=lowercase',
      '?type=DEAL_CREATED&type=DEAL_CREATED', '?from=', '?from=tomorrow', '?to=tomorrow',
      '?from=2030-01-01T00:00:00Z&to=2020-01-01T00:00:00Z', '?limit=1&limit=2',
      '?unexpected=value', '?from=%ZZ', '?deal_id=other',
    ]) await error(await customer.get(route(draft.id) + suffix), 400, 'INVALID_REQUEST');
    for (const suffix of [
      '', '?format=', '?format=xml', '?format=JSON', '?format=json&format=csv', '?format=json&limit=1',
      '?format=csv&cursor=anything', '?format=csv&unexpected=value', '?format=csv&type=lowercase',
      '?format=json&from=2030-01-01T00:00:00Z&to=2020-01-01T00:00:00Z',
    ]) await error(await customer.get(route(draft.id) + '/export' + suffix), 400, 'INVALID_REQUEST');
    for (const suffix of ['?deal_id=', '?deal_id=x&deal_id=y', '?format=json', '?limit=101', '?type=lowercase']) {
      await error(await operator.get('/api/v1/demo/audit' + suffix), 400, 'INVALID_REQUEST');
    }
    const before = await all(customer, draft.id);
    const rejectedResponse = await postDraft(customer, draftInput({ deadline_at: '2020-01-01T00:00:00Z' }));
    const rejected = await error(rejectedResponse, 422, 'DEADLINE_PASSED');
    const global = await page(operator, '/api/v1/demo/audit', { type: 'DEAL_CREATE_REJECTED', limit: 100 });
    const rejection = global.items.find(event => event.request_id === rejected.request_id);
    expect(rejection).toBeDefined();
    safeEvent(rejection!);
    expect(rejection).toMatchObject({ deal_id: null, deal_version: null, result: 'REJECTED', error_code: 'DEADLINE_PASSED' });
    expect(await all(customer, draft.id)).toEqual(before);
  } finally { await Promise.all([customer.dispose(), operator.dispose()]); }
});

async function register(client: APIRequestContext) {
  const data = { email: 'audit-' + randomUUID() + '@demo.poaktu.local', password: 'AuditOnlyPassword2026!',
    organisation: { name: 'История учебного расчёта ' + randomUUID(), kind: 'COMPANY' } };
  for (let attempt = 0; attempt < 2; attempt++) {
    const response = await client.post('/api/v1/auth/register', { data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) } });
    if (response.status() !== 429 || attempt === 1) return ok(response, 201);
    await error(response, 429, 'RATE_LIMITED');
    const seconds = Number(response.headers()['retry-after']);
    expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true);
    test.setTimeout(test.info().timeout + seconds * 1000);
    await test.step('respect auth Retry-After before one fresh-fixture retry', () => delay(seconds * 1000));
  }
  throw new Error('No registration result');
}
async function laboratory(playwright: PlaywrightWorkerArgs['playwright']) {
  const customer = await playwright.request.newContext({ baseURL: API_BASE_URL });
  const supplier = await playwright.request.newContext({ baseURL: API_BASE_URL });
  const operator = await apiFor(playwright, 'operator');
  const clients = [customer, supplier, operator];
  try {
    const customerIdentity = await register(customer), supplierIdentity = await register(supplier);
    return { customer, supplier, operator, customerIdentity, supplierIdentity, close: () => Promise.all(clients.map(client => client.dispose())) };
  } catch (failure) { await Promise.all(clients.map(client => client.dispose())); throw failure; }
}
test('a real isolated settlement has one payment fact and source actor, exact versions and immutable signature replay history', async ({ playwright }) => {
  test.setTimeout(60_000);
  const lab = await laboratory(playwright);
  try {
    const account = (await ok(await lab.customer.get('/api/v1/organisations/me'))).account;
    const credit = await ok(await command(lab.operator, '/api/v1/demo/accounts/' + account.id + '/credits',
      { amount_kopecks: '30000', reason: 'Изолированная проверка истории тестового расчёта' }), 201);
    const creditHistory = await page(lab.operator, '/api/v1/demo/audit', { type: 'DEMO_CREDITED', limit: 100 });
    const creditEvent = creditHistory.items.find(event => event.details.transaction_id === credit.transaction_id);
    expect(creditEvent).toBeDefined();
    safeEvent(creditEvent!);
    expect(creditEvent).toMatchObject({ deal_id: null, deal_version: null, result: 'SUCCEEDED',
      actor_user_id: expect.any(String),
      details: { account_id: account.id, transaction_id: credit.transaction_id } });
    expect(Object.keys(creditEvent!.details).sort()).toEqual(['account_id', 'transaction_id']);
    const draft = await ok(await postDraft(lab.customer, draftInput({ supplier_party_id: lab.supplierIdentity.organisation.id, amount_kopecks: '30000' })), 201);
    const generated = await ok(await command(lab.customer, '/api/v1/deals/' + draft.id + '/documents/generate',
      { expected_deal_version: 1, template_id: 'printing-acceptance-v1' }), 201);
    const deal = generated.deal, doc = generated.document;
    const binding = { deal_version: deal.deal_version, document_id: doc.id, document_version: doc.document_version, document_sha256: doc.sha256 };
    for (const client of [lab.customer, lab.supplier]) await ok(await command(client, '/api/v1/deals/' + deal.id + '/agree', binding));
    const reserve = await ok(await command(lab.customer, '/api/v1/deals/' + deal.id + '/reserve', { deal_version: deal.deal_version }), 201);
    const sign = { deal_version: deal.deal_version, document_version: doc.document_version, document_sha256: doc.sha256 };
    const signPath = '/api/v1/documents/' + doc.id + '/sign', signKey = randomUUID();
    const firstResponse = await command(lab.customer, signPath, sign, signKey);
    const first = await ok(firstResponse, 201);
    await ok(await command(lab.supplier, signPath, sign), 201);
    await expect.poll(async () => (await ok(await lab.customer.get('/api/v1/deals/' + deal.id))).status,
      { timeout: 20_000, intervals: [100, 250, 500] }).toBe('SETTLED');
    const settled = await ok(await lab.customer.get('/api/v1/deals/' + deal.id));
    const history = await exportsMatch(lab.customer, deal.id);
    expect(await all(lab.supplier, deal.id)).toEqual(history);
    history.forEach(safeEvent);
    const payments = history.filter(event => event.type === 'DEAL_SETTLED');
    expect(payments).toHaveLength(1);
    expect(payments[0]).toMatchObject({ deal_id: deal.id, deal_version: deal.deal_version, result: 'SUCCEEDED',
      actor_user_id: null, actor_organization_id: null, actor_source: 'edo-emulator', error_code: null,
      details: { payment_id: settled.payment_id, amount_kopecks: '30000' } });
    expect(history.filter(event => event.type === 'DEAL_RESERVED')).toHaveLength(1);
    expect(history.find(event => event.type === 'DEAL_RESERVED')?.details.reserve_id).toBe(reserve.reserve.id);
    const signatures = history.filter(event => event.type === 'DOCUMENT_SIGNED');
    expect(signatures).toHaveLength(2);
    expect(new Set(signatures.map(event => event.actor_organization_id)).size).toBe(2);
    for (const event of signatures) expect(event).toMatchObject({ deal_version: deal.deal_version,
      details: { document_id: doc.id, document_version: doc.document_version, document_sha256: doc.sha256 } });
    expect(await ok(await command(lab.customer, signPath, sign, signKey), 201)).toEqual(first);
    expect((await ok(await command(lab.customer, signPath, sign), 201)).signature.id).toBe(first.signature.id);
    expect(await all(lab.customer, deal.id)).toEqual(history);
    const before = await Promise.all([lab.customer, lab.supplier].map(async client => (await ok(await client.get('/api/v1/organisations/me'))).account));
    expect(before[0]).toMatchObject({ total_kopecks: '0', reserved_kopecks: '0', available_kopecks: '0' });
    expect(before[1]).toMatchObject({ total_kopecks: '30000', reserved_kopecks: '0', available_kopecks: '30000' });
    const job = await ok(await command(lab.operator, '/api/v1/demo/edo/events', { deal_id: deal.id, template: 'valid' }), 202);
    await expect.poll(async () => (await ok(await lab.operator.get('/api/v1/demo/edo/events/' + job.event_id))).status,
      { timeout: 20_000, intervals: [100, 250, 500] }).toBe('DELIVERED');
    const after = await all(lab.customer, deal.id);
    expect(after.filter(event => event.type === 'DEAL_SETTLED')).toEqual(payments);
    const processed = after.filter(event => event.type === 'EDO_EVENT_PROCESSED');
    expect(processed.length).toBeGreaterThanOrEqual(2);
    expect(processed.some(event => event.details.event_id === job.event_id && event.details.status === 'ALREADY_SETTLED')).toBe(true);
    expect(await Promise.all([lab.customer, lab.supplier].map(async client => (await ok(await client.get('/api/v1/organisations/me'))).account))).toEqual(before);
  } finally { await lab.close(); }
});
