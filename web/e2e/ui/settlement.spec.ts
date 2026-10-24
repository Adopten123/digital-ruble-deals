import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import type { APIRequestContext, Browser, Page, PlaywrightWorkerArgs, Request } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, API_BASE_URL, E2E_BASE_URL } from '../support.ts';
import type { Deal, OwnOrganisation, Session } from '../../src/lib/api.ts';

test.describe.configure({ timeout: 120_000 });
async function command(client: APIRequestContext, path: string, input: unknown) {
  return client.post(path, { data: input, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': randomUUID() } });
}
async function register(client: APIRequestContext, role: string): Promise<Session> {
  const input = { email: 'settlement-ui-' + randomUUID() + '@example.test', password: 'SettlementUiOnly2026!', organisation: { name: 'Settlement UI ' + role + ' ' + randomUUID(), kind: 'COMPANY' } };
  for (let attempt = 0; attempt < 2; attempt++) {
    const response = await client.post('/api/v1/auth/register', { data: input, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) } });
    if (response.status() !== 429 || attempt === 1) { expect(response.status(), await response.text()).toBe(201); return response.json(); }
    expect((await response.json()).error.code).toBe('RATE_LIMITED');
    const seconds = Number(response.headers()['retry-after']);
    expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true);
    test.setTimeout(test.info().timeout + seconds * 1000);
    await test.step('respect registration budget before one explicit retry', async () => { await delay(seconds * 1000); });
  }
  throw new Error('Registration budget exhausted');
}
async function fixture(playwright: PlaywrightWorkerArgs['playwright'], funded = true) {
  const customer = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] }, maxRedirects: 0 });
  const supplier = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] }, maxRedirects: 0 });
  const operator = await apiFor(playwright, 'operator');
  try {
    const session = await register(customer, 'customer');
    const supplierSession = await register(supplier, 'supplier');
    const own = await (await customer.get('/api/v1/organisations/me')).json() as OwnOrganisation;
    expect((await command(operator, '/api/v1/demo/accounts/' + own.account.id + '/credits', { amount_kopecks: '50000', reason: 'Isolated settlement UI fixture' })).status()).toBe(201);
    const created = await postDraft(customer, draftInput({ supplier_party_id: supplierSession.organisation!.id, amount_kopecks: '30000', title: 'Settlement UI deal ' + randomUUID() }));
    expect(created.status()).toBe(201);
    let deal = await created.json() as Deal;
    if (funded) {
      const generated = await command(customer, '/api/v1/deals/' + deal.id + '/documents/generate', { expected_deal_version: deal.deal_version, template_id: 'printing-acceptance-v1' });
      expect(generated.status()).toBe(201);
      deal = (await generated.json()).deal;
      const input = { deal_version: deal.deal_version, document_id: deal.document!.id, document_version: deal.document!.document_version, document_sha256: deal.document!.sha256 };
      expect((await command(customer, '/api/v1/deals/' + deal.id + '/agree', input)).status()).toBe(200);
      expect((await command(supplier, '/api/v1/deals/' + deal.id + '/agree', input)).status()).toBe(200);
      const reserve = await command(customer, '/api/v1/deals/' + deal.id + '/reserve', { deal_version: deal.deal_version });
      expect(reserve.status()).toBe(201);
      deal = (await reserve.json()).deal;
    }
    return { customer, supplier, operator, session, supplierSession, deal, dispose: () => Promise.all([customer.dispose(), supplier.dispose(), operator.dispose()]) };
  } catch (failure) { await Promise.all([customer.dispose(), supplier.dispose(), operator.dispose()]); throw failure; }
}
async function actorPage(browser: Browser, client: APIRequestContext) {
  const context = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: await client.storageState() });
  return { context, page: await context.newPage() };
}
async function openDeal(page: Page, id: string) {
  await page.goto('/');
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(id);
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  await page.locator('.deal-row').filter({ hasText: id }).click();
  await expect(page.getByRole('region', { name: 'Спор и отмена', exact: true })).toBeVisible();
}
async function openEDO(page: Page) {
  await page.goto('/');
  await page.getByRole('button', { name: 'События ЭДО', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'События ЭДО', exact: true })).toBeVisible();
}
async function requestCancellation(page: Page, reason: string) {
  await page.getByRole('button', { name: 'Запросить отмену', exact: true }).click();
  await page.getByLabel('Причина отмены', { exact: true }).fill(reason);
  await page.getByRole('button', { name: 'Отправить запрос отмены', exact: true }).click();
}
async function acceptCancellation(page: Page) {
  const panel = page.getByRole('region', { name: 'Спор и отмена', exact: true });
  await expect(panel.getByRole('button', { name: 'Принять отмену', exact: true })).toBeDisabled();
  await panel.getByRole('checkbox').check();
  await panel.getByRole('button', { name: 'Принять отмену', exact: true }).click();
}
async function actual(client: APIRequestContext, id: string): Promise<Deal> {
  const response = await client.get('/api/v1/deals/' + id); expect(response.status()).toBe(200); return response.json();
}
async function account(client: APIRequestContext) { return (await (await client.get('/api/v1/organisations/me')).json()).account; }
function sent(request: Request) { return { key: request.headers()['idempotency-key'], actor: request.headers()['x-expected-user-id'], body: request.postData() }; }
async function queue(page: Page, id: string, template = 'valid', replay?: string) {
  await page.getByLabel('ID сделки для события', { exact: true }).fill(id);
  await page.getByLabel('Учебный сценарий ЭДО', { exact: true }).selectOption(template);
  if (replay) await page.getByLabel('ID исходного события', { exact: true }).fill(replay);
  await page.getByRole('button', { name: 'Отправить учебное событие', exact: true }).click();
}

