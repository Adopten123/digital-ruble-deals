import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';

// Creates only isolated учебные users with zero balances. Never resets a database.
const base = (process.env.BASE_URL ?? 'http://localhost:8080').replace(/\/$/, '');
const origin = process.env.APP_ORIGIN ?? base;
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(base).hostname), 'Smoke target must be loopback');

class Client {
  cookies = new Map();
  csrf = '';

  async request(path, { method = 'GET', body, headers = {}, expected = 200 } = {}) {
    const mutation = !['GET', 'HEAD'].includes(method);
    const response = await fetch(`${base}${path}`, {
      method,
      redirect: 'error',
      headers: {
        ...(this.cookies.size ? { Cookie: [...this.cookies].map(([k, v]) => `${k}=${v}`).join('; ') } : {}),
        ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        ...(mutation ? { Origin: origin, 'X-CSRF-Token': this.csrf } : {}),
        ...headers,
      },
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    });
    for (const cookie of response.headers.getSetCookie()) {
      const first = cookie.split(';', 1)[0];
      const separator = first.indexOf('=');
      const name = first.slice(0, separator);
      const value = first.slice(separator + 1);
      if (/max-age=0/i.test(cookie) || !value) this.cookies.delete(name);
      else this.cookies.set(name, value);
    }
    const text = await response.text();
    const json = text ? JSON.parse(text) : null;
    assert.equal(response.status, expected, `${method} ${path}: ${json?.error?.code ?? response.status}`);
    return { response, json };
  }

  async refreshCSRF() {
    const { json } = await this.request('/api/v1/auth/csrf');
    assert.equal(typeof json.csrf_token, 'string');
    this.csrf = json.csrf_token;
    return this.csrf;
  }

  async login(email) {
    await this.refreshCSRF();
    const result = await this.request('/api/v1/auth/login', {
      method: 'POST', body: { email, password: 'PoaktuDemo2026!' },
    });
    await this.refreshCSRF();
    return result;
  }
}

async function ready() {
  for (let attempt = 0; attempt < 30; attempt++) {
    try {
      const response = await fetch(`${base}/readyz`, { redirect: 'error' });
      if (response.status === 200) {
        const body = await response.json();
        assert.equal(body.contour, 'test');
        assert.equal(body.stage, 'foundation');
        return;
      }
    } catch (error) {
      if (attempt === 29) throw error;
    }
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  throw new Error('Foundation API did not become ready');
}

await ready();
const unauthenticated = new Client();
const { json: noSession } = await unauthenticated.request('/api/v1/auth/me', { expected: 401 });
assert.equal(noSession.error.code, 'AUTH_REQUIRED');
assert.equal(typeof noSession.request_id, 'string');
const preAuthToken = await unauthenticated.refreshCSRF();
assert.equal(unauthenticated.cookies.has('pa_session'), false);

const { json: badOrigin } = await unauthenticated.request('/api/v1/auth/login', {
  method: 'POST', headers: { Origin: 'https://not-poaktu.invalid' },
  body: { email: 'customer@demo.poaktu.local', password: 'PoaktuDemo2026!' }, expected: 403,
});
assert.equal(badOrigin.error.code, 'CSRF_INVALID');

const suffix = randomUUID();
const registration = {
  email: `smoke-${suffix}@demo.poaktu.local`, password: 'SmokeOnlyPassword2026!',
  organisation: { name: `Учебная smoke-организация ${suffix}`, kind: 'COMPANY' },
};
const { json: roleInjection } = await unauthenticated.request('/api/v1/auth/register', {
  method: 'POST', body: { ...registration, system_role: 'DEMO_OPERATOR' }, expected: 400,
});
assert.equal(roleInjection.error.code, 'INVALID_REQUEST');
const { json: registered, response: registrationResponse } = await unauthenticated.request('/api/v1/auth/register', {
  method: 'POST', body: registration, expected: 201,
});
assert.equal(registered.user.system_role, 'USER');
assert.equal(registered.organisation.role, 'ORG_OWNER');
assert.match(registered.session_expires_at, /Z$/);
const sessionCookie = registrationResponse.headers.getSetCookie().find(value => value.startsWith('pa_session='));
assert(sessionCookie);
assert.match(sessionCookie, /HttpOnly/i);
assert.match(sessionCookie, /SameSite=Lax/i);
assert.doesNotMatch(sessionCookie, /;\s*Secure/i); // Explicit local HTTP test contour.

const { json: staleCSRF } = await unauthenticated.request('/api/v1/auth/logout', {
  method: 'POST', headers: { 'X-CSRF-Token': preAuthToken }, expected: 403,
});
assert.equal(staleCSRF.error.code, 'CSRF_INVALID');
await unauthenticated.refreshCSRF();
const { json: own } = await unauthenticated.request('/api/v1/organisations/me');
assert.equal(own.organisation.id, registered.organisation.id);
for (const key of ['total_kopecks', 'reserved_kopecks', 'available_kopecks']) assert.equal(own.account[key], '0');

const { json: catalog } = await unauthenticated.request('/api/v1/organisations?limit=1');
assert.equal(catalog.items.length, 1);
assert.deepEqual(Object.keys(catalog.items[0]).sort(), ['id', 'is_demo', 'kind', 'name']);
assert.equal(typeof catalog.next_cursor, 'string');
const { json: next } = await unauthenticated.request(`/api/v1/organisations?limit=1&cursor=${encodeURIComponent(catalog.next_cursor)}`);
assert.notEqual(next.items[0].id, catalog.items[0].id);

const customer = new Client();
const { json: customerIdentity } = await customer.login('customer@demo.poaktu.local');
assert.notEqual(customerIdentity.organisation.id, own.organisation.id);
const { json: customerAccount } = await customer.request('/api/v1/organisations/me');
assert.equal(customerAccount.account.total_kopecks, '50000000');
assert.equal(customerAccount.account.reserved_kopecks, '0');
assert.equal(customerAccount.account.available_kopecks, '50000000');

const supplier = new Client();
await supplier.login('supplier@demo.poaktu.local');
const { json: supplierAccount } = await supplier.request('/api/v1/organisations/me');
assert.equal(supplierAccount.account.total_kopecks, '0');
assert.notEqual(supplierAccount.organisation.id, customerIdentity.organisation.id);

const operator = new Client();
const { json: operatorIdentity } = await operator.login('operator@demo.poaktu.local');
assert.equal(operatorIdentity.user.system_role, 'DEMO_OPERATOR');
assert.equal(operatorIdentity.organisation, null);
const { json: operatorAccount } = await operator.request('/api/v1/organisations/me', { expected: 403 });
assert.equal(operatorAccount.error.code, 'FORBIDDEN');

for (const client of [unauthenticated, customer, supplier, operator]) {
  const revoked = new Client();
  revoked.cookies = new Map(client.cookies);
  await client.request('/api/v1/auth/logout', { method: 'POST', expected: 204 });
  await revoked.request('/api/v1/auth/me', { expected: 401 });
}

console.log('Foundation smoke passed: readiness, strict role input, CSRF/origin/rotation, zero registration, catalog scope, demo balances, operator scope, server logout.');
