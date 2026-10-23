import assert from 'node:assert/strict';
import test from 'node:test';
import { ApiClient, ApiError } from '../src/lib/api.ts';
import { createDealInput, dateBoundary, pendingStorageKey, readPendingDeal, removePendingDeal } from '../src/lib/deals.ts';

const fields = { supplierId: 'supplier-1', title: '  Тестовая печать  ', description: '  Макет и результат  ', amount: '300000,00', deadline: '2027-01-05T12:00:00Z' };
const now = Date.parse('2026-10-20T10:00:00Z');

test('create input trims conditions, converts money exactly and normalizes deadline to UTC', () => {
  assert.deepEqual(createDealInput({ ...fields, deadline: '2027-01-05T15:00:00+03:00' }, now), {
    supplier_party_id: 'supplier-1', title: 'Тестовая печать', description: 'Макет и результат', amount_kopecks: '30000000', currency: 'RUB', deadline_at: '2027-01-05T12:00:00.000Z',
  });
  assert.doesNotThrow(() => createDealInput({ ...fields, title: '😀'.repeat(200) }, now));
  assert.throws(() => createDealInput({ ...fields, title: '😀'.repeat(201) }, now));
  for (const changed of [{ supplierId: '' }, { description: ' ' }, { deadline: 'bad' }, { deadline: '+010000-01-01T00:00:00Z' }, { deadline: '2026-10-20T10:00:00Z' }, { description: 'я'.repeat(5001) }]) {
    assert.throws(() => createDealInput({ ...fields, ...changed }, now));
  }
  assert.throws(() => createDealInput({ ...fields, description: '😀'.repeat(4100) }, now), /Сократите/);
});

test('lost create response retains exact body and command key through manual retry and reload', async () => {
  const input = createDealInput(fields, now);
  const serialized = JSON.stringify({ key: 'abec4124-e107-4e07-88c8-0322c3b15a9b', input, supplier: { id: 'supplier-1', name: 'Учебная типография', kind: 'COMPANY', is_demo: true } });
  const attempt = readPendingDeal(serialized)!;
  const requests: RequestInit[] = [];
  const client = new ApiClient(async (url, options) => {
    if (String(url).endsWith('/csrf')) return Response.json({ csrf_token: 'csrf-1' });
    requests.push(options!);
    if (requests.length === 1) throw new TypeError('connection lost after commit');
    return Response.json({ id: 'saved-deal' }, { status: 201 });
  });
  await assert.rejects(client.createDeal(attempt.input, attempt.key), TypeError);
  assert.equal(requests.length, 1, 'mutations must never retry automatically');
  const restored = readPendingDeal(serialized)!;
  await client.createDeal(restored.input, restored.key);
  assert.equal(requests.length, 2);
  assert.equal(requests[0].body, requests[1].body);
  assert.equal((requests[0].headers as Record<string, string>)['Idempotency-Key'], restored.key);
  assert.deepEqual(requests[0].headers, requests[1].headers);
  assert.equal((requests[0].headers as Record<string, string>)['X-CSRF-Token'], 'csrf-1');
  assert.equal(pendingStorageKey('org-a'), 'poaktu:pending-deal:org-a');
  assert.notEqual(pendingStorageKey('org-a'), pendingStorageKey('org-b'));
});

test('saved command corruption is explicit instead of silently starting a fresh command', () => {
  assert.equal(readPendingDeal(null), null);
  assert.throws(() => readPendingDeal('{broken'));
  assert.throws(() => readPendingDeal(JSON.stringify({ key: 'short' })));
});

test('late completion of command A cannot delete newer pending command B', () => {
  const values = new Map<string, string>();
  const storage = { getItem: (key: string) => values.get(key) ?? null, removeItem: (key: string) => { values.delete(key); } };
  const key = pendingStorageKey('org-a');
  values.set(key, JSON.stringify({ key: 'command-B-newer' }));
  assert.equal(removePendingDeal(storage, 'org-a', 'command-A-original'), false);
  assert.equal(values.get(key), JSON.stringify({ key: 'command-B-newer' }));
  assert.equal(removePendingDeal(storage, 'org-a', 'command-B-newer'), true);
  assert.equal(values.has(key), false);
  assert.equal(removePendingDeal(storage, 'org-a', 'command-B-newer'), false);
  values.set(key, '{malformed');
  assert.equal(removePendingDeal(storage, 'org-a', 'command-B-newer'), false);
  assert.equal(values.get(key), '{malformed');
});

test('inclusive calendar-day upper bound covers PostgreSQL microsecond precision', () => {
  assert.match(dateBoundary('2026-10-20', true), /\.999999Z$/);
  const expected = new Date('2026-10-20T23:59:59.999').toISOString();
  assert.equal(dateBoundary('2026-10-20', true).replace('.999999Z', '.999Z'), expected);
  assert.equal(dateBoundary('', true), '');
  assert.throws(() => dateBoundary('10000-10-20', true));
  assert.throws(() => dateBoundary('0000-10-20', false));
});

test('deals filters, opaque cursor, and detail identifiers are encoded without extra account scope', async () => {
  const requests: string[] = [];
  const client = new ApiClient(async (url) => { requests.push(String(url)); return Response.json({ items: [], next_cursor: null }); });
  await client.deals({ q: 'Типография & ИП', role: 'supplier', status: 'DRAFT', created_from: '2026-10-20T00:00:00Z', created_to: '2026-10-20T23:59:59Z' }, 'opaque+/=');
  const params = new URL(requests[0], 'http://localhost').searchParams;
  assert.equal(params.get('q'), 'Типография & ИП');
  assert.equal(params.get('cursor'), 'opaque+/=');
  assert.equal(params.get('limit'), '20');
  assert.equal(params.get('role'), 'supplier');
  assert.equal(params.get('status'), 'DRAFT');
  assert.equal(params.get('created_to'), '2026-10-20T23:59:59Z');
  assert.equal(params.has('customer_party_id'), false);
  await client.deal('a/b?x=1');
  assert.equal(requests[1], '/api/v1/deals/a%2Fb%3Fx%3D1');
});

test('command conflict exposes a clear stable message and does not automatically resend', async () => {
  let posts = 0;
  const client = new ApiClient(async (url) => {
    if (String(url).endsWith('/csrf')) return Response.json({ csrf_token: 'csrf' });
    posts += 1;
    return Response.json({ error: { code: 'IDEMPOTENCY_CONFLICT', message: 'private internals' } }, { status: 409 });
  });
  await assert.rejects(client.createDeal(createDealInput(fields, now), 'a-real-key-at-least-16'), (error: unknown) => error instanceof ApiError && error.code === 'IDEMPOTENCY_CONFLICT' && !error.message.includes('private'));
  assert.equal(posts, 1);
});
