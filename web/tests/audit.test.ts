import assert from 'node:assert/strict';
import test from 'node:test';
import { ApiClient, ApiError } from '../src/lib/api.ts';
import type { AuditEvent } from '../src/lib/api.ts';
import { auditActor, auditFilters, auditResultLabels } from '../src/lib/audit.ts';

const emptyFields = { type: '', from: '', to: '' };
test('history filters trim scope and include complete local calendar days at database precision', () => {
  assert.deepEqual(auditFilters(emptyFields), {});
  const result = auditFilters({ type: ' DEAL_SETTLED ', from: '2026-10-24', to: '2026-10-24', dealId: ' deal_1-A ' });
  assert.equal(result.type, 'DEAL_SETTLED'); assert.equal(result.deal_id, 'deal_1-A');
  assert.equal(result.from, new Date('2026-10-24T00:00:00.000').toISOString());
  assert.equal(result.to?.replace('.999999Z', '.999Z'), new Date('2026-10-24T23:59:59.999').toISOString());
  for (const fields of [{ type: 'future?' }, { from: '2026-02-30' }, { from: '0000-01-01' }, { from: '2026-10-25', to: '2026-10-24' }, { dealId: 'x?organisation_id=other' }]) {
    assert.throws(() => auditFilters({ ...emptyFields, ...fields }));
  }
});

test('audit labels distinguish a recorded refusal to cancel from a rejected command and preserve actor source', () => {
  assert.equal(auditResultLabels.SUCCEEDED, 'Действие выполнено');
  assert.equal(auditResultLabels.REJECTED, 'Команда отклонена');
  const event = { actor_user_id: 'user', actor_organization_id: 'org', actor_source: null } as AuditEvent;
  assert.equal(auditActor(event), 'Пользователь организации');
  assert.equal(auditActor({ ...event, actor_organization_id: null }), 'Учебный оператор');
  assert.equal(auditActor({ ...event, actor_user_id: null, actor_source: 'edo-emulator' }), 'Учебный источник ЭДО');
});

test('history requests bind displayed actor, opaque cursor and exact participant or operator scope', async () => {
  const calls: { url: string; headers: Headers }[] = [];
  const client = new ApiClient(async (url, options) => { calls.push({ url: String(url), headers: new Headers(options?.headers) }); return Response.json({ items: [], next_cursor: null }); });
  client.bindIdentity('visible-user');
  await client.dealAudit('a/b', { type: 'DEAL_SETTLED', from: '2026-10-24T00:00:00Z' }, 'opaque+/=');
  await client.demoAudit({ deal_id: 'deal-a', to: '2026-10-24T23:59:59.999999Z' });
  assert.ok(calls[0].url.startsWith('/api/v1/deals/a%2Fb/audit?'));
  const query = new URL(calls[0].url, 'http://localhost').searchParams;
  assert.equal(query.get('limit'), '20'); assert.equal(query.get('cursor'), 'opaque+/='); assert.equal(query.get('type'), 'DEAL_SETTLED');
  assert.equal(query.get('organisation_id'), null); assert.ok(calls[1].url.startsWith('/api/v1/demo/audit?'));
  assert.equal(new URL(calls[1].url, 'http://localhost').searchParams.get('deal_id'), 'deal-a');
  assert.ok(calls.every(call => call.headers.get('X-Expected-User-ID') === 'visible-user'));
});

test('export sends only applied filters, requests exact format and preserves authenticated bytes', async () => {
  const calls: { url: string; options: RequestInit }[] = [];
  const client = new ApiClient(async (url, options) => { calls.push({ url: String(url), options: options! }); return new Response('id,reason\r\n1,"кириллица"', { headers: { 'Content-Type': 'text/csv; charset=utf-8' } }); });
  client.bindIdentity('visible-user');
  assert.match(await (await client.exportDealAudit('a/b', 'csv', { type: 'DEAL_REWORKED', to: '2026-10-24T23:59:59Z' })).text(), /кириллица/);
  const query = new URL(calls[0].url, 'http://localhost').searchParams;
  assert.ok(calls[0].url.startsWith('/api/v1/deals/a%2Fb/audit/export?'));
  assert.equal(query.get('format'), 'csv'); assert.equal(query.get('type'), 'DEAL_REWORKED');
  assert.equal(query.has('cursor'), false); assert.equal(query.has('limit'), false);
  assert.equal(calls[0].options.credentials, 'same-origin');
  assert.equal(new Headers(calls[0].options.headers).get('X-Expected-User-ID'), 'visible-user');
  assert.equal(new Headers(calls[0].options.headers).get('Accept'), 'text/csv');
});

test('export refuses wrong content types and translates bounded-export errors without raw server text', async () => {
  for (const mime of ['text/html', 'text/csv-malicious', 'application/json']) {
    const client = new ApiClient(async () => new Response('wrong', { headers: { 'Content-Type': mime } }));
    await assert.rejects(client.exportDealAudit('deal', 'csv'), /неизвестного формата/);
  }
  const client = new ApiClient(async () => Response.json({ error: { code: 'EXPORT_TOO_LARGE', message: 'private database text' }, request_id: 'audit-limit' }, { status: 422 }));
  await assert.rejects(client.exportDealAudit('deal', 'json'), (error: unknown) => error instanceof ApiError && error.code === 'EXPORT_TOO_LARGE' && error.requestId === 'audit-limit' && /5 000/.test(error.message) && !/private/.test(error.message));
});

test('a late history response cannot be handed to a newly bound account', async () => {
  let reply!: (response: Response) => void;
  const client = new ApiClient(() => new Promise(resolve => { reply = resolve; }));
  client.bindIdentity('old'); const pending = client.dealAudit('old-deal');
  const rejected = assert.rejects(pending, (error: unknown) => error instanceof ApiError && error.code === 'SESSION_CHANGED');
  client.bindIdentity('new'); reply(Response.json({ items: [{ id: 'private-old-event' }], next_cursor: null })); await rejected;
});

test('identity change while the export body is still arriving discards all bytes', async () => {
  let stream!: ReadableStreamDefaultController<Uint8Array>;
  let began!: () => void; const receiving = new Promise<void>(resolve => { began = resolve; });
  const client = new ApiClient(async () => new Response(new ReadableStream<Uint8Array>({ start(controller) { stream = controller; began(); } }), { headers: { 'Content-Type': 'application/json' } }));
  client.bindIdentity('old'); const pending = client.exportDealAudit('old-deal', 'json');
  const rejected = assert.rejects(pending, (error: unknown) => error instanceof ApiError && error.code === 'SESSION_CHANGED');
  await receiving; client.bindIdentity('new'); stream.enqueue(new TextEncoder().encode('{"items":[]}')); stream.close(); await rejected;
});

test('a late export authentication failure does not clear the new account security token', async () => {
  let reply!: (response: Response) => void; let csrfReads = 0;
  const client = new ApiClient(async url => String(url).endsWith('/csrf') ? (csrfReads++, Response.json({ csrf_token: 'new-token' })) : new Promise(resolve => { reply = resolve; }));
  client.bindIdentity('old'); const pending = client.exportDealAudit('old-deal', 'json');
  const rejected = assert.rejects(pending, (error: unknown) => error instanceof ApiError && error.status === 401);
  client.bindIdentity('new'); assert.equal(await client.csrf(), 'new-token');
  reply(Response.json({ error: { code: 'SESSION_EXPIRED' } }, { status: 401 })); await rejected;
  assert.equal(await client.csrf(), 'new-token'); assert.equal(csrfReads, 1);
});
