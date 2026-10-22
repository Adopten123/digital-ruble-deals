import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import type { APIRequestContext, Browser, BrowserContext, Page, PlaywrightWorkerArgs, Request } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, API_BASE_URL, E2E_BASE_URL } from '../support.ts';
import type { Deal, OwnOrganisation, Session } from '../../src/lib/api.ts';

test.describe.configure({ timeout: 90_000 });
async function command(client: APIRequestContext, path: string, input: unknown) {
  return client.post(path, { data: input, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': randomUUID() } });
}
async function register(client: APIRequestContext, name: string): Promise<Session> {
  const input = { email: `reserve-ui-${randomUUID()}@example.test`, password: 'ReserveUiOnly2026!', organisation: { name, kind: 'COMPANY' } };
  for (let attempt = 0; attempt < 2; attempt++) {
    const response = await client.post('/api/v1/auth/register', { data: input, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) } });
    if (response.status() !== 429 || attempt === 1) { expect(response.status(), await response.text()).toBe(201); return response.json(); }
    expect((await response.json()).error.code).toBe('RATE_LIMITED');
    const seconds = Number(response.headers()['retry-after']);
    expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true);
    await test.step(`respect proxy registration budget: wait ${seconds}s before explicit retry`, async () => { await delay(seconds * 1000); });
  }
  throw new Error('Registration retry budget exhausted');
}
async function fixture(playwright: PlaywrightWorkerArgs['playwright'], supplierId: string, funds = '50000000', amount = '30000000', name = `Reserve UI ${randomUUID()}`) {
  const customer = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] }, maxRedirects: 0 });
  const supplier = await apiFor(playwright, 'supplier');
  const operator = await apiFor(playwright, 'operator');
  try {
    const session = await register(customer, name);
    const own = await (await customer.get('/api/v1/organisations/me')).json() as OwnOrganisation;
    if (funds !== '0') expect((await command(operator, `/api/v1/demo/accounts/${own.account.id}/credits`, { amount_kopecks: funds, reason: 'Isolated UI fixture funding' })).status()).toBe(201);
    const created = await postDraft(customer, draftInput({ supplier_party_id: supplierId, amount_kopecks: amount, title: `Reserve deal ${randomUUID()}` }));
    expect(created.status()).toBe(201);
    const initial = await created.json() as Deal;
    const generated = await command(customer, `/api/v1/deals/${initial.id}/documents/generate`, { expected_deal_version: 1, template_id: 'printing-acceptance-v1' });
    expect(generated.status()).toBe(201);
    const draft = (await generated.json()).deal as Deal;
    const context = { deal_version: draft.deal_version, document_id: draft.document!.id, document_version: draft.document!.document_version, document_sha256: draft.document!.sha256 };
    expect((await command(customer, `/api/v1/deals/${draft.id}/agree`, context)).status()).toBe(200);
    expect((await command(supplier, `/api/v1/deals/${draft.id}/agree`, context)).status()).toBe(200);
    const deal = await (await customer.get(`/api/v1/deals/${draft.id}`)).json() as Deal;
    expect(deal.status).toBe('AGREED');
    return { customer, supplier, operator, session, accountId: own.account.id, deal, dispose: () => Promise.all([customer.dispose(), supplier.dispose(), operator.dispose()]) };
  } catch (failure) { await Promise.all([customer.dispose(), supplier.dispose(), operator.dispose()]); throw failure; }
}
async function customerPage(browser: Browser, client: APIRequestContext): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: await client.storageState() });
  return { context, page: await context.newPage() };
}
async function openDeal(page: Page, id: string) {
  await page.goto('/');
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(id);
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  await page.locator('.deal-row').filter({ hasText: id }).click();
  await expect(page.getByRole('region', { name: 'Резерв сделки' })).toBeVisible();
}
async function operatorPage(browser: Browser, statePath: string) {
  const context = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: statePath });
  const page = await context.newPage();
  await page.goto('/');
  await page.getByRole('button', { name: 'Учебные счета', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Учебные счета', exact: true })).toBeVisible();
  return { context, page };
}
async function findAccount(page: Page, id: string) {
  await page.getByLabel('Счёт или организация', { exact: true }).fill(id);
  await page.getByRole('search', { name: 'Поиск учебных счетов' }).getByRole('button', { name: 'Найти', exact: true }).click();
  await page.locator('.demo-account-row').filter({ hasText: id }).click();
}
function sent(request: Request) { return { key: request.headers()['idempotency-key'], actor: request.headers()['x-expected-user-id'], body: request.postData() }; }

test('the customer reserves exactly the full agreed amount and both parties see FUNDED without a payout', async ({ browser, playwright, actors }, testInfo) => {
  const setup = await fixture(playwright, actors.supplier.organisation!.id);
  const customer = await customerPage(browser, setup.customer);
  const supplierContext = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: actors.supplier.statePath });
  try {
    await openDeal(customer.page, setup.deal.id);
    await expect(customer.page.getByRole('button', { name: 'Учебные счета', exact: true })).toHaveCount(0);
    await expect(customer.page.locator('.deal-reserve .reserve-balances')).toContainText('500 000,00');
    const reserved = customer.page.waitForResponse(response => response.url().endsWith(`/deals/${setup.deal.id}/reserve`));
    await customer.page.getByRole('button', { name: 'Зарезервировать всю сумму', exact: true }).click();
    expect((await reserved).status()).toBe(201);
    await expect(customer.page.getByText('Сумма зарезервирована', { exact: true })).toBeVisible();
    await expect(customer.page.locator('.deal-detail-heading')).toContainText('Средства зарезервированы');
    await expect(customer.page.locator('.deal-reserve .reserve-balances')).toContainText('200 000,00');
    await expect(customer.page.getByRole('button', { name: 'Зарезервировать всю сумму', exact: true })).toHaveCount(0);
    await expect(customer.page.getByRole('button', { name: 'Вернуть на доработку', exact: true })).toHaveCount(0);
    const actual = await (await setup.customer.get(`/api/v1/deals/${setup.deal.id}`)).json();
    expect(actual).toMatchObject({ status: 'FUNDED', deal_version: setup.deal.deal_version, reserve: { amount_kopecks: '30000000', status: 'ACTIVE' }, payment_id: null });
    const supplier = await supplierContext.newPage();
    await openDeal(supplier, setup.deal.id);
    await expect(supplier.getByText('Сумма зарезервирована', { exact: true })).toBeVisible();
    await expect(supplier.getByRole('button', { name: 'Зарезервировать всю сумму', exact: true })).toHaveCount(0);
    await expect(supplier.getByRole('button', { name: 'Загрузить PDF', exact: true })).toHaveCount(0);
    await customer.page.getByRole('button', { name: 'Мой счёт', exact: true }).click();
    await expect(customer.page.locator('.balance-total>strong')).toHaveText('500 000,00 ₽');
    await expect(customer.page.locator('.balance-reserved>strong')).toHaveText('300 000,00 ₽');
    await expect(customer.page.locator('.balance-available>strong')).toHaveText('200 000,00 ₽');
    await supplier.setViewportSize({ width: 375, height: 812 });
    expect(await supplier.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
    await testInfo.attach('mobile-funded-supplier', { body: await supplier.screenshot({ fullPage: true }), contentType: 'image/png' });
  } finally { await customer.context.close(); await supplierContext.close(); await setup.dispose(); }
});

