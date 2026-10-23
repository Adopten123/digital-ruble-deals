import { randomUUID } from 'node:crypto';
import type { APIResponse } from '@playwright/test';
import { test, expect, API_BASE_URL, E2E_BASE_URL, apiFor, csrf, draftInput, postDraft } from '../support.ts';

async function apiError(response: APIResponse, status: number, code: string) {
  expect(response.status()).toBe(status);
  const body = await response.json();
  expect(body).toMatchObject({ error: { code, message: expect.any(String) }, request_id: expect.any(String) });
  expect(body.request_id).toBeTruthy();
  expect(response.headers()['x-request-id']).toBeTruthy();
  return body;
}

test('health and readiness identify the test contour and implemented stage', async ({ playwright }) => {
  const client = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    for (const path of ['/healthz', '/readyz']) {
      const response = await client.get(path);
      expect(response.status()).toBe(200);
      expect(await response.json()).toMatchObject(path === '/readyz'
        ? { status: 'ready', contour: 'test', stage: 'deal_agreement' }
        : { status: 'ok', contour: 'test' });
    }
  } finally {
    await client.dispose();
  }
});

test('HTTP responses use fresh request IDs, JSON errors and protective headers', async ({ playwright }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const ids = new Set<string>();
    for (const path of ['/api/v1/auth/me', '/api/v1/deals/nonexistent-e2e-resource']) {
      const response = await client.get(path, { headers: { 'X-Request-ID': 'untrusted-caller-id' } });
      expect(response.status()).toBe(path.endsWith('/me') ? 200 : 404);
      const headers = response.headers();
      expect(headers['content-type']).toMatch(/^application\/json;\s*charset=utf-8/i);
      expect(headers['cache-control']).toContain('no-store');
      expect(headers['x-content-type-options']).toContain('nosniff');
      expect(headers['referrer-policy']).toContain('same-origin');
      expect(headers['content-security-policy']).toContain("frame-ancestors 'none'");
      expect(headers['x-request-id']).toMatch(/^[A-Za-z0-9_-]{43}$/);
      expect(headers['x-request-id']).not.toBe('untrusted-caller-id');
      ids.add(headers['x-request-id']);
      if (response.status() === 404) {
        const body = await apiError(response, 404, 'RESOURCE_NOT_FOUND');
        expect(body.request_id).toBe(headers['x-request-id']);
        expect(JSON.stringify(body)).not.toMatch(/password|token_hash|SELECT |pgx|goroutine/i);
      }
    }
    expect(ids.size).toBe(2);
  } finally {
    await client.dispose();
  }
});

test('mutations require an exact Origin and a session-bound CSRF token', async ({ playwright }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  const anonymous = await playwright.request.newContext({ baseURL: API_BASE_URL });
  let transplanted;
  try {
    const token = await csrf(customer);
    const foreignToken = await csrf(supplier);
    const anonymousToken = await csrf(anonymous);
    const base = { Origin: E2E_BASE_URL, 'X-CSRF-Token': token, 'Idempotency-Key': randomUUID() };
    const probes = [
      { ...base, Origin: '' },
      { ...base, Origin: 'https://not-poaktu.invalid' },
      { ...base, Origin: `${E2E_BASE_URL}.attacker.invalid` },
      { ...base, 'X-CSRF-Token': '' },
      { ...base, 'X-CSRF-Token': foreignToken },
      { ...base, 'X-CSRF-Token': anonymousToken },
      { ...base, 'Sec-Fetch-Site': 'cross-site' },
    ];
    for (const headers of probes) {
      await apiError(await customer.post('/api/v1/deals', { data: draftInput(), headers }), 403, 'CSRF_INVALID');
    }
    // Matching header + cookie from another identity must still fail the session binding.
    const state = await customer.storageState();
    transplanted = await playwright.request.newContext({
      baseURL: API_BASE_URL,
      storageState: { ...state, cookies: state.cookies.map(cookie => cookie.name === 'pa_csrf' ? { ...cookie, value: foreignToken } : cookie) },
    });
    await apiError(await transplanted.post('/api/v1/deals', {
      data: draftInput(), headers: { ...base, 'X-CSRF-Token': foreignToken },
    }), 403, 'CSRF_INVALID');
    const crossOriginHeaders: Record<string, string>[] = [{ Origin: 'https://not-poaktu.invalid' }, { 'Sec-Fetch-Site': 'cross-site' }];
    for (const headers of crossOriginHeaders) {
      await apiError(await anonymous.get('/api/v1/auth/csrf', { headers }), 403, 'CSRF_INVALID');
    }
    expect((await postDraft(customer)).status()).toBe(201);
  } finally {
    await customer.dispose();
    await supplier.dispose();
    await anonymous.dispose();
    await transplanted?.dispose();
  }
});

