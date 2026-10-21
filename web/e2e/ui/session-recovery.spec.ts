import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import type { Page } from '@playwright/test';
import { test, expect } from '../support.ts';
import type { Session } from '../../src/lib/api.ts';

type Account = { name: string; email: string; password: string; session: Session };

async function submitAuth(page: Page, button: string, path: string, expectedStatus: number) {
  for (let attempt = 0; attempt < 3; attempt += 1) {
    const received = page.waitForResponse((response) => response.url().endsWith(path) && response.request().method() === 'POST');
    await page.getByRole('button', { name: button, exact: true }).click();
    const response = await received;
    if (response.status() !== 429 || attempt === 2) {
      expect(response.status()).toBe(expectedStatus);
      return response;
    }
    expect((await response.json()).error.code).toBe('RATE_LIMITED');
    const seconds = Number(response.headers()['retry-after']);
    expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60, 'bounded server Retry-After').toBe(true);
    await expect(page.getByRole('alert')).toContainText('Слишком много попыток');
    await test.step(`respect the shared proxy auth budget: wait ${seconds}s before explicit retry`, async () => { await delay(seconds * 1000); });
  }
  throw new Error('Authentication retry budget exhausted');
}

async function login(page: Page, account: Account) {
  await page.goto('/');
  await page.getByLabel('Email', { exact: true }).fill(account.email);
  await page.getByLabel('Пароль', { exact: true }).fill(account.password);
  await submitAuth(page, 'Войти в кабинет', '/auth/login', 200);
  await expect(page.getByRole('heading', { name: account.name, exact: true })).toBeVisible();
}

async function registerAccount(page: Page, label = 'Session owner'): Promise<Account> {
  await page.goto('/');
  await page.getByRole('button', { name: 'Регистрация', exact: true }).click();
  const name = `${label} ${randomUUID()}`;
  const email = `session-${randomUUID()}@example.test`;
  const password = 'SessionSwitchOnly2026!';
  await page.getByLabel('Email', { exact: true }).fill(email);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByLabel('Название организации', { exact: true }).fill(name);
  const response = await submitAuth(page, 'Создать организацию', '/auth/register', 201);
  await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
  return { name, email, password, session: await response.json() };
}

