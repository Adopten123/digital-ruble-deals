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