test('strict transport rejects malformed JSON, unknown fields, nulls and wrong types', async ({ playwright }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const valid = draftInput();
    const malformed: (string | Buffer)[] = [
      '', 'null', '[]', 'true', '"text"', '{', `${JSON.stringify(valid)} {}`,
      JSON.stringify({ ...valid, customer_party_id: 'injected-customer' }),
      JSON.stringify({ ...valid, deal_version: 77 }),
      JSON.stringify({ ...valid, title: null }),
      JSON.stringify({ ...valid, amount_kopecks: 30000000 }),
      JSON.stringify({ ...valid, description: { text: 'wrong type' } }),
      JSON.stringify({ ...valid, deadline_at: false }),
      JSON.stringify({ title: 'missing required fields' }),
      Buffer.concat([Buffer.from('{"title":"'), Buffer.from([0xff]), Buffer.from('"}')]),
    ];
    const headers = { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Content-Type': 'application/json' };
    for (const data of malformed) {
      await apiError(await client.post('/api/v1/deals', {
        data, headers: { ...headers, 'Idempotency-Key': randomUUID() },
      }), 400, 'INVALID_REQUEST');
    }
  } finally {
    await client.dispose();
  }
});

test('content type, body limit and idempotency key are enforced on deployed HTTP', async ({ playwright }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const headers = { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': randomUUID() };
    for (const contentType of ['text/plain', 'application/x-www-form-urlencoded']) {
      await apiError(await client.post('/api/v1/deals', {
        data: JSON.stringify(draftInput()), headers: { ...headers, 'Content-Type': contentType },
      }), 415, 'UNSUPPORTED_MEDIA_TYPE');
    }
    await apiError(await client.post('/api/v1/deals', {
      data: { ...draftInput(), description: 'x'.repeat(17 * 1024) }, headers,
    }), 413, 'PAYLOAD_TOO_LARGE');
    for (const key of ['', 'short', 'x'.repeat(129), 'key with spaces and length']) {
      await apiError(await client.post('/api/v1/deals', {
        data: draftInput(), headers: { ...headers, 'Idempotency-Key': key },
      }), 400, 'IDEMPOTENCY_KEY_REQUIRED');
    }
    expect((await postDraft(client)).status()).toBe(201);
  } finally {
    await client.dispose();
  }
});

test('public deployment does not expose internal handlers or bypass HTTP methods', async ({ playwright }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    for (const path of ['/internal/edo/events', '/api/v1/internal/edo/events']) {
      const response = await client.post(path, { data: {} });
      expect(response.status()).toBe(404);
    }
    const input = draftInput();
    // The deployed server's catch-all responds with 404 for unsupported methods.
    await apiError(await client.put('/api/v1/deals', { data: input }), 404, 'RESOURCE_NOT_FOUND');
    const list = await client.get('/api/v1/deals', { params: { q: input.title } });
    expect(list.status()).toBe(200);
    expect(await list.json()).toEqual({ items: [], next_cursor: null });
    const missing = await client.get('/api/v1/nonexistent-e2e-route');
    expect(missing.status()).toBe(404);
    expect(missing.headers()['content-type']).not.toContain('text/html');
  } finally {
    await client.dispose();
  }
});
