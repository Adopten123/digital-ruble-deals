import { randomUUID } from 'node:crypto';
import { test, expect } from '../support.ts';

test('the cabinet shows three separate balances and the catalog exposes only public fields', async ({ page, actors }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Учебный счёт' })).toBeVisible();
  await expect(page.locator('.balance-total strong')).toHaveText('500 000,00 ₽');
  await expect(page.locator('.balance-reserved strong')).toHaveText('0,00 ₽');
  await expect(page.locator('.balance-available strong')).toHaveText('500 000,00 ₽');
  await page.getByRole('button', { name: 'Контрагенты', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Каталог контрагентов' })).toBeVisible();
  const catalog = page.waitForResponse((response) => response.url().includes('/api/v1/organisations?') && new URL(response.url()).searchParams.get('q') === actors.supplier.organisation!.name);
  await page.getByLabel('Название организации', { exact: true }).fill(actors.supplier.organisation!.name);
  await page.getByLabel('Название организации', { exact: true }).press('Enter');
  const body = await (await catalog).json();
  expect(body.items.length).toBeGreaterThan(0);
  for (const organisation of body.items) expect(Object.keys(organisation).sort()).toEqual(['id', 'is_demo', 'kind', 'name']);
  await expect(page.locator('.counterparty')).toHaveCount(body.items.length);
  await expect(page.locator('.catalog-list')).not.toContainText(actors.supplier.user.email);
  await expect(page.locator('.catalog-list')).not.toContainText('Общий остаток');
  await expect(page.locator('.catalog-privacy')).toContainText('Остатки, документы и данные участников других организаций закрыты');
  await page.getByLabel('Название организации', { exact: true }).fill(`missing-${randomUUID()}`);
  await page.getByLabel('Название организации', { exact: true }).press('Enter');
  await expect(page.getByRole('heading', { name: 'Организации не найдены' })).toBeVisible();
  await page.getByLabel('Название организации', { exact: true }).fill(actors.customer.organisation!.name);
  await page.getByRole('button', { name: 'Найти', exact: true }).click();
  await expect(page.locator('.counterparty').filter({ hasText: actors.customer.organisation!.id })).toContainText('Ваша');
});

test('catalog network failure does not invent rows and retry loads the server catalog', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Учебный счёт' })).toBeVisible();
  await page.route('**/api/v1/organisations?*', async (route) => route.abort('failed'));
  await page.getByRole('button', { name: 'Контрагенты', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Нет связи с сервером');
  await expect(page.locator('.counterparty')).toHaveCount(0);
  await expect(page.getByText('Каталог не загружен. Повторите запрос после восстановления связи.')).toBeVisible();
  await page.unroute('**/api/v1/organisations?*');
  await page.getByRole('button', { name: 'Повторить запрос' }).click();
  await expect(page.locator('.counterparty').first()).toBeVisible();
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('the operator has no account or deal controls and can read the public catalog', async ({ browser, actors }) => {
  const context = await browser.newContext({ baseURL: test.info().project.use.baseURL, storageState: actors.operator.statePath });
  try {
    const page = await context.newPage();
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Панель оператора' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Отдельный доступ оператора' })).toBeVisible();
    await expect(page.locator('.balance-card')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Сделки', exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Мой счёт', exact: true })).toHaveCount(0);
    expect((await page.request.get('/api/v1/organisations/me')).status()).toBe(403);
    expect((await page.request.get('/api/v1/deals')).status()).toBe(403);
    await page.getByRole('button', { name: 'Открыть каталог' }).click();
    await expect(page.getByRole('heading', { name: 'Каталог контрагентов' })).toBeVisible();
    await expect(page.locator('.counterparty').first()).toBeVisible();
  } finally { await context.close(); }
});
