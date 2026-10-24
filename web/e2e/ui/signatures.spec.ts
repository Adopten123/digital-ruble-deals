import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import type { APIRequestContext, Browser, Page, PlaywrightWorkerArgs, Request } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, API_BASE_URL, E2E_BASE_URL } from '../support.ts';
import type { Deal, OwnOrganisation, Session } from '../../src/lib/api.ts';

test.describe.configure({ timeout: 120_000 });
const signLabel = 'Подписать документ в тестовом контуре';
const pendingLabel = 'Команда ожидает подтверждения: тестовая подпись документа';
async function command(client: APIRequestContext, path: string, input: unknown) {
  return client.post(path, { data: input, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': randomUUID() } });
}
async function fixture(playwright: PlaywrightWorkerArgs['playwright']) {
  const customer = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] }, maxRedirects: 0 });
  const supplier = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] }, maxRedirects: 0 });
  const operator = await apiFor(playwright, 'operator');
  const email = `signature-ui-${randomUUID()}@example.test`;
  const password = 'SignatureUiOnly2026!';
  const name = `Signature UI ${randomUUID()}`;
  try {
    async function register(client: APIRequestContext, identity: { email: string; password: string; name: string }): Promise<Session> {
      for (let attempt = 0; attempt < 2; attempt++) {
      const response = await client.post('/api/v1/auth/register', { data: { email: identity.email, password: identity.password, organisation: { name: identity.name, kind: 'COMPANY' } }, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) } });
      if (response.status() !== 429 || attempt === 1) { expect(response.status(), await response.text()).toBe(201); return response.json(); }
      expect((await response.json()).error.code).toBe('RATE_LIMITED');
      const seconds = Number(response.headers()['retry-after']);
      expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true);
      test.setTimeout(test.info().timeout + seconds * 1000);
      await test.step(`respect registration budget: wait ${seconds}s before explicit retry`, async () => { await delay(seconds * 1000); });
    }
      throw new Error('Synthetic signature registration did not return a session');
    }
    const session = await register(customer, { email, password, name });
    const supplierEmail = `signature-supplier-${randomUUID()}@example.test`;
    const supplierName = `Signature supplier ${randomUUID()}`;
    const supplierSession = await register(supplier, { email: supplierEmail, password, name: supplierName });
    const own = await (await customer.get('/api/v1/organisations/me')).json() as OwnOrganisation;
    expect((await command(operator, `/api/v1/demo/accounts/${own.account.id}/credits`, { amount_kopecks: '50000', reason: 'Isolated signature UI funding' })).status()).toBe(201);
    const response = await postDraft(customer, draftInput({ supplier_party_id: supplierSession.organisation!.id, title: `Signature deal ${randomUUID()}`, amount_kopecks: '30000' }));
    expect(response.status()).toBe(201);
    const initial = await response.json() as Deal;
    const generated = await command(customer, `/api/v1/deals/${initial.id}/documents/generate`, { expected_deal_version: 1, template_id: 'printing-acceptance-v1' });
    expect(generated.status()).toBe(201);
    const draft = (await generated.json()).deal as Deal;
    const binding = { deal_version: draft.deal_version, document_id: draft.document!.id, document_version: draft.document!.document_version, document_sha256: draft.document!.sha256 };
    expect((await command(customer, `/api/v1/deals/${draft.id}/agree`, binding)).status()).toBe(200);
    expect((await command(supplier, `/api/v1/deals/${draft.id}/agree`, binding)).status()).toBe(200);
    const reserved = await command(customer, `/api/v1/deals/${draft.id}/reserve`, { deal_version: draft.deal_version });
    expect(reserved.status()).toBe(201);
    const deal = (await reserved.json()).deal as Deal;
    const input = { deal_version: deal.deal_version, document_version: deal.document!.document_version, document_sha256: deal.document!.sha256 };
    return { customer, supplier, operator, session, supplierSession, supplierEmail, supplierName, email, password, name, deal, input, dispose: () => Promise.all([customer.dispose(), supplier.dispose(), operator.dispose()]) };
  } catch (error) { await Promise.all([customer.dispose(), supplier.dispose(), operator.dispose()]); throw error; }
}
async function customerPage(browser: Browser, client: APIRequestContext) {
  const context = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: await client.storageState() });
  return { context, page: await context.newPage() };
}
async function openDeal(page: Page, id: string) {
  await page.goto('/');
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(id);
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  await page.locator('.deal-row').filter({ hasText: id }).click();
  await expect(page.getByRole('region', { name: 'Тестовые подписи', exact: true })).toBeVisible();
}
async function sign(page: Page) {
  const panel = page.getByRole('region', { name: 'Тестовые подписи', exact: true });
  await expect(panel.getByRole('button', { name: signLabel, exact: true })).toBeDisabled();
  await panel.getByRole('checkbox').check();
  await panel.getByRole('button', { name: signLabel, exact: true }).click();
}
function sent(request: Request) { return { key: request.headers()['idempotency-key'], actor: request.headers()['x-expected-user-id'], body: request.postData() }; }
async function login(page: Page, email: string, password: string, name: string) {
  await page.getByRole('button', { name: 'Выйти', exact: true }).click();
  await page.getByLabel('Email', { exact: true }).fill(email);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  for (let attempt = 0; attempt < 2; attempt++) {
    const received = page.waitForResponse(response => response.url().endsWith('/auth/login') && response.request().method() === 'POST');
    await page.getByRole('button', { name: 'Войти в кабинет', exact: true }).click();
    const response = await received;
    if (response.status() !== 429 || attempt === 1) { expect(response.status()).toBe(200); break; }
    const seconds = Number(response.headers()['retry-after']);
    expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true);
    await test.step(`respect login budget: wait ${seconds}s before explicit retry`, async () => { await delay(seconds * 1000); });
  }
  await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
}

