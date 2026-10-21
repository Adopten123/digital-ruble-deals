import { randomUUID } from 'node:crypto';
import type { Page } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, API_BASE_URL, E2E_BASE_URL } from '../support.ts';

async function openDeals(page: Page) {
  await page.goto('/');
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await expect(page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true })).toBeEnabled();
}

test('blank or one-character organisation name is explained locally and correction registers a separate company', async ({ browser }) => {
  const context = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: { cookies: [], origins: [] } });
  try {
    const page = await context.newPage();
    let posts = 0;
    page.on('request', (request) => { if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/auth/register') posts += 1; });
    await page.goto('/');
    await page.getByRole('button', { name: 'Регистрация', exact: true }).click();
    await page.getByLabel('Email', { exact: true }).fill(`blank-name-${randomUUID()}@example.test`);
    await page.getByLabel('Пароль', { exact: true }).fill('BlankNameRegression2026!');
    for (const invalidName of ['   ', 'Я', '🧪']) {
      await page.getByLabel('Название организации', { exact: true }).fill(invalidName);
      await page.getByRole('button', { name: 'Создать организацию', exact: true }).click();
      await expect(page.getByRole('alert')).toContainText('Название организации должно содержать от 2 до 200 символов.');
      expect(posts).toBe(0);
    }
    await expect(page.getByLabel('Название организации', { exact: true })).toBeEnabled();

    const name = `Исправленное учебное название ${randomUUID()}`;
    await page.getByLabel('Название организации', { exact: true }).fill(`  ${name}  `);
    const registration = page.waitForResponse((response) => response.request().method() === 'POST'
      && new URL(response.url()).pathname === '/api/v1/auth/register');
    await page.getByRole('button', { name: 'Создать организацию', exact: true }).click();
    const response = await registration;
    expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).toMatchObject({ organisation: { name, kind: 'COMPANY' } });
    await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
    await expect(page.locator('.balance-card')).toHaveCount(3);
    for (const card of await page.locator('.balance-card').all()) await expect(card.locator('strong')).toHaveText('0,00 ₽');
    expect(posts).toBe(1);
  } finally { await context.close(); }
});

