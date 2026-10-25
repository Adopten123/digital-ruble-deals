import assert from 'node:assert/strict';
import test from 'node:test';
import { ApiClient, ApiError } from '../src/lib/api.ts';
import { isAborted, NotificationReadGate, notificationQuery } from '../src/lib/notifications.ts';

test('notification read filters preserve explicit false instead of treating it as all', () => {
  assert.deepEqual(notificationQuery('all'), {}); assert.deepEqual(notificationQuery('unread'), { unread: true }); assert.deepEqual(notificationQuery('read'), { unread: false });
});
test('a new notification filter cancels the old fetch and a late completion cannot finish the current fetch', () => {
  const gate = new NotificationReadGate(); assert.equal(gate.busy, false);
  const old = gate.begin(); assert.equal(gate.busy, true); const current = gate.begin();
  assert.equal(old.signal.aborted, true); assert.equal(gate.accepts(old), false); assert.equal(gate.accepts(current), true);
  gate.finish(old); assert.equal(gate.busy, true); gate.finish(current); assert.equal(gate.busy, false); assert.equal(gate.accepts(current), false);
});
test('unmount or logout cancels the active notification read and invalidates its payload', () => {
  const gate = new NotificationReadGate(); const ticket = gate.begin(); gate.cancel();
  assert.equal(ticket.signal.aborted, true); assert.equal(gate.accepts(ticket), false); assert.equal(gate.busy, false);
  assert.equal(isAborted(new DOMException('cancelled', 'AbortError')), true); assert.equal(isAborted(new TypeError('network')), false);
});
test('notification list carries actor, cancellation, scope, exact filter and opaque cursor', async () => {
  const calls: { url: string; options: RequestInit }[] = [];
  const client = new ApiClient(async (url, options) => { calls.push({ url: String(url), options: options! }); return Response.json({ items: [], next_cursor: null, unread_count: 7 }); });
  client.bindIdentity('shown-user'); const controller = new AbortController();
  assert.equal((await client.notifications({ unread: false }, 'opaque+/=', controller.signal)).unread_count, 7);
  await client.notifications({ limit: 1 });
  const query = new URL(calls[0].url, 'http://localhost').searchParams;
  assert.equal(query.get('unread'), 'false'); assert.equal(query.get('limit'), '20'); assert.equal(query.get('cursor'), 'opaque+/=');
  assert.equal(new Headers(calls[0].options.headers).get('X-Expected-User-ID'), 'shown-user'); assert.equal(calls[0].options.signal, controller.signal);
  assert.equal(new URL(calls[1].url, 'http://localhost').searchParams.get('limit'), '1');
  assert.equal(new URL(calls[1].url, 'http://localhost').searchParams.has('unread'), false);
});
test('read is a protected empty POST and repeats without an idempotency key or reconstructed payload', async () => {
  const calls: { url: string; options: RequestInit }[] = [];
  const client = new ApiClient(async (url, options) => { calls.push({ url: String(url), options: options! }); return String(url).endsWith('/csrf') ? Response.json({ csrf_token: 'csrf' }) : new Response(null, { status: 204 }); });
  client.bindIdentity('shown-user'); await client.markNotificationRead('notification/a'); await client.markNotificationRead('notification/a');
  assert.equal(calls.length, 3);
  for (const call of calls.slice(1)) {
    const headers = new Headers(call.options.headers);
    assert.equal(call.url, '/api/v1/notifications/notification%2Fa/read'); assert.equal(call.options.method, 'POST'); assert.equal(call.options.body, undefined);
    assert.equal(headers.get('Idempotency-Key'), null); assert.equal(headers.get('Content-Type'), null); assert.equal(headers.get('X-CSRF-Token'), 'csrf'); assert.equal(headers.get('X-Expected-User-ID'), 'shown-user');
  }
});
test('notification read cancelled during CSRF never sends its POST', async () => {
  let reply!: (response: Response) => void; let calls = 0;
  const client = new ApiClient(() => { calls++; return new Promise(resolve => { reply = resolve; }); }); client.bindIdentity('old');
  const controller = new AbortController(); const pending = client.markNotificationRead('notification-a', controller.signal);
  const rejected = assert.rejects(pending, isAborted); controller.abort(); reply(Response.json({ csrf_token: 'old-token' })); await rejected; assert.equal(calls, 1);
});
test('a late notification page cannot cross an account change', async () => {
  let reply!: (response: Response) => void; const client = new ApiClient(() => new Promise(resolve => { reply = resolve; })); client.bindIdentity('old');
  const pending = client.notifications(); const rejected = assert.rejects(pending, (error: unknown) => error instanceof ApiError && error.code === 'SESSION_CHANGED');
  client.bindIdentity('new'); reply(Response.json({ items: [{ id: 'private-old' }], next_cursor: null, unread_count: 99 })); await rejected;
});
test('an account switch during notification CSRF never sends a read as the new user', async () => {
  let reply!: (response: Response) => void; let calls = 0;
  const client = new ApiClient(() => { calls++; return new Promise(resolve => { reply = resolve; }); }); client.bindIdentity('old');
  const pending = client.markNotificationRead('old-notification'); const rejected = assert.rejects(pending, (error: unknown) => error instanceof ApiError && error.code === 'SESSION_CHANGED');
  client.bindIdentity('new'); reply(Response.json({ csrf_token: 'old-token' })); await rejected; assert.equal(calls, 1);
});