test('one explicit signature retains all balances and the second reaches one settlement in the card and list filter', async ({ browser, playwright }, testInfo) => {
  const setup = await fixture(playwright);
  const customer = await customerPage(browser, setup.customer);
  const supplierContext = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: await setup.supplier.storageState() });
  try {
    const customerBefore = (await (await setup.customer.get('/api/v1/organisations/me')).json()).account;
    const supplierBefore = (await (await setup.supplier.get('/api/v1/organisations/me')).json()).account;
    await openDeal(customer.page, setup.deal.id);
    await expect(customer.page.getByRole('region', { name: 'Тестовые подписи', exact: true })).toContainText('не квалифицированная электронная подпись');
    await sign(customer.page);
    await expect(customer.page.getByText('Ваша тестовая подпись сохранена', { exact: true })).toBeVisible();
    await expect(customer.page.locator('.deal-detail-heading')).toContainText('Средства зарезервированы');
    await expect(customer.page.getByRole('button', { name: signLabel, exact: true })).toHaveCount(0);
    const first = await (await setup.customer.get(`/api/v1/deals/${setup.deal.id}`)).json() as Deal;
    expect(first).toMatchObject({ status: 'FUNDED', payment_id: null, reserve: { status: 'ACTIVE' } });
    expect(first.signatures).toHaveLength(1);
    expect(first.signatures[0].organisation_id).toBe(setup.session.organisation!.id);
    expect((await (await setup.customer.get('/api/v1/organisations/me')).json()).account).toEqual(customerBefore);
    expect((await (await setup.supplier.get('/api/v1/organisations/me')).json()).account).toEqual(supplierBefore);
    const supplier = await supplierContext.newPage();
    await openDeal(supplier, setup.deal.id);
    await expect(supplier.locator('.signature-list .agreement-confirmed')).toHaveCount(1);
    await sign(supplier);
    await expect(supplier.getByText('Документ подписан обеими сторонами', { exact: true })).toBeVisible();
    await expect(supplier.locator('.deal-detail-heading')).toContainText('Расчёт завершён', { timeout: 20_000 });
    const ready = await (await setup.customer.get(`/api/v1/deals/${setup.deal.id}`)).json() as Deal;
    expect(ready).toMatchObject({ status: 'SETTLED', payment_id: expect.any(String), reserve: { id: setup.deal.reserve!.id, status: 'CONSUMED' } });
    expect(ready.signatures).toHaveLength(2);
    expect((await (await setup.customer.get('/api/v1/organisations/me')).json()).account).toMatchObject({ total_kopecks: '20000', reserved_kopecks: '0', available_kopecks: '20000' });
    expect((await (await setup.supplier.get('/api/v1/organisations/me')).json()).account).toMatchObject({ total_kopecks: '30000', reserved_kopecks: '0', available_kopecks: '30000' });
    await supplier.setViewportSize({ width: 320, height: 812 });
    expect(await supplier.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
    await expect(supplier.getByRole('button', { name: signLabel, exact: true })).toHaveCount(0);
    await testInfo.attach('mobile-two-signatures-settled', { body: await supplier.screenshot({ fullPage: true }), contentType: 'image/png' });
    await openDeal(customer.page, setup.deal.id);
    await expect(customer.page.getByText('Документ подписан обеими сторонами', { exact: true })).toBeVisible();
    await customer.page.getByRole('button', { name: '← К списку сделок', exact: true }).click();
    await customer.page.getByLabel('Поиск по названию, ID или контрагенту').fill(setup.deal.id);
    await customer.page.getByLabel('Состояние', { exact: true }).selectOption('SETTLED');
    await customer.page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
    await expect(customer.page.locator('.deal-row')).toHaveCount(1);
    await expect(customer.page.locator('.deal-row')).toContainText('Расчёт завершён');
  } finally { await customer.context.close(); await supplierContext.close(); await setup.dispose(); }
});

