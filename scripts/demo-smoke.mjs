// Additive CLI/API/browser acceptance; credentials are never printed or uploaded.
import assert from 'node:assert/strict';
import { randomBytes, randomUUID } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join, isAbsolute } from 'node:path';
import { createRequire } from 'node:module';
const base = (process.env.BASE_URL ?? 'http://localhost:8080').replace(/\/$/, '');
const origin = process.env.APP_ORIGIN ?? base;
const binary = process.env.DEMO_BINARY;
const directory = process.env.POAKTU_DEMO_INPUT_DIR;
const loopback = ['localhost', '127.0.0.1', '[::1]'];
assert(loopback.includes(new URL(base).hostname));
assert(directory && isAbsolute(directory), 'A fresh absolute private input directory is required');
if (binary) { const db = new URL(process.env.DATABASE_URL); assert(loopback.includes(db.hostname) && db.pathname.endsWith('_test'), 'Local smoke requires a loopback _test database'); }
mkdirSync(directory, { mode: 0o700 }); // Refuse an existing directory.
const password = randomBytes(24).toString('base64url');
const passwordFile = join(directory, 'password.txt');
writeFileSync(passwordFile, password + '\n', { mode: 0o600, flag: 'wx' });
if (!binary) {
  assert.equal(process.platform, 'linux', 'Container acceptance runs on Linux');
  const r = spawnSync('sudo', ['chown', '10001:10001', directory, passwordFile], { encoding: 'utf8' });
  assert.equal(r.status, 0, 'Cannot give the test CLI access to its private input');
}
function invoke(args, runtime = false) {
  if (binary) return spawnSync(binary, args, { encoding: 'utf8', timeout: 150000, env: { ...process.env, ...(runtime ? { DATABASE_URL: process.env.DEMO_RUNTIME_DATABASE_URL } : {}) } });
  return spawnSync('docker', ['compose', '-f', 'compose.yaml', '-f', 'compose.demo.yaml', 'run', '--rm', '--no-deps', ...(runtime ? ['-e', 'DATABASE_URL=postgres://poaktu_runtime:poaktu_runtime_demo_only@db:5432/poaktu?sslmode=disable'] : []), 'demo', ...args], { encoding: 'utf8', timeout: 150000 });
}
function cli(run, expected = 0, code = undefined, runtime = false) {
  const r = invoke(['demo', 'create', '--run-id', run, '--password-file', binary ? passwordFile : '/input/password.txt'], runtime);
  assert.ifError(r.error); assert.equal(r.status, expected, 'CLI exit differs at ' + run);
  const value = JSON.parse(r.stdout.trim()); assert.equal(value.status, expected === 0 ? 'DEMO_READY' : 'DEMO_FAILED');
  if (code) assert.equal(value.code, code);
  assert(!r.stdout.includes(password), 'CLI exposed its input password'); return value;
}
class Client {
  cookies = new Map(); csrf = ''; user = '';
  async request(path, { method = 'GET', body, expected = 200 } = {}) {
    const mutation = method !== 'GET';
    const r = await fetch(base + path, { method, redirect: 'error', headers: {
      Cookie: [...this.cookies].map(([k, v]) => `${k}=${v}`).join('; '),
      ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(mutation ? { Origin: origin, 'X-CSRF-Token': this.csrf, 'Idempotency-Key': randomUUID() } : {}),
      ...(this.user ? { 'X-Expected-User-ID': this.user } : {}),
    }, ...(body ? { body: JSON.stringify(body) } : {}) });
    for (const cookie of r.headers.getSetCookie()) { const [key, ...rest] = cookie.split(';')[0].split('='); const value = rest.join('='); if (value) this.cookies.set(key, value); else this.cookies.delete(key); }
    const text = await r.text(); const value = text ? JSON.parse(text) : null;
    assert.equal(r.status, expected, `${method} ${path}: ${value?.error?.code ?? r.status}`); return value;
  }
  async login(email) {
    this.csrf = (await this.request('/api/v1/auth/csrf')).csrf_token;
    const identity = await this.request('/api/v1/auth/login', { method: 'POST', body: { email, password } });
    this.user = identity.user.id; this.csrf = (await this.request('/api/v1/auth/csrf')).csrf_token;
  }
}
let stage = 'prepare';
async function main() {
  const run = 'smoke-' + randomUUID().replaceAll('-', '').slice(0, 20);
  const a = cli(run + '-a'), again = cli(run + '-a');
  assert.equal(a.resumed, false); assert.equal(again.resumed, true); assert.deepEqual(a.deals, again.deals); assert.deepEqual(a.identities, again.identities);
  stage = 'restricted runtime'; cli(run + '-denied', 3, 'OWNER_REQUIRED', true);
  const b = cli(run + '-b'); assert.notEqual(a.identities[0].account_id, b.identities[0].account_id);
  const customer = new Client(), supplier = new Client(), outsider = new Client();
  await customer.login(a.identities[0].email); await supplier.login(a.identities[1].email); await outsider.login(b.identities[0].email);
  assert.equal((await customer.request('/api/v1/organisations/me')).account.total_kopecks, '50000000');
  assert.equal((await supplier.request('/api/v1/organisations/me')).account.total_kopecks, '0');
  const page = await customer.request('/api/v1/deals?limit=100'); assert.equal(page.items.length, 4); assert(page.items.every(d => d.status === 'DRAFT' && !d.document && !d.reserve && d.signatures.length === 0));
  await outsider.request('/api/v1/deals/' + a.deals[0].id, { expected: 404 });
  stage = 'browser';
  const require = createRequire(new URL('../web/package.json', import.meta.url));
  const { chromium, expect } = require('@playwright/test');
  const browser = await chromium.launch();
  try {
    for (const identity of a.identities) {
      stage = 'browser ' + identity.role;
      const mobile = identity.role === 'supplier';
      const context = await browser.newContext({ locale: 'ru-RU', timezoneId: 'Europe/Moscow', viewport: { width: mobile ? 375 : 1440, height: mobile ? 812 : 1000 }, isMobile: mobile, hasTouch: mobile });
      try {
        const ui = await context.newPage(); const errors = []; ui.on('pageerror', e => errors.push(e.name));
        await ui.goto(base); await ui.getByLabel('Email', { exact: true }).fill(identity.email); await ui.getByLabel('Пароль', { exact: true }).fill(password);
        await ui.getByRole('button', { name: 'Войти в кабинет' }).click();
        if (identity.role === 'operator') {
          await expect(ui.getByRole('heading', { name: 'Панель оператора', exact: true })).toBeVisible();
          await expect(ui.locator('.balance-card')).toHaveCount(0);
        } else {
          await expect(ui.getByRole('heading', { name: 'Учебный счёт', exact: true })).toBeVisible();
          await expect(ui.locator('.balance-total strong')).toHaveText(identity.role === 'customer' ? '500 000,00 ₽' : '0,00 ₽');
          await expect(ui.locator('.balance-reserved strong')).toHaveText('0,00 ₽');
          await ui.getByRole('button', { name: 'Сделки', exact: true }).click(); await expect(ui.locator('.deal-row')).toHaveCount(4);
          await expect(ui.locator('.deal-row').first()).toContainText(run + '-a');
        }
        assert.equal(await ui.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1), false, 'Horizontal overflow');
        assert.equal(errors.length, 0, 'Browser error');
        if (process.env.DEMO_SCREENSHOT_DIR) {
          if (identity.role === 'customer') mkdirSync(process.env.DEMO_SCREENSHOT_DIR, { recursive: true });
          await ui.screenshot({ path: join(process.env.DEMO_SCREENSHOT_DIR, identity.role + '.png'), fullPage: true });
        }
      } finally { await context.close(); }
    }
  } finally { await browser.close(); }
  stage = 'settlement';
  const deal = a.deals.find(d => d.scenario === 'payment');
  const doc = await customer.request(`/api/v1/deals/${deal.id}/documents/generate`, { method: 'POST', body: { expected_deal_version: 1, template_id: 'printing-acceptance-v1' }, expected: 201 });
  const agreement = { deal_version: doc.deal.deal_version, document_id: doc.document.id, document_version: doc.document.document_version, document_sha256: doc.document.sha256 };
  for (const actor of [customer, supplier]) await actor.request(`/api/v1/deals/${deal.id}/agree`, { method: 'POST', body: agreement });
  await customer.request(`/api/v1/deals/${deal.id}/reserve`, { method: 'POST', body: { deal_version: agreement.deal_version }, expected: 201 });
  const sign = { deal_version: agreement.deal_version, document_version: agreement.document_version, document_sha256: agreement.document_sha256 };
  for (const actor of [customer, supplier]) await actor.request(`/api/v1/documents/${agreement.document_id}/sign`, { method: 'POST', body: sign, expected: 201 });
  let paid;
  for (let i = 0; i < 100; i++) { paid = await customer.request('/api/v1/deals/' + deal.id); if (paid.status === 'SETTLED') break; await new Promise(resolve => setTimeout(resolve, 250)); }
  assert.equal(paid.status, 'SETTLED'); assert(paid.payment_id);
  const current = await customer.request('/api/v1/organisations/me'); assert.equal(current.account.total_kopecks, '20000000'); assert.equal(current.account.reserved_kopecks, '0');
  assert.equal((await supplier.request('/api/v1/organisations/me')).account.total_kopecks, '30000000');
  stage = 'bootstrap and paid replay';
  const boot = invoke(['bootstrap']); assert.ifError(boot.error); assert.equal(boot.status, 0, 'Repeat bootstrap failed');
  assert.deepEqual(cli(run + '-a').deals, a.deals);
  assert.deepEqual((await customer.request('/api/v1/organisations/me')).account, current.account);
  const after = await customer.request('/api/v1/deals/' + deal.id); assert.equal(after.status, 'SETTLED'); assert.equal(after.payment_id, paid.payment_id);
  assert.equal((await outsider.request('/api/v1/organisations/me')).account.total_kopecks, '50000000');
  if (process.env.DEMO_REPORT_FILE) writeFileSync(process.env.DEMO_REPORT_FILE, JSON.stringify({ run_a: a, run_b: b, payment_id: paid.payment_id, result: 'PASS' }, null, 2), { mode: 0o600, flag: 'wx' });
  console.log('Isolated demo smoke passed: fresh/repeat/restricted CLI, separate organisations, three browser roles desktop/375px, real settlement, repeat bootstrap and paid replay without recredit/reset. Credentials are not printed or uploaded.');
}
try { await main(); } catch (error) { console.error('Isolated demo smoke failed at ' + stage + '; no credentials emitted.'); if (!stage.startsWith('browser') && error?.code === 'ERR_ASSERTION') console.error(error.message); process.exitCode = 1; }