test('a lost cancellation request survives reload and its historical replay reads the later two-party release', async ({ browser, playwright }, testInfo) => {
  const setup = await fixture(playwright);
  const customer = await actorPage(browser, setup.customer);
  const supplier = await actorPage(browser, setup.supplier);
  try {
    await openDeal(customer.page, setup.deal.id);
    const requests: ReturnType<typeof sent>[] = [];
    let replayStatus = '';
    await customer.page.route('**/api/v1/deals/' + setup.deal.id + '/cancellation-requests', async route => {
      requests.push(sent(route.request()));
      const response = await route.fetch();
      expect(response.status()).toBe(201);
      if (requests.length === 1) await route.abort('failed');
      else { replayStatus = (await response.json()).deal.status; await route.fulfill({ response }); }
    });
    await requestCancellation(customer.page, 'Ответ не получен, отменяем по согласию');
    await expect(customer.page.getByText('Команда ожидает подтверждения: запрос отмены', { exact: true })).toBeVisible();
    const storageKey = 'poaktu:pending-agreement:' + setup.session.organisation!.id + ':' + setup.deal.id;
    const original = await customer.page.evaluate(key => sessionStorage.getItem(key), storageKey);
    expect(JSON.parse(original!)).toMatchObject({ kind: 'cancel-request', input: { deal_version: setup.deal.deal_version, reason: 'Ответ не получен, отменяем по согласию' } });
    expect(await actual(setup.customer, setup.deal.id)).toMatchObject({ status: 'CANCELLATION_PENDING', reserve: { status: 'ACTIVE' }, payment_id: null });
    expect(await account(setup.customer)).toMatchObject({ total_kopecks: '50000', reserved_kopecks: '30000', available_kopecks: '20000' });
    await customer.page.reload();
    await openDeal(customer.page, setup.deal.id);
    await expect(customer.page.getByRole('button', { name: 'Принять отмену', exact: true })).toHaveCount(0);
    expect(requests).toHaveLength(1);
    await openDeal(supplier.page, setup.deal.id);
    await supplier.page.setViewportSize({ width: 320, height: 812 });
    expect(await supplier.page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
    await acceptCancellation(supplier.page);
    await expect(supplier.page.locator('.deal-detail-heading')).toContainText('Резерв освобождён');
    await expect(supplier.page.getByText('Резерв освобождён по согласию сторон', { exact: true })).toBeVisible();
    await testInfo.attach('mobile-agreed-release', { body: await supplier.page.screenshot({ fullPage: true }), contentType: 'image/png' });
    await customer.page.getByRole('button', { name: 'Проверить результат команды', exact: true }).click();
    await expect(customer.page.locator('.deal-detail-heading')).toContainText('Резерв освобождён');
    expect(replayStatus).toBe('CANCELLATION_PENDING');
    expect(requests).toHaveLength(2); expect(requests[1]).toEqual(requests[0]);
    expect(requests[0].actor).toBe(setup.session.user.id);
    expect(await customer.page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBeNull();
    await expect(customer.page.getByRole('button', { name: 'Запросить отмену', exact: true })).toHaveCount(0);
    expect(await actual(setup.customer, setup.deal.id)).toMatchObject({ status: 'REFUNDED', reserve: { id: setup.deal.reserve!.id, status: 'RELEASED' }, cancellation_request: null, payment_id: null });
    expect(await account(setup.customer)).toMatchObject({ total_kopecks: '50000', reserved_kopecks: '0', available_kopecks: '50000' });
    expect(await account(setup.supplier)).toMatchObject({ total_kopecks: '0', reserved_kopecks: '0', available_kopecks: '0' });
  } finally { await customer.context.close(); await supplier.context.close(); await setup.dispose(); }
});

test('an independent dispute and cancellation remain visible together and only the other party releases the hold', async ({ browser, playwright }) => {
  const setup = await fixture(playwright);
  const customer = await actorPage(browser, setup.customer);
  const supplier = await actorPage(browser, setup.supplier);
  try {
    await openDeal(customer.page, setup.deal.id);
    await customer.page.getByRole('button', { name: 'Открыть спор', exact: true }).click();
    await customer.page.getByLabel('Причина спора', { exact: true }).fill('Результат работы\nнужно проверить обеим сторонам');
    await customer.page.getByRole('button', { name: 'Подтвердить открытие спора', exact: true }).click();
    await expect(customer.page.locator('.deal-detail-heading')).toContainText('Открыт спор');
    await expect(customer.page.getByRole('button', { name: 'Подписать документ в тестовом контуре', exact: true })).toHaveCount(0);
    await expect(customer.page.getByRole('region', { name: 'Спор и отмена', exact: true })).toContainText('автоматического арбитража и возобновления выплаты нет');
    await requestCancellation(customer.page, 'Согласуем освобождение резерва при споре');
    await expect(customer.page.getByText('Активный запрос отмены', { exact: true })).toBeVisible();
    await openDeal(supplier.page, setup.deal.id);
    const panel = supplier.page.getByRole('region', { name: 'Спор и отмена', exact: true });
    await expect(panel).toContainText('Результат работы');
    await expect(panel).toContainText('Согласуем освобождение резерва при споре');
    await expect(panel.getByRole('button', { name: 'Открыть спор', exact: true })).toHaveCount(0);
    await acceptCancellation(supplier.page);
    await expect(supplier.page.locator('.deal-detail-heading')).toContainText('Резерв освобождён');
    const ended = await actual(setup.customer, setup.deal.id);
    expect(ended.dispute?.reason).toBe('Результат работы\nнужно проверить обеим сторонам');
    expect(ended).toMatchObject({ payment_id: null, reserve: { status: 'RELEASED' } });
    await expect(panel).toContainText('Факт спора сохранён в завершённой сделке.');
  } finally { await customer.context.close(); await supplier.context.close(); await setup.dispose(); }
});

test('rejecting cancellation is explicit and a new request retains sticky review instead of reopening signatures', async ({ browser, playwright }) => {
  const setup = await fixture(playwright);
  const customer = await actorPage(browser, setup.customer);
  const supplier = await actorPage(browser, setup.supplier);
  try {
    await openDeal(customer.page, setup.deal.id);
    await requestCancellation(customer.page, 'Просим отменить работу');
    await expect(customer.page.getByText('Активный запрос отмены', { exact: true })).toBeVisible();
    await openDeal(supplier.page, setup.deal.id);
    await supplier.page.getByRole('button', { name: 'Отклонить отмену', exact: true }).click();
    await supplier.page.getByLabel('Причина отказа', { exact: true }).fill('Работа исполнена, нужно согласовать результат');
    await supplier.page.getByRole('button', { name: 'Подтвердить отказ от отмены', exact: true }).click();
    await expect(supplier.page.locator('.deal-detail-heading')).toContainText('Требует проверки');
    await expect(supplier.page.getByRole('region', { name: 'Спор и отмена', exact: true })).toContainText('Выплата автоматически не возобновляется.');
    await expect(supplier.page.getByRole('button', { name: 'Подписать документ в тестовом контуре', exact: true })).toHaveCount(0);
    await requestCancellation(supplier.page, 'Предлагаем согласовать новую отмену');
    await expect(supplier.page.getByText('Активный запрос отмены', { exact: true })).toBeVisible();
    await expect(supplier.page.locator('.deal-detail-heading')).toContainText('Требует проверки');
    await expect(supplier.page.getByRole('button', { name: 'Принять отмену', exact: true })).toHaveCount(0);
    const reviewed = await actual(setup.customer, setup.deal.id);
    expect(reviewed.review_reasons).toContain('CANCELLATION_REJECTED');
    expect(reviewed).toMatchObject({ status: 'REVIEW_REQUIRED', payment_id: null, reserve: { status: 'ACTIVE' }, cancellation_request: { initiator_organisation_id: setup.supplierSession.organisation!.id } });
    expect(await account(setup.customer)).toMatchObject({ total_kopecks: '50000', reserved_kopecks: '30000', available_kopecks: '20000' });
    await customer.page.locator('.deal-detail-heading').getByRole('button', { name: 'Обновить', exact: true }).click();
    await acceptCancellation(customer.page);
    await expect(customer.page.locator('.deal-detail-heading')).toContainText('Резерв освобождён');
  } finally { await customer.context.close(); await supplier.context.close(); await setup.dispose(); }
});

test('a draft cancellation needs a second explicit vote and produces CANCELLED without a reserve or payment', async ({ browser, playwright }) => {
  const setup = await fixture(playwright, false);
  const customer = await actorPage(browser, setup.customer);
  const supplier = await actorPage(browser, setup.supplier);
  try {
    const before = await account(setup.customer);
    await openDeal(customer.page, setup.deal.id);
    await expect(customer.page.getByRole('button', { name: 'Открыть спор', exact: true })).toHaveCount(0);
    await requestCancellation(customer.page, 'Условия пока не согласованы');
    await expect(customer.page.locator('.deal-detail-heading')).toContainText('Ожидает решения об отмене');
    await expect(customer.page.getByRole('button', { name: 'Принять отмену', exact: true })).toHaveCount(0);
    await openDeal(supplier.page, setup.deal.id);
    await acceptCancellation(supplier.page);
    await expect(supplier.page.locator('.deal-detail-heading')).toContainText('Сделка отменена');
    await expect(supplier.page.getByText('Сделка отменена по согласию сторон', { exact: true })).toBeVisible();
    expect(await actual(setup.customer, setup.deal.id)).toMatchObject({ status: 'CANCELLED', reserve: null, payment_id: null, cancellation_request: null });
    expect(await account(setup.customer)).toEqual(before);
    await expect(supplier.page.getByRole('button', { name: 'Запросить отмену', exact: true })).toHaveCount(0);
  } finally { await customer.context.close(); await supplier.context.close(); await setup.dispose(); }
});

test('operator delivery distinguishes rejection, the existing single payment and a conflicting replay at mobile width', async ({ browser, playwright }, testInfo) => {
  const setup = await fixture(playwright);
  const operator = await actorPage(browser, setup.operator);
  try {
    await openEDO(operator.page);
    await operator.page.setViewportSize({ width: 320, height: 812 });
    await queue(operator.page, setup.deal.id);
    await expect(operator.page.getByText(/Задание принято.*Это ещё не подтверждение выплаты/)).toBeVisible();
    await expect(operator.page.getByRole('heading', { name: 'Событие доставлено, выплата отклонена', exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(operator.page.getByRole('region', { name: 'Результат события ЭДО', exact: true })).toContainText('две действительные подписи');
    expect(await actual(setup.customer, setup.deal.id)).toMatchObject({ status: 'FUNDED', reserve: { status: 'ACTIVE' }, payment_id: null });
    const signature = { deal_version: setup.deal.deal_version, document_version: setup.deal.document!.document_version, document_sha256: setup.deal.document!.sha256 };
    for (const actor of [setup.customer, setup.supplier]) expect((await command(actor, '/api/v1/documents/' + setup.deal.document!.id + '/sign', signature)).status()).toBe(201);
    await expect.poll(async () => (await actual(setup.customer, setup.deal.id)).status, { timeout: 20_000 }).toBe('SETTLED');
    const paid = await actual(setup.customer, setup.deal.id);
    await queue(operator.page, setup.deal.id);
    await expect(operator.page.getByRole('heading', { name: 'Прежний расчёт подтверждён', exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(operator.page.getByRole('region', { name: 'Результат события ЭДО', exact: true })).toContainText(paid.payment_id!);
    const successfulEvent = await operator.page.getByLabel('ID события для проверки', { exact: true }).inputValue();
    await queue(operator.page, setup.deal.id, 'replay', successfulEvent);
    await expect(operator.page.getByRole('heading', { name: 'Прежний расчёт подтверждён', exact: true })).toBeVisible({ timeout: 20_000 });
    await queue(operator.page, setup.deal.id, 'same_id_different_body', successfulEvent);
    await expect(operator.page.getByRole('heading', { name: 'Событие доставлено, выплата отклонена', exact: true })).toBeVisible({ timeout: 20_000 });
    const result = operator.page.getByRole('region', { name: 'Результат события ЭДО', exact: true });
    await expect(result).toContainText('Этот ID события уже использован с другим содержимым.');
    await expect(result).toContainText('409');
    expect(await operator.page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
    await testInfo.attach('mobile-edo-final-rejection', { body: await operator.page.screenshot({ fullPage: true }), contentType: 'image/png' });
    expect((await actual(setup.customer, setup.deal.id)).payment_id).toBe(paid.payment_id);
    expect(await account(setup.customer)).toMatchObject({ total_kopecks: '20000', reserved_kopecks: '0', available_kopecks: '20000' });
    expect(await account(setup.supplier)).toMatchObject({ total_kopecks: '30000', reserved_kopecks: '0', available_kopecks: '30000' });
  } finally { await operator.context.close(); await setup.dispose(); }
});

test('a lost committed EDO queue command is restored without an automatic resend and repeats its exact original key', async ({ browser, playwright }) => {
  const setup = await fixture(playwright);
  const operator = await actorPage(browser, setup.operator);
  try {
    const operatorSession = await (await setup.operator.get('/api/v1/auth/me')).json() as Session;
    const requests: ReturnType<typeof sent>[] = [];
    await openEDO(operator.page);
    await operator.page.route('**/api/v1/demo/edo/events', async route => {
      requests.push(sent(route.request()));
      const response = await route.fetch(); expect(response.status()).toBe(202);
      if (requests.length === 1) await route.abort('failed');
      else await route.fulfill({ response });
    });
    await queue(operator.page, setup.deal.id);
    await expect(operator.page.getByText('Отправка ЭДО ожидает подтверждения', { exact: true })).toBeVisible();
    const storageKey = 'poaktu:pending-edo:' + operatorSession.user.id;
    const saved = await operator.page.evaluate(key => sessionStorage.getItem(key), storageKey);
    expect(JSON.parse(saved!)).toMatchObject({ userId: operatorSession.user.id, input: { deal_id: setup.deal.id, template: 'valid' } });
    await expect(operator.page.getByLabel('ID сделки для события', { exact: true })).toBeDisabled();
    await operator.page.reload(); await openEDO(operator.page);
    await expect(operator.page.getByText('Отправка ЭДО ожидает подтверждения', { exact: true })).toBeVisible();
    expect(requests).toHaveLength(1);
    await operator.page.getByRole('button', { name: 'Проверить результат отправки ЭДО', exact: true }).click();
    await expect(operator.page.getByRole('heading', { name: 'Событие доставлено, выплата отклонена', exact: true })).toBeVisible({ timeout: 20_000 });
    expect(requests).toHaveLength(2); expect(requests[1]).toEqual(requests[0]);
    expect(requests[0].actor).toBe(operatorSession.user.id);
    expect(await operator.page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBeNull();
    await expect(operator.page.getByLabel('ID сделки для события', { exact: true })).toBeEnabled();
    expect(await actual(setup.customer, setup.deal.id)).toMatchObject({ status: 'FUNDED', payment_id: null, reserve: { status: 'ACTIVE' } });
  } finally { await operator.context.close(); await setup.dispose(); }
});

test('a definitive missing-PDF queue refusal frees the form for an explicit corrected command', async ({ browser, playwright }) => {
  const setup = await fixture(playwright, false);
  const operator = await actorPage(browser, setup.operator);
  try {
    const keys: string[] = [];
    operator.page.on('request', request => { if (request.method() === 'POST' && request.url().endsWith('/demo/edo/events')) keys.push(request.headers()['idempotency-key']); });
    await openEDO(operator.page);
    await queue(operator.page, setup.deal.id);
    await expect(operator.page.getByRole('alert')).toContainText('Сначала добавьте PDF');
    await expect(operator.page.getByText('Отправка ЭДО ожидает подтверждения', { exact: true })).toHaveCount(0);
    await expect(operator.page.getByLabel('ID сделки для события', { exact: true })).toBeEnabled();
    const generated = await command(setup.customer, '/api/v1/deals/' + setup.deal.id + '/documents/generate', { expected_deal_version: setup.deal.deal_version, template_id: 'printing-acceptance-v1' });
    expect(generated.status()).toBe(201);
    await queue(operator.page, setup.deal.id);
    await expect(operator.page.getByRole('heading', { name: 'Событие доставлено, выплата отклонена', exact: true })).toBeVisible({ timeout: 20_000 });
    expect(keys).toHaveLength(2); expect(keys[0]).not.toBe(keys[1]);
    expect((await actual(setup.customer, setup.deal.id)).payment_id).toBeNull();
  } finally { await operator.context.close(); await setup.dispose(); }
});
