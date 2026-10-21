import { randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import type { Page } from '@playwright/test';
import { test, expect } from '../support.ts';

function operation(page: Page, path: string, method = 'get') {
  return page.locator(`.opblock-${method}`).filter({ has: page.locator('.opblock-summary-path').filter({ hasText: path }) });
}

async function openOperation(page: Page, path: string, method = 'get') {
  const block = operation(page, path, method);
  await block.locator('.opblock-summary-control').click();
  await block.getByRole('button', { name: 'Try it out', exact: true }).click();
  return block;
}

async function authorizeCsrf(page: Page, token: string) {
  await page.locator('.scheme-container').getByRole('button', { name: 'Authorize' }).click();
  const scheme = page.locator('.dialog-ux form').filter({ hasText: 'csrfToken' });
  const remove = scheme.getByRole('button', { name: 'Remove authorization', exact: true });
  if (await remove.isVisible()) await remove.click();
  await scheme.locator('input').fill(token);
  await scheme.getByRole('button', { name: 'Apply credentials', exact: true }).click();
  await scheme.getByRole('button', { name: 'Close', exact: true }).click();
}

test('Swagger renders the implemented contract using local assets and executes health', async ({ page }) => {
  const external: string[] = [];
  const errors: string[] = [];
  page.on('request', request => { if (new URL(request.url()).origin !== test.info().project.use.baseURL) external.push(request.url()); });
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
  // Vite does not send production CSP. Exercise the same page under nginx's policy locally too.
  const nginx = readFileSync(new URL('../../../deploy/nginx.conf', import.meta.url), 'utf8');
  const policy = nginx.match(/add_header Content-Security-Policy "([^"]+)" always;/)![1];
  await page.route('**/api-docs/', async route => {
    const response = await route.fetch();
    await route.fulfill({ response, headers: { ...response.headers(), 'content-security-policy': response.headers()['content-security-policy'] ?? policy } });
  });
  await page.goto('/api-docs/');
  await expect(page.getByRole('heading', { name: 'Документация API', exact: true })).toBeVisible();
  await expect(page.locator('.opblock')).toHaveCount(12);
  const block = await openOperation(page, '/healthz');
  const received = page.waitForResponse(response => response.url().endsWith('/healthz'));
  await block.getByRole('button', { name: 'Execute', exact: true }).click();
  const response = await received;
  expect(response.status()).toBe(200);
  expect(await response.json()).toEqual({ status: 'ok', contour: 'test' });
  await expect(block.locator('.live-responses-table tbody .response-col_status')).toContainText('200');
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('Swagger protected reads use the existing same-origin HttpOnly session', async ({ page, actors }) => {
  await page.goto('/api-docs/');
  const block = await openOperation(page, '/api/v1/auth/me');
  const received = page.waitForResponse(response => response.url().endsWith('/api/v1/auth/me'));
  await block.getByRole('button', { name: 'Execute', exact: true }).click();
  const response = await received;
  expect(response.status()).toBe(200);
  expect((await response.json()).user.id).toBe(actors.customer.user.id);
});

test('Swagger obtains CSRF, registers a synthetic account and logs it out with the rotated token', async ({ browser }) => {
  const context = await browser.newContext({ baseURL: test.info().project.use.baseURL });
  try {
    const page = await context.newPage();
    await page.goto('/api-docs/');
    const csrf = await openOperation(page, '/api/v1/auth/csrf');
    let received = page.waitForResponse(response => response.url().endsWith('/api/v1/auth/csrf'));
    await csrf.getByRole('button', { name: 'Execute', exact: true }).click();
    await authorizeCsrf(page, (await (await received).json()).csrf_token);
    const register = await openOperation(page, '/api/v1/auth/register', 'post');
    await register.locator('textarea.body-param__text').fill(JSON.stringify({ email: `swagger-${randomUUID()}@example.test`, password: 'SwaggerLocalOnly2026!', organisation: { name: `Swagger ${randomUUID()}`, kind: 'COMPANY' } }));
    received = page.waitForResponse(response => response.url().endsWith('/api/v1/auth/register'));
    await register.getByRole('button', { name: 'Execute', exact: true }).click();
    const registered = await received;
    expect(registered.status()).toBe(201);
    const session = await registered.json();
    await authorizeCsrf(page, session.csrf_token);
    const logout = await openOperation(page, '/api/v1/auth/logout', 'post');
    received = page.waitForResponse(response => response.url().endsWith('/api/v1/auth/logout'));
    await logout.getByRole('button', { name: 'Execute', exact: true }).click();
    expect((await received).status()).toBe(204);
    expect((await page.request.get('/api/v1/auth/me')).status()).toBe(401);
  } finally { await context.close(); }
});
