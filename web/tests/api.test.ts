import assert from 'node:assert/strict';
import test from 'node:test';
import { ApiClient, ApiError, parseApiError } from '../src/lib/api.ts';

test('default browser transport retains the global fetch receiver', async () => {
  const original = globalThis.fetch;
  try {
    globalThis.fetch = async function () {
      assert.equal(this, globalThis);
      return Response.json({ user: { id: 'u' } });
    };
    await new ApiClient().me();
  } finally { globalThis.fetch = original; }
});

test('error messages use stable codes without exposing arbitrary server text', () => {
  const error = parseApiError(401, { error: { code: 'INVALID_CREDENTIALS', message: 'private SQL or email' }, request_id: 'req-1' });
  assert.equal(error.code, 'INVALID_CREDENTIALS');
  assert.equal(error.requestId, 'req-1');
  assert.doesNotMatch(error.message, /private/);
  assert.equal(parseApiError(500, '<html>').code, 'UNKNOWN_ERROR');
  assert.equal(parseApiError(500, { error: { code: 'FUTURE_ERROR', message: 'secret' } }).message, 'Не удалось выполнить запрос. Повторите попытку позже.');
});

test('mutation obtains csrf first, uses same-origin cookies, and does not send browser roles', async () => {
  const calls: { url: string; options?: RequestInit }[] = [];
  const client = new ApiClient(async (input, options) => {
    calls.push({ url: String(input), options });
    return Response.json(calls.length === 1 ? { csrf_token: 'token-one' } : { user: { id: 'u' } });
  });
  await client.register({ email: 'owner@example.test', password: 'a-demo-password', organisation: { name: 'Test', kind: 'COMPANY' } });
  assert.equal(calls[0].url, '/api/v1/auth/csrf');
  assert.equal(calls[1].url, '/api/v1/auth/register');
  assert.equal(calls[1].options?.credentials, 'same-origin');
  assert.equal((calls[1].options?.headers as Record<string, string>)['X-CSRF-Token'], 'token-one');
  assert.deepEqual(JSON.parse(calls[1].options?.body as string), { email: 'owner@example.test', password: 'a-demo-password', organisation: { name: 'Test', kind: 'COMPANY' } });
});

test('CSRF rejection discards token and does not automatically repeat a mutation', async () => {
  const calls: string[] = [];
  const client = new ApiClient(async (input) => {
    const path = String(input);
    calls.push(path);
    return path.endsWith('/csrf')
      ? Response.json({ csrf_token: 'fresh-token' })
      : Response.json({ error: { code: 'CSRF_INVALID' } }, { status: 403 });
  });
  await assert.rejects(client.login('owner@example.test', 'password'), (error: unknown) => error instanceof ApiError && error.code === 'CSRF_INVALID');
  assert.equal(calls.length, 2);
  await client.csrf();
  assert.equal(calls.length, 3);
  assert.ok(calls[2].endsWith('/csrf'));
});

test('catalog encodes query and opaque cursor, session failures remain explicit', async () => {
  let called = '';
  const client = new ApiClient(async (input) => {
    called = String(input);
    return Response.json({ error: { code: 'SESSION_EXPIRED' } }, { status: 401 });
  });
  await assert.rejects(client.organisations('Тест & ИП', 'a+b/='), (error: unknown) => error instanceof ApiError && error.status === 401 && error.code === 'SESSION_EXPIRED');
  const query = new URL(called, 'http://localhost').searchParams;
  assert.equal(query.get('q'), 'Тест & ИП');
  assert.equal(query.get('cursor'), 'a+b/=');
});

test('clearing CSRF invalidates the old flight without replacing the new flight or token', async () => {
  const replies: ((response: Response) => void)[] = [];
  const client = new ApiClient(() => new Promise<Response>((resolve) => replies.push(resolve)));
  const old = client.csrf();
  const rejected = assert.rejects(old, (error: unknown) => error instanceof ApiError && error.code === 'SESSION_CHANGED');
  client.clearCsrf();
  const fresh = client.csrf();
  replies[0](Response.json({ csrf_token: 'old-session-token' }));
  await rejected;
  const joined = client.csrf();
  assert.equal(replies.length, 2, 'old finally must not discard the new pending request');
  replies[1](Response.json({ csrf_token: 'new-session-token' }));
  assert.equal(await fresh, 'new-session-token');
  assert.equal(await joined, 'new-session-token');
  assert.equal(await client.csrf(), 'new-session-token');
});

test('protected requests carry the displayed actor, while session discovery stays unbound', async () => {
  const calls: { url: string; headers: Headers }[] = [];
  const client = new ApiClient(async (url, options) => {
    calls.push({ url: String(url), headers: new Headers(options?.headers) });
    return Response.json(String(url).endsWith('/csrf') ? { csrf_token: 'token' } : {});
  });
  client.bindIdentity('displayed-user');
  await client.me();
  await client.ownOrganisation();
  await client.logout();
  assert.equal(calls.find(x => x.url.endsWith('/auth/me'))!.headers.get('X-Expected-User-ID'), null);
  assert.equal(calls.find(x => x.url.endsWith('/organisations/me'))!.headers.get('X-Expected-User-ID'), 'displayed-user');
  assert.equal(calls.find(x => x.url.endsWith('/auth/logout'))!.headers.get('X-Expected-User-ID'), 'displayed-user');
});