test('a lost committed signature survives reload and historical replay reads the actual settlement after both signatures', async ({ browser, playwright }) => {
  const setup = await fixture(playwright);
  const { context, page } = await customerPage(browser, setup.customer);
  try {
    await openDeal(page, setup.deal.id);
    const commands: ReturnType<typeof sent>[] = [];
    let historicalStatus = '';
    let currentReads = 0;
    page.on('request', request => { if (commands.length === 2 && request.method() === 'GET' && request.url().endsWith(`/deals/${setup.deal.id}`)) currentReads++; });
    await page.route(`**/api/v1/documents/${setup.deal.document!.id}/sign`, async route => {
      commands.push(sent(route.request()));
      const response = await route.fetch();
      expect(response.status()).toBe(201);
      if (commands.length === 1) await route.abort('failed');
      else { historicalStatus = (await response.json()).deal.status; await route.fulfill({ response }); }
    });
    await sign(page);
    await expect(page.getByRole('alert')).toContainText('Нет связи с сервером');
    await expect(page.getByText(pendingLabel, { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: signLabel, exact: true })).toBeDisabled();
    const storageKey = `poaktu:pending-agreement:${setup.session.organisation!.id}:${setup.deal.id}`;
    const saved = JSON.parse((await page.evaluate(key => sessionStorage.getItem(key), storageKey))!);
    expect(saved).toMatchObject({ kind: 'sign', documentId: setup.deal.document!.id, input: setup.input });
    expect((await command(setup.supplier, `/api/v1/documents/${setup.deal.document!.id}/sign`, setup.input)).status()).toBe(201);
    await expect.poll(async () => (await (await setup.customer.get(`/api/v1/deals/${setup.deal.id}`)).json()).status, { timeout: 20_000 }).toBe('SETTLED');
    await page.reload();
    await openDeal(page, setup.deal.id);
    await expect(page.getByText(pendingLabel, { exact: true })).toBeVisible();
    expect(commands).toHaveLength(1);
    await page.getByRole('button', { name: 'Проверить результат команды', exact: true }).click();
    await expect(page.getByText('Команда подтверждена сервером.', { exact: true })).toBeVisible();
    await expect.poll(() => currentReads).toBeGreaterThan(0);
    await expect(page.locator('.deal-detail-heading')).toContainText('Расчёт завершён');
    expect(historicalStatus).toBe('FUNDED');
    expect(commands).toHaveLength(2);
    expect(commands[1]).toEqual(commands[0]);
    expect(commands[0].actor).toBe(setup.session.user.id);
    expect(await page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBeNull();
    expect((await (await setup.customer.get(`/api/v1/deals/${setup.deal.id}`)).json()).signatures).toHaveLength(2);
  } finally { await context.close(); await setup.dispose(); }
});

test('a pending signature stays with its original organisation across logout and another party can sign independently', async ({ browser, playwright }) => {
  const setup = await fixture(playwright);
  const { context, page } = await customerPage(browser, setup.customer);
  try {
    await openDeal(page, setup.deal.id);
    const commands: ReturnType<typeof sent>[] = [];
    await page.route(`**/api/v1/documents/${setup.deal.document!.id}/sign`, async route => {
      commands.push(sent(route.request()));
      if (commands.length === 1) await route.abort('failed'); // Explicit no-delivery case; no product response is mocked.
      else await route.continue();
    });
    await sign(page);
    await expect(page.getByText(pendingLabel, { exact: true })).toBeVisible();
    const storageKey = `poaktu:pending-agreement:${setup.session.organisation!.id}:${setup.deal.id}`;
    const original = await page.evaluate(key => sessionStorage.getItem(key), storageKey);
    await login(page, setup.supplierEmail, setup.password, setup.supplierName);
    await openDeal(page, setup.deal.id);
    await expect(page.getByText(pendingLabel, { exact: true })).toHaveCount(0);
    await sign(page);
    await expect(page.getByText('Ваша тестовая подпись сохранена', { exact: true })).toBeVisible();
    expect(await page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBe(original);
    await login(page, setup.email, setup.password, setup.name);
    await openDeal(page, setup.deal.id);
    await expect(page.getByText(pendingLabel, { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: signLabel, exact: true })).toBeDisabled();
    await page.getByRole('button', { name: 'Проверить результат команды', exact: true }).click();
    await expect(page.getByText('Документ подписан обеими сторонами', { exact: true })).toBeVisible();
    expect(commands).toHaveLength(3);
    expect(commands[2]).toEqual(commands[0]);
    expect(commands[1].actor).toBe(setup.supplierSession.user.id);
    expect(commands[1].key).not.toBe(commands[0].key);
    expect(await page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBeNull();
    await expect(page.locator('.deal-detail-heading')).toContainText('Расчёт завершён', { timeout: 20_000 });
  } finally { await context.close(); await setup.dispose(); }
});