test('Enter in the supplier picker searches without submitting the enclosing draft form', async ({ page, actors }) => {
  await openDeals(page);
  await page.getByRole('button', { name: 'Новая сделка', exact: true }).click();
  await expect(page.locator('.supplier-picker').getByRole('button', { name: 'Найти', exact: true })).toBeEnabled();
  await page.getByLabel('Название сделки', { exact: true }).fill(`Keyboard picker ${randomUUID()}`);
  await page.getByLabel('Условия и результат работы', { exact: true }).fill('Учебные условия для проверки поиска с клавиатуры.');
  await page.getByLabel('Полная сумма, ₽', { exact: true }).fill('100,01');
  await page.getByLabel('Срок сделки', { exact: true }).fill('2030-12-25T15:45');
  let posts = 0;
  page.on('request', (request) => { if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/deals') posts += 1; });
  const supplier = actors.supplier.organisation!;
  const response = page.waitForResponse((value) => new URL(value.url()).pathname === '/api/v1/organisations'
    && new URL(value.url()).searchParams.get('q') === supplier.name);
  await page.getByLabel('Исполнитель', { exact: true }).fill(supplier.name);
  await page.getByLabel('Исполнитель', { exact: true }).press('Enter');
  expect((await response).status()).toBe(200);
  await expect(page.locator('.supplier-option').filter({ hasText: supplier.id })).toBeVisible();
  await expect(page.getByRole('alert')).toHaveCount(0);
  expect(posts).toBe(0);
});

test('an invalid date period clears the old result and requires correction or reset', async ({ page, playwright }) => {
  const title = `Invalid date period ${randomUUID()}`;
  const request = await apiFor(playwright, 'customer');
  try { expect((await postDraft(request, draftInput({ title }))).status()).toBe(201); }
  finally { await request.dispose(); }
  await openDeals(page);
  const search = page.getByRole('search', { name: 'Поиск сделок' });
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(title);
  await search.getByRole('button', { name: 'Найти', exact: true }).click();
  await expect(page.locator('.deal-row').filter({ hasText: title })).toHaveCount(1);
  await page.getByLabel('Создана с', { exact: true }).fill('2040-01-01');
  await page.getByLabel('По дату включительно', { exact: true }).fill('2020-01-01');
  await search.getByRole('button', { name: 'Найти', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Начало периода не может быть позже');
  await expect(page.locator('.deal-row')).toHaveCount(0);
  await expect(page.locator('.deal-empty')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Повторить запрос', exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Показать ещё', exact: true })).toHaveCount(0);
  await expect(page.locator('.deal-list-caption').getByRole('button', { name: 'Обновить', exact: true })).toBeDisabled();

  await page.getByLabel('Создана с', { exact: true }).fill('2020-01-01');
  await page.getByLabel('По дату включительно', { exact: true }).fill('2040-01-01');
  await search.getByRole('button', { name: 'Найти', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.locator('.deal-row').filter({ hasText: title })).toHaveCount(1);

  await page.getByLabel('Создана с', { exact: true }).fill('2041-01-01');
  await search.getByRole('button', { name: 'Найти', exact: true }).click();
  await expect(page.getByRole('alert')).toBeVisible();
  await page.getByRole('button', { name: 'Сбросить фильтры', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.getByLabel('Поиск по названию, ID или контрагенту')).toHaveValue('');
  await expect(page.getByLabel('Создана с', { exact: true })).toHaveValue('');
  await expect(page.getByLabel('По дату включительно', { exact: true })).toHaveValue('');
  await expect(page.locator('.deal-list-caption').getByRole('button', { name: 'Обновить', exact: true })).toBeEnabled();
  await expect(page.locator('.deal-row').filter({ hasText: title })).toHaveCount(1);
});

test('all search fields preserve and submit a complete 200-code-point Unicode name', async ({ page, playwright }) => {
  const name = `${'🧪'.repeat(164)}${randomUUID()}`;
  expect([...name]).toHaveLength(200);
  const registration = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  let organisationId: string;
  try {
    const response = await registration.post('/api/v1/auth/register', {
      headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(registration) },
      data: { email: `unicode-search-${randomUUID()}@example.test`, password: 'UnicodeSearchTest2026!', organisation: { name, kind: 'COMPANY' } },
    });
    expect(response.status()).toBe(201);
    organisationId = (await response.json()).organisation.id;
  } finally { await registration.dispose(); }
  const customer = await apiFor(playwright, 'customer');
  try { expect((await postDraft(customer, draftInput({ title: name, supplier_party_id: organisationId }))).status()).toBe(201); }
  finally { await customer.dispose(); }

  await page.goto('/');
  await page.getByRole('button', { name: 'Контрагенты', exact: true }).click();
  await expect(page.getByRole('search').getByRole('button', { name: 'Найти', exact: true })).toBeEnabled();
  const catalogResponse = page.waitForResponse((value) => new URL(value.url()).pathname === '/api/v1/organisations'
    && new URL(value.url()).searchParams.get('q') === name);
  await page.getByLabel('Название организации', { exact: true }).fill(name);
  await expect(page.getByLabel('Название организации', { exact: true })).toHaveValue(name);
  await page.getByLabel('Название организации', { exact: true }).press('Enter');
  expect((await catalogResponse).status()).toBe(200);
  await expect(page.locator('.counterparty').filter({ hasText: organisationId })).toHaveCount(1);

  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  const search = page.getByRole('search', { name: 'Поиск сделок' });
  await expect(search.getByRole('button', { name: 'Найти', exact: true })).toBeEnabled();
  const dealsResponse = page.waitForResponse((value) => new URL(value.url()).pathname === '/api/v1/deals'
    && new URL(value.url()).searchParams.get('q') === name);
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(name);
  await expect(page.getByLabel('Поиск по названию, ID или контрагенту')).toHaveValue(name);
  await search.getByRole('button', { name: 'Найти', exact: true }).click();
  expect((await dealsResponse).status()).toBe(200);
  await expect(page.locator('.deal-row').filter({ hasText: name })).toHaveCount(1);

  await page.getByRole('button', { name: 'Новая сделка', exact: true }).click();
  await expect(page.locator('.supplier-picker').getByRole('button', { name: 'Найти', exact: true })).toBeEnabled();
  const pickerResponse = page.waitForResponse((value) => new URL(value.url()).pathname === '/api/v1/organisations'
    && new URL(value.url()).searchParams.get('q') === name);
  await page.getByLabel('Исполнитель', { exact: true }).fill(name);
  await expect(page.getByLabel('Исполнитель', { exact: true })).toHaveValue(name);
  await page.getByLabel('Исполнитель', { exact: true }).press('Enter');
  expect((await pickerResponse).status()).toBe(200);
  await page.locator('.supplier-option').filter({ hasText: organisationId }).click();
  await expect(page.locator('.selected-supplier')).toContainText(organisationId);
});