async function switchAccount(page: Page) {
  await page.goto('/');
  await page.getByRole('button', { name: 'Выйти', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Рады видеть вас', exact: true })).toBeVisible();
  return registerAccount(page, 'Session switch');
}

test.describe('shared cookies must not change the actor behind an old cabinet', () => {
  test.describe.configure({ timeout: 90_000 });
  test.use({ storageState: { cookies: [], origins: [] } });

  test('an old account read rechecks identity instead of mixing another account balances with its heading', async ({ page, context }, testInfo) => {
    const owner = await registerAccount(page);
    const other = await context.newPage();
    const changed = await switchAccount(other);
    const refreshed = page.waitForResponse((response) => response.url().endsWith('/organisations/me'));
    await page.getByRole('button', { name: 'Обновить', exact: true }).click();
    const response = await refreshed;
    expect(response.request().headers()['x-expected-user-id']).toBe(owner.session.user.id);
    expect(response.status()).toBe(409);
    await expect(page.getByRole('heading', { name: changed.name, exact: true })).toBeVisible();
    await expect(page.getByRole('alert')).toContainText('Аккаунт изменился');
    for (const card of await page.locator('.balance-card').all()) await expect(card.locator('strong')).toHaveText('0,00 ₽');
    await testInfo.attach('reconciled-account', { body: await page.screenshot({ fullPage: true }), contentType: 'image/png' });
  });

  test('an old draft without cached CSRF cannot be submitted as the new cookie owner', async ({ page, context, actors }) => {
    const owner = await registerAccount(page);
    await page.reload(); // A fresh API client has no cached CSRF token.
    await page.getByRole('button', { name: 'Сделки', exact: true }).click();
    await page.getByRole('button', { name: 'Новая сделка', exact: true }).click();
    await expect(page.locator('.supplier-picker').getByRole('button', { name: 'Найти', exact: true })).toBeEnabled();
    await page.getByLabel('Исполнитель', { exact: true }).fill(actors.supplier.organisation!.name);
    await page.getByLabel('Исполнитель', { exact: true }).press('Enter');
    await page.locator('.supplier-option').filter({ hasText: actors.supplier.organisation!.id }).click();
    const title = `Old account draft ${randomUUID()}`;
    await page.getByLabel('Название сделки', { exact: true }).fill(title);
    await page.getByLabel('Условия и результат работы', { exact: true }).fill('Учебная команда прежней организации не должна исполняться от имени новой.');
    await page.getByLabel('Полная сумма, ₽', { exact: true }).fill('123,45');
    await page.getByLabel('Срок сделки', { exact: true }).fill('2030-12-25T15:45');
    const other = await context.newPage();
    const changed = await switchAccount(other);
    const posted = page.waitForResponse((response) => response.url().endsWith('/api/v1/deals') && response.request().method() === 'POST');
    await page.getByRole('button', { name: 'Создать черновик', exact: true }).click();
    const response = await posted;
    expect(response.status()).toBe(409);
    expect((await response.json()).error.code).toBe('SESSION_CHANGED');
    await expect(page.getByRole('heading', { name: changed.name, exact: true })).toBeVisible();
    const list = await page.request.get('/api/v1/deals', { params: { q: title } });
    expect((await list.json()).items).toEqual([]);
    const saved = await page.evaluate((id) => sessionStorage.getItem(`poaktu:pending-deal:${id}`), owner.session.organisation!.id);
    expect(JSON.parse(saved!).input.title).toBe(title);
    await page.getByRole('button', { name: 'Сделки', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Новая сделка', exact: true })).toBeVisible();
    await expect(page.getByText('Один запрос ожидает подтверждения')).toHaveCount(0);
    expect(changed.session.organisation!.id).not.toBe(owner.session.organisation!.id);
  });

  test('logout in one tab revokes the other tab on its next protected read', async ({ page, context }) => {
    await registerAccount(page);
    const other = await context.newPage();
    await other.goto('/');
    await expect(other.locator('.balance-card')).toHaveCount(3);
    await page.getByRole('button', { name: 'Выйти', exact: true }).click();
    const rejected = other.waitForResponse((response) => response.url().endsWith('/organisations/me'));
    await other.getByRole('button', { name: 'Обновить', exact: true }).click();
    expect((await rejected).status()).toBe(401);
    for (const tab of [page, other]) {
      await tab.reload();
      await expect(tab.getByRole('heading', { name: 'Рады видеть вас', exact: true })).toBeVisible();
      await expect(tab.locator('.balance-card')).toHaveCount(0);
    }
  });

  test('a pending command remains owned by its original organisation across a cross-tab account switch', async ({ page, context, actors }) => {
    const owner = await registerAccount(page);
    await page.getByRole('button', { name: 'Сделки', exact: true }).click();
    await page.getByRole('button', { name: 'Новая сделка', exact: true }).click();
    await expect(page.locator('.supplier-picker').getByRole('button', { name: 'Найти', exact: true })).toBeEnabled();
    await page.getByLabel('Исполнитель', { exact: true }).fill(actors.supplier.organisation!.name);
    await page.getByLabel('Исполнитель', { exact: true }).press('Enter');
    await page.locator('.supplier-option').filter({ hasText: actors.supplier.organisation!.id }).click();
    const title = `Original actor pending ${randomUUID()}`;
    await page.getByLabel('Название сделки', { exact: true }).fill(title);
    await page.getByLabel('Условия и результат работы', { exact: true }).fill('Сохранённые условия и ключ остаются у исходной организации.');
    await page.getByLabel('Полная сумма, ₽', { exact: true }).fill('100,01');
    await page.getByLabel('Срок сделки', { exact: true }).fill('2030-12-25T15:45');
    const commands: { key: string | undefined; body: string | null; actor: string | undefined }[] = [];
    await page.route('**/api/v1/deals', async route => {
      if (route.request().method() !== 'POST') return route.continue();
      commands.push({ key: route.request().headers()['idempotency-key'], body: route.request().postData(), actor: route.request().headers()['x-expected-user-id'] });
      if (commands.length === 1) return route.fulfill({ status: 503, json: { error: { code: 'TEMPORARILY_UNAVAILABLE' } } });
      await route.continue();
    });
    await page.getByRole('button', { name: 'Создать черновик', exact: true }).click();
    await expect(page.getByLabel('Название сделки', { exact: true })).toBeDisabled();
    const other = await context.newPage();
    const changed = await switchAccount(other);
    const rejected = page.waitForResponse((response) => response.url().endsWith('/api/v1/deals') && response.request().method() === 'POST');
    await page.getByRole('button', { name: 'Проверить результат создания', exact: true }).click();
    expect((await rejected).status()).toBe(409);
    await expect(page.getByRole('heading', { name: changed.name, exact: true })).toBeVisible();
    expect((await (await page.request.get('/api/v1/deals', { params: { q: title } })).json()).items).toEqual([]);
    expect(commands).toHaveLength(2);
    await page.getByRole('button', { name: 'Выйти', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Рады видеть вас', exact: true })).toBeVisible();
    let loginAttempts = 0;
    await page.route('**/api/v1/auth/login', async route => {
      if (++loginAttempts === 1) return route.fulfill({ status: 429, headers: { 'Retry-After': '1' }, json: { error: { code: 'RATE_LIMITED' } } });
      await route.continue();
    });
    await login(page, owner);
    expect(loginAttempts).toBeGreaterThanOrEqual(2);
    await page.getByRole('button', { name: 'Сделки', exact: true }).click();
    await page.getByRole('button', { name: 'Продолжить создание', exact: true }).click();
    await expect(page.getByLabel('Название сделки', { exact: true })).toHaveValue(title);
    await expect(page.getByLabel('Название сделки', { exact: true })).toBeDisabled();
    expect(commands).toHaveLength(2);
    await page.getByRole('button', { name: 'Проверить результат создания', exact: true }).click();
    await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
    expect(commands).toHaveLength(3);
    expect(commands[2]).toEqual(commands[0]);
    const list = await page.request.get('/api/v1/deals', { params: { q: title } });
    const deals = (await list.json()).items;
    expect(deals).toHaveLength(1);
    expect(deals[0].customer_party_id).toBe(owner.session.organisation!.id);
  });

  test('a failed logout keeps the real session and retry revokes it on the server', async ({ page }) => {
    await registerAccount(page);
    await page.route('**/api/v1/auth/logout', route => route.fulfill({ status: 503, json: { error: { code: 'TEMPORARILY_UNAVAILABLE' }, request_id: 'logout-recovery-test' } }));
    await page.getByRole('button', { name: 'Выйти', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('logout-recovery-test');
    await expect(page.getByRole('button', { name: 'Выйти', exact: true })).toBeEnabled();
    expect((await page.request.get('/api/v1/auth/me')).status()).toBe(200);
    await page.reload();
    await expect(page.locator('.balance-card')).toHaveCount(3);
    await page.unroute('**/api/v1/auth/logout');
    await page.getByRole('button', { name: 'Выйти', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Рады видеть вас', exact: true })).toBeVisible();
    expect((await page.request.get('/api/v1/auth/me')).status()).toBe(401);
  });
});
