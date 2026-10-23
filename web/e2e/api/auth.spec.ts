import { randomUUID } from 'node:crypto';
import type { APIResponse } from '@playwright/test';
import { test, expect, API_BASE_URL, E2E_BASE_URL, apiFor, csrf } from '../support.ts';

async function apiError(response: APIResponse, status: number, code: string) {
  expect(response.status()).toBe(status);
  const body = await response.json();
  expect(body).toMatchObject({ error: { code, message: expect.any(String) }, request_id: expect.any(String) });
  expect(body.request_id).toBe(response.headers()['x-request-id']);
  return body;
}

test('anonymous and forged sessions cannot read private resources', async ({ playwright }) => {
  const anonymous = await playwright.request.newContext({ baseURL: API_BASE_URL });
  const forged = await playwright.request.newContext({
    baseURL: API_BASE_URL,
    extraHTTPHeaders: { Cookie: `pa_session=${'A'.repeat(43)}` },
  });
  try {
    for (const path of ['/api/v1/auth/me', '/api/v1/organisations', '/api/v1/organisations/me', '/api/v1/deals']) {
      await apiError(await anonymous.get(path), 401, 'AUTH_REQUIRED');
      await apiError(await forged.get(path), 401, 'SESSION_EXPIRED');
    }
    const response = await anonymous.get('/api/v1/auth/csrf');
    expect(response.status()).toBe(200);
    expect((await response.json()).csrf_token).toEqual(expect.any(String));
    expect((await anonymous.storageState()).cookies.some(cookie => cookie.name === 'pa_session')).toBe(false);
  } finally {
    await anonymous.dispose();
    await forged.dispose();
  }
});

test('role identities and session expiry come from the server', async ({ playwright, actors }) => {
  for (const role of ['customer', 'supplier', 'outsider', 'operator'] as const) {
    const client = await apiFor(playwright, role);
    try {
      const response = await client.get('/api/v1/auth/me');
      expect(response.status()).toBe(200);
      const identity = await response.json();
      expect(identity.user).toEqual(actors[role].user);
      expect(identity.organisation).toEqual(actors[role].organisation);
      expect(identity.session_expires_at).toMatch(/Z$/);
      const remaining = Date.parse(identity.session_expires_at) - Date.now();
      expect(remaining).toBeGreaterThan(0);
      expect(remaining).toBeLessThanOrEqual(24 * 60 * 60 * 1000);
      expect(Object.keys(identity.user).sort()).toEqual(['email', 'id', 'system_role']);
      expect(identity.user.system_role).toBe(role === 'operator' ? 'DEMO_OPERATOR' : 'USER');
    } finally {
      await client.dispose();
    }
  }
});

test('wrong passwords do not reveal whether an email exists', async ({ playwright, actors }) => {
  const client = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    const token = await csrf(client);
    const errors = [];
    for (const email of [actors.customer.user.email, `missing-${randomUUID()}@demo.poaktu.local`]) {
      const response = await client.post('/api/v1/auth/login', {
        headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': token },
        data: { email, password: 'ThisPasswordIsWrong2026!' },
      });
      errors.push((await apiError(response, 401, 'INVALID_CREDENTIALS')).error);
    }
    expect(errors[0]).toEqual(errors[1]);
    expect((await client.storageState()).cookies.some(cookie => cookie.name === 'pa_session')).toBe(false);
  } finally {
    await client.dispose();
  }
});

test('registration cannot assign an operator role', async ({ playwright }) => {
  const client = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    const response = await client.post('/api/v1/auth/register', {
      headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) },
      data: {
        email: `role-probe-${randomUUID()}@demo.poaktu.local`, password: 'SyntheticApiPassword2026!',
        organisation: { name: 'Учебная проверка роли', kind: 'COMPANY' }, system_role: 'DEMO_OPERATOR',
      },
    });
    await apiError(response, 400, 'INVALID_REQUEST');
    await apiError(await client.get('/api/v1/auth/me'), 401, 'AUTH_REQUIRED');
  } finally {
    await client.dispose();
  }
});

test('a new zero-balance account rotates sessions and logout revokes a stolen cookie', async ({ playwright }) => {
  const client = await playwright.request.newContext({ baseURL: API_BASE_URL });
  const credentials = { email: `lifecycle-${randomUUID()}@demo.poaktu.local`, password: 'SyntheticApiPassword2026!' };
  let oldSession, loggedInSession;
  try {
    const preAuthToken = await csrf(client);
    const registration = { ...credentials, organisation: { name: `Учебная API ${randomUUID()}`, kind: 'IP' } };
    const registered = await client.post('/api/v1/auth/register', {
      headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': preAuthToken }, data: registration,
    });
    expect(registered.status()).toBe(201);
    const identity = await registered.json();
    expect(identity).toMatchObject({
      user: { email: credentials.email, system_role: 'USER' },
      organisation: { name: registration.organisation.name, kind: 'IP', role: 'ORG_OWNER' },
    });
    expect(identity.csrf_token).not.toBe(preAuthToken);
    const beforeLogin = await client.storageState();
    const session = beforeLogin.cookies.find(cookie => cookie.name === 'pa_session');
    const csrfCookie = beforeLogin.cookies.find(cookie => cookie.name === 'pa_csrf');
    expect(session).toMatchObject({ httpOnly: true, sameSite: 'Lax', path: '/' });
    expect(csrfCookie).toMatchObject({ httpOnly: true, sameSite: 'Lax', path: '/' });
    expect(session?.value).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(session?.secure).toBe(new URL(API_BASE_URL).protocol === 'https:');
    oldSession = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: beforeLogin });
    const own = await client.get('/api/v1/organisations/me');
    expect(own.status()).toBe(200);
    expect((await own.json()).account).toMatchObject({
      currency: 'RUB', total_kopecks: '0', reserved_kopecks: '0', available_kopecks: '0',
    });
    await apiError(await client.post('/api/v1/auth/register', {
      headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) }, data: registration,
    }), 409, 'EMAIL_CONFLICT');
    const login = await client.post('/api/v1/auth/login', {
      headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) }, data: credentials,
    });
    expect(login.status()).toBe(200);
    const afterLogin = await client.storageState();
    expect(afterLogin.cookies.find(cookie => cookie.name === 'pa_session')?.value).not.toBe(session?.value);
    expect((await login.json()).csrf_token).not.toBe(identity.csrf_token);
    await apiError(await oldSession.get('/api/v1/auth/me'), 401, 'SESSION_EXPIRED');
    loggedInSession = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: afterLogin });
    const logout = await client.post('/api/v1/auth/logout', {
      headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) },
    });
    expect(logout.status()).toBe(204);
    expect(await logout.body()).toHaveLength(0);
    expect((await client.storageState()).cookies.some(cookie => ['pa_session', 'pa_csrf'].includes(cookie.name))).toBe(false);
    await apiError(await client.get('/api/v1/auth/me'), 401, 'AUTH_REQUIRED');
    await apiError(await loggedInSession.get('/api/v1/auth/me'), 401, 'SESSION_EXPIRED');
  } finally {
    await oldSession?.dispose();
    await loggedInSession?.dispose();
    await client.dispose();
  }
});