test('insufficient funds is definitive and a later credit permits a fresh command despite an older insufficient preview', async ({ browser, playwright, actors }) => {
  const setup = await fixture(playwright, actors.supplier.organisation!.id, '10000000');
  const { context, page } = await customerPage(browser, setup.customer);
  try {
    await openDeal(page, setup.deal.id);
    const commands: ReturnType<typeof sent>[] = [];
    page.on('request', request => { if (request.method() === 'POST' && request.url().endsWith(`/deals/${setup.deal.id}/reserve`)) commands.push(sent(request)); });
    await expect(page.getByText(/По текущему снимку доступной суммы недостаточно/)).toBeVisible();
    const refused = page.waitForResponse(response => response.url().endsWith(`/deals/${setup.deal.id}/reserve`));
    await page.getByRole('button', { name: 'Зарезервировать всю сумму', exact: true }).click();
    expect((await refused).status()).toBe(422);
    await expect(page.getByRole('alert')).toContainText('Недостаточно доступных тестовых средств');
    const storageKey = `poaktu:pending-agreement:${setup.session.organisation!.id}:${setup.deal.id}`;
    expect(await page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBeNull();
    expect((await (await setup.customer.get(`/api/v1/deals/${setup.deal.id}`)).json()).reserve).toBeNull();
    expect((await command(setup.operator, `/api/v1/demo/accounts/${setup.accountId}/credits`, { amount_kopecks: '20000000', reason: 'Top up isolated UI account after a definitive refusal' })).status()).toBe(201);
    // The preview is informational; it cannot veto a new command after funds changed elsewhere.
    await expect(page.getByRole('button', { name: 'Зарезервировать всю сумму', exact: true })).toBeEnabled();
    await page.getByRole('button', { name: 'Зарезервировать всю сумму', exact: true }).click();
    await expect(page.getByText('Сумма зарезервирована', { exact: true })).toBeVisible();
    expect(commands).toHaveLength(2);
    expect(commands[1].key).not.toBe(commands[0].key);
    expect(commands[1].body).toBe(commands[0].body);
  } finally { await context.close(); await setup.dispose(); }
});

test('a lost reserve reply after commit survives reload and replays the same version, actor and command key', async ({ browser, playwright, actors }) => {
  const setup = await fixture(playwright, actors.supplier.organisation!.id);
  const { context, page } = await customerPage(browser, setup.customer);
  try {
    await openDeal(page, setup.deal.id);
    const commands: ReturnType<typeof sent>[] = [];
    let readsAfterReplay = 0;
    page.on('request', request => { if (commands.length === 2 && request.method() === 'GET' && request.url().endsWith(`/deals/${setup.deal.id}`)) readsAfterReplay++; });
    await page.route(`**/api/v1/deals/${setup.deal.id}/reserve`, async route => {
      commands.push(sent(route.request()));
      if (commands.length === 1) { const committed = await route.fetch(); expect(committed.status()).toBe(201); await route.abort('failed'); }
      else await route.continue();
    });
    await page.getByRole('button', { name: 'Зарезервировать всю сумму', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('Нет связи с сервером');
    await expect(page.getByText(/Результат резервирования уточняется/)).toBeVisible();
    const storageKey = `poaktu:pending-agreement:${setup.session.organisation!.id}:${setup.deal.id}`;
    const saved = await page.evaluate(key => sessionStorage.getItem(key), storageKey);
    expect(JSON.parse(saved!).input).toEqual({ deal_version: setup.deal.deal_version });
    await page.reload();
    await openDeal(page, setup.deal.id);
    await expect(page.getByText('Команда ожидает подтверждения: резервирование средств', { exact: true })).toBeVisible();
    expect(commands).toHaveLength(1);
    await page.getByRole('button', { name: 'Проверить результат команды', exact: true }).click();
    await expect(page.getByText('Команда подтверждена сервером.', { exact: true })).toBeVisible();
    await expect.poll(() => readsAfterReplay).toBeGreaterThan(0);
    expect(commands).toHaveLength(2);
    expect(commands[1]).toEqual(commands[0]);
    expect(commands[0].actor).toBe(setup.session.user.id);
    expect(await page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBeNull();
    const own = await (await setup.customer.get('/api/v1/organisations/me')).json();
    expect(own.account).toMatchObject({ total_kopecks: '50000000', reserved_kopecks: '30000000', available_kopecks: '20000000' });
  } finally { await context.close(); await setup.dispose(); }
});

test('operator search and real pagination work; a concurrent reserve defeats a stale debit preview', async ({ browser, playwright, actors }, testInfo) => {
  const prefix = `Operator UI ${randomUUID()}`;
  const setup = await fixture(playwright, actors.supplier.organisation!.id, '0', '60000', `${prefix} A`);
  const other = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  const { context, page } = await operatorPage(browser, actors.operator.statePath);
  try {
    await register(other, `${prefix} B`);
    // Real server pages, restricted to one fixture row per page without creating 21 extra registrations.
    await page.route('**/api/v1/demo/accounts?*', async route => {
      const url = new URL(route.request().url());
      if (url.searchParams.get('q') !== prefix) return route.continue();
      expect(url.searchParams.get('limit')).toBe('20');
      url.searchParams.set('limit', '1');
      await route.fulfill({ response: await route.fetch({ url: url.toString() }) });
    });
    await page.getByLabel('Счёт или организация', { exact: true }).fill(prefix);
    await page.getByRole('search', { name: 'Поиск учебных счетов' }).getByRole('button', { name: 'Найти', exact: true }).click();
    await expect(page.locator('.demo-account-row')).toHaveCount(1);
    await page.getByRole('button', { name: 'Показать ещё счета', exact: true }).click();
    await expect(page.locator('.demo-account-row')).toHaveCount(2);
    await page.locator('.demo-account-row').filter({ hasText: setup.accountId }).click();
    await page.getByLabel('Сумма операции, ₽', { exact: true }).fill('1000,00');
    await page.getByLabel('Основание операции', { exact: true }).fill('Учебное зачисление из интерфейса.');
    await page.getByRole('button', { name: 'Зачислить тестовые средства', exact: true }).click();
    await expect(page.getByText(/Учебная операция подтверждена/)).toBeVisible();
    await expect(page.locator('.demo-money-panel .reserve-balances')).toContainText('1 000,00');
    expect((await command(setup.customer, `/api/v1/deals/${setup.deal.id}/reserve`, { deal_version: setup.deal.deal_version })).status()).toBe(201);
    await page.getByLabel('Операция с тестовыми средствами', { exact: true }).selectOption('debits');
    await page.getByLabel('Сумма операции, ₽', { exact: true }).fill('500,00');
    await page.getByLabel('Основание операции', { exact: true }).fill('Списание после конкурентного резерва: сервер должен защитить резерв.');
    const refused = page.waitForResponse(response => response.url().endsWith(`/demo/accounts/${setup.accountId}/debits`));
    await page.getByRole('button', { name: 'Списать доступные средства', exact: true }).click();
    expect((await refused).status()).toBe(422);
    await expect(page.getByRole('alert')).toContainText('активные резервы нельзя расходовать');
    await expect(page.locator('.demo-money-panel .reserve-balances')).toContainText('400,00');
    await expect(page.locator('.demo-money-panel .reserve-balances')).toContainText('600,00');
    await page.getByLabel('Сумма операции, ₽', { exact: true }).fill('400,00');
    await page.getByRole('button', { name: 'Списать доступные средства', exact: true }).click();
    await expect(page.getByText(/Учебная операция подтверждена/)).toBeVisible();
    const own = await (await setup.customer.get('/api/v1/organisations/me')).json();
    expect(own.account).toMatchObject({ total_kopecks: '60000', reserved_kopecks: '60000', available_kopecks: '0' });
    await expect(page.locator('.demo-money-panel .reserve-balances>div').last().locator('dd')).toHaveText('0,00 ₽');
    await page.setViewportSize({ width: 375, height: 812 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
    await testInfo.attach('mobile-operator-reserved-balance', { body: await page.screenshot({ fullPage: true }), contentType: 'image/png' });
  } finally { await context.close(); await other.dispose(); await setup.dispose(); }
});

test('a lost operator credit survives reload and the same replay reads current balances after another real credit', async ({ browser, playwright, actors }) => {
  const setup = await fixture(playwright, actors.supplier.organisation!.id, '0', '1');
  const { context, page } = await operatorPage(browser, actors.operator.statePath);
  try {
    await findAccount(page, setup.accountId);
    const commands: ReturnType<typeof sent>[] = [];
    await page.route(`**/api/v1/demo/accounts/${setup.accountId}/credits`, async route => {
      commands.push(sent(route.request()));
      if (commands.length === 1) { const committed = await route.fetch(); expect(committed.status()).toBe(201); await route.abort('failed'); }
      else await route.continue();
    });
    await page.getByLabel('Сумма операции, ₽', { exact: true }).fill('100,01');
    await page.getByLabel('Основание операции', { exact: true }).fill('Сохранённое основание не должно поменяться после потери ответа.');
    await page.getByRole('button', { name: 'Зачислить тестовые средства', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('Нет связи с сервером');
    await expect(page.getByLabel('Сумма операции, ₽', { exact: true })).toBeDisabled();
    const storageKey = `poaktu:pending-demo-money:${actors.operator.user.id}:${setup.accountId}:credits`;
    expect(JSON.parse((await page.evaluate(key => sessionStorage.getItem(key), storageKey))!).input.amount_kopecks).toBe('10001');
    expect((await command(setup.operator, `/api/v1/demo/accounts/${setup.accountId}/credits`, { amount_kopecks: '1', reason: 'Separate later committed credit' })).status()).toBe(201);
    await page.reload();
    await page.getByRole('button', { name: 'Учебные счета', exact: true }).click();
    await expect(page.getByText('Операция ожидает подтверждения', { exact: true })).toBeVisible();
    expect(commands).toHaveLength(1);
    await page.getByRole('button', { name: 'Проверить результат операции', exact: true }).click();
    await expect(page.getByText(/Учебная операция подтверждена/)).toBeVisible();
    await expect(page.locator('.demo-money-panel .reserve-balances')).toContainText('100,02');
    expect(commands).toHaveLength(2);
    expect(commands[1]).toEqual(commands[0]);
    expect(commands[0].actor).toBe(actors.operator.user.id);
    expect(await page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBeNull();
    expect((await (await setup.customer.get('/api/v1/organisations/me')).json()).account.total_kopecks).toBe('10002');
  } finally { await context.close(); await setup.dispose(); }
});