test('identity change during CSRF preflight cancels the old mutation without sending it', async () => {
  let respond!: (response: Response) => void;
  let calls = 0;
  const client = new ApiClient(() => { calls++; return new Promise<Response>(resolve => { respond = resolve; }); });
  client.bindIdentity('old-user');
  const pending = client.logout();
  const rejected = assert.rejects(pending, (error: unknown) => error instanceof ApiError && error.code === 'SESSION_CHANGED');
  client.bindIdentity('new-user');
  respond(Response.json({ csrf_token: 'old-token' }));
  await rejected;
  assert.equal(calls, 1, 'only preflight, no old mutation under the new actor');
});

test('a late 401 for the previous actor cannot clear the new actor CSRF cache', async () => {
  let oldReply!: (response: Response) => void;
  let csrfReads = 0;
  const client = new ApiClient(async (url) => {
    if (String(url).endsWith('/csrf')) { csrfReads++; return Response.json({ csrf_token: 'new-actor-token' }); }
    return new Promise<Response>(resolve => { oldReply = resolve; });
  });
  client.bindIdentity('old-user');
  const old = client.ownOrganisation();
  const rejected = assert.rejects(old, (error: unknown) => error instanceof ApiError && error.status === 401);
  client.bindIdentity('new-user');
  assert.equal(await client.csrf(), 'new-actor-token');
  oldReply(Response.json({ error: { code: 'SESSION_EXPIRED' } }, { status: 401 }));
  await rejected;
  assert.equal(await client.csrf(), 'new-actor-token');
  assert.equal(csrfReads, 1);
});

test('agreement commands preserve version bindings, command keys and the displayed actor', async () => {
  const calls: { path: string; options: RequestInit }[] = [];
  const client = new ApiClient(async (url, options) => {
    calls.push({ path: String(url), options: options! });
    return Response.json(String(url).endsWith('/csrf') ? { csrf_token: 'csrf-agreement' } : {});
  });
  client.bindIdentity('visible-actor');
  const update = { expected_version: 7, title: 'Новые условия' };
  const agree = { deal_version: 8, document_id: 'doc-a', document_version: 4, document_sha256: 'a'.repeat(64) };
  const rework = { deal_version: 8, reason: 'Уточнить результат' };
  const generate = { expected_deal_version: 7, template_id: 'printing-acceptance-v1' as const };
  await client.updateDeal('deal/a', update, 'fixed-key-update');
  await client.agreeDeal('deal/a', agree, 'fixed-key-agree');
  await client.reworkDeal('deal/a', rework, 'fixed-key-rework');
  await client.generateDocument('deal/a', generate, 'fixed-key-generate');
  const commands = calls.slice(1);
  assert.equal(commands[0].path, '/api/v1/deals/deal%2Fa');
  assert.equal(commands[0].options.method, 'PATCH');
  assert.deepEqual(commands.map(item => JSON.parse(item.options.body as string)), [update, agree, rework, generate]);
  for (const command of commands) {
    const headers = new Headers(command.options.headers);
    assert.equal(headers.get('X-Expected-User-ID'), 'visible-actor');
    assert.equal(headers.get('X-CSRF-Token'), 'csrf-agreement');
    assert.ok(headers.get('Idempotency-Key')?.startsWith('fixed-key-'));
  }
});

test('multipart retry sends exact PDF bytes and original filename without setting a random boundary', async () => {
  const calls: { path: string; options: RequestInit }[] = [];
  const client = new ApiClient(async (url, options) => {
    calls.push({ path: String(url), options: options! });
    return Response.json(String(url).endsWith('/csrf') ? { csrf_token: 'csrf-pdf' } : {});
  });
  client.bindIdentity('pdf-owner');
  const blob = new Blob(['%PDF-1.7\nsaved bytes\n%%EOF'], { type: 'application/pdf' });
  await client.uploadDocument('deal/a', 7, blob, 'original.pdf', 'fixed-upload-key');
  const call = calls[1];
  const headers = new Headers(call.options.headers);
  assert.equal(call.path, '/api/v1/deals/deal%2Fa/documents');
  assert.equal(headers.get('Content-Type'), null, 'browser supplies the multipart boundary');
  assert.equal(headers.get('X-Expected-User-ID'), 'pdf-owner');
  assert.equal(headers.get('Idempotency-Key'), 'fixed-upload-key');
  const form = call.options.body as FormData;
  assert.equal(form.get('expected_deal_version'), '7');
  assert.equal((form.get('file') as File).name, 'original.pdf');
  assert.equal(await (form.get('file') as File).text(), await blob.text());
});

test('authenticated document reads require a concrete PDF version and do not open non-PDF responses', async () => {
  const calls: { path: string; headers: Headers }[] = [];
  let fail = false;
  const client = new ApiClient(async (url, options) => {
    calls.push({ path: String(url), headers: new Headers(options?.headers) });
    if (fail) return new Response('<html>wrong</html>', { headers: { 'Content-Type': 'text/html' } });
    return new Response('%PDF-1.7\nbytes', { headers: { 'Content-Type': 'application/pdf' } });
  });
  client.bindIdentity('visible-user');
  assert.equal(await (await client.documentFile('doc/a', 5)).text(), '%PDF-1.7\nbytes');
  await client.documentFile('doc/a', 3, true);
  assert.deepEqual(calls.map(item => item.path), ['/api/v1/documents/doc%2Fa/view?version=5', '/api/v1/documents/doc%2Fa/download?version=3']);
  assert.equal(calls[0].headers.get('X-Expected-User-ID'), 'visible-user');
  fail = true;
  await assert.rejects(client.documentFile('doc/a', 5), /неизвестного формата/);
});
