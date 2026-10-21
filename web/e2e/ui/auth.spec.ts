import { randomUUID } from 'node:crypto';
import { test, expect } from '../support.ts';

test.describe('authentication through the browser', () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test('wrong credentials recover, a real session survives reload, and logout revokes it', async ({ page, actors }) => {
    await page.goto('/');
    await expect(page.getByText('Тестовый контур. Расчёты и ЭДО эмулируются')).toBeVisible();
    await page.getByLabel('Email', { exact: true }).fill(actors.customer.user.email);
    await page.getByLabel('Пароль', { exact: true }).fill('DefinitelyWrong2026!');
    await page.getByRole('button', { name: 'Войти в кабинет' }).click();
    await expect(page.getByRole('alert')).toContainText('Проверьте email и пароль');
    await expect(page.getByLabel('Пароль', { exact: true })).toBeEnabled();
    await page.getByLabel('Пароль', { exact: true }).fill('PoaktuDemo2026!');
    await page.getByLabel('Пароль', { exact: true }).press('Enter');
    await expect(page.getByRole('heading', { name: actors.customer.organisation!.name, exact: true })).toBeVisible();
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Учебный счёт' })).toBeVisible();
    const sessionCookie = (await page.context().cookies()).find((cookie) => cookie.name === 'pa_session');
    expect(sessionCookie?.httpOnly).toBe(true);
    expect(sessionCookie?.sameSite).toBe('Lax');
    const logout = page.waitForResponse((response) => response.url().endsWith('/api/v1/auth/logout') && response.request().method() === 'POST');
    await page.getByRole('button', { name: 'Выйти', exact: true }).click();
    expect((await logout).status()).toBe(204);
    await expect(page.getByRole('heading', { name: 'Рады видеть вас' })).toBeVisible();
    expect((await page.request.get('/api/v1/auth/me')).status()).toBe(401);
    const oldSession = await page.request.get('/api/v1/auth/me', { headers: { Cookie: `pa_session=${sessionCookie!.value}` } });
    expect(oldSession.status()).toBe(401);
  });

  test('duplicate email is recoverable and a new company receives only a zero account', async ({ page, actors }) => {
    await page.goto('/');
    await page.getByRole('button', { name: 'Регистрация', exact: true }).click();
    await page.getByLabel('Email', { exact: true }).fill(actors.customer.user.email);
    await page.getByLabel('Пароль', { exact: true }).fill('SyntheticCompany2026!');
    await page.getByLabel('Название организации', { exact: true }).fill(`UI Company ${randomUUID()}`);
    await page.getByRole('button', { name: 'Создать организацию' }).click();
    await expect(page.getByRole('alert')).toContainText('Этот email уже используется');
    await page.getByLabel('Email', { exact: true }).fill(`ui-company-${randomUUID()}@demo.poaktu.local`);
    const registered = page.waitForResponse((response) => response.url().endsWith('/api/v1/auth/register') && response.request().method() === 'POST');
    await page.getByRole('button', { name: 'Создать организацию' }).click();
    const response = await registered;
    expect(response.status()).toBe(201);
    const session = await response.json();
    expect(session.organisation.kind).toBe('COMPANY');
    expect(session.user.system_role).toBe('USER');
    await expect(page.locator('.balance-card')).toHaveCount(3);
    for (const card of await page.locator('.balance-card').all()) await expect(card.locator('strong')).toHaveText('0,00 ₽');
    await page.reload();
    await expect(page.getByRole('heading', { name: session.organisation.name, exact: true })).toBeVisible();
    expect((await page.request.get('/api/v1/organisations/me')).status()).toBe(200);
  });

  test('registration counts Unicode characters and permits the documented IP limits', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: 'Регистрация', exact: true }).click();
    await page.getByLabel('Email', { exact: true }).fill(`ui-ip-${randomUUID()}@demo.poaktu.local`);
    await page.getByLabel('Форма организации').selectOption('IP');
    const password = '🔐'.repeat(128);
    const name = '🧵'.repeat(200);
    let posts = 0;
    page.on('request', (request) => { if (request.method() === 'POST' && request.url().endsWith('/auth/register')) posts += 1; });
    await page.getByLabel('Пароль', { exact: true }).fill('a'.repeat(129));
    await page.getByLabel('Название организации', { exact: true }).fill(name);
    await page.getByRole('button', { name: 'Создать организацию' }).click();
    await expect(page.getByRole('alert')).toContainText('от 12 до 128 символов');
    expect(posts).toBe(0);
    await page.getByLabel('Пароль', { exact: true }).fill(password);
    await page.getByLabel('Название организации', { exact: true }).fill('a'.repeat(201));
    await page.getByRole('button', { name: 'Создать организацию' }).click();
    await expect(page.getByRole('alert')).toContainText('до 200 символов');
    expect(posts).toBe(0);
    await page.getByLabel('Название организации', { exact: true }).fill(name);
    const registered = page.waitForResponse((response) => response.url().endsWith('/api/v1/auth/register') && response.request().method() === 'POST');
    await page.getByRole('button', { name: 'Создать организацию' }).click();
    const response = await registered;
    expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).toMatchObject({ password, organisation: { name, kind: 'IP' } });
    await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
    await expect(page.locator('.balance-card')).toHaveCount(3);
    expect(posts).toBe(1);
  });
});

test('bootstrap and readiness failures are explicit and retry recovers real data', async ({ page, actors }) => {
  await page.route('**/api/v1/auth/me', async (route) => route.fulfill({ status: 503, json: { error: { code: 'TEMPORARILY_UNAVAILABLE', message: 'Synthetic test failure' }, request_id: 'ui-bootstrap-503' } }));
  await page.route('**/readyz', async (route) => route.fulfill({ status: 503, body: '' }));
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Не удалось открыть кабинет' })).toBeVisible();
  await expect(page.getByRole('alert')).toContainText('ui-bootstrap-503');
  await expect(page.getByRole('button', { name: 'Обновить проверку готовности стенда' })).toContainText('Сервис недоступен');
  await page.unroute('**/api/v1/auth/me');
  await page.unroute('**/readyz');
  await page.getByRole('button', { name: 'Повторить запрос' }).click();
  await expect(page.getByRole('heading', { name: actors.customer.organisation!.name, exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Обновить проверку готовности стенда' })).toContainText('Сервис готов');
});

test('a protected resource returning 401 hides the cabinet and returns to login', async ({ page }) => {
  await page.route('**/api/v1/organisations/me', async (route) => route.fulfill({ status: 401, json: { error: { code: 'SESSION_EXPIRED', message: 'Synthetic test expiry' }, request_id: 'ui-expired-session' } }));
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Рады видеть вас' })).toBeVisible();
  await expect(page.getByRole('alert')).toContainText('Сессия завершилась');
  await expect(page.getByRole('button', { name: 'Выйти', exact: true })).toHaveCount(0);
  await expect(page.locator('.balance-card')).toHaveCount(0);
  // The fixture session was not revoked: failure injection must not log out other tests.
  expect((await page.request.get('/api/v1/auth/me')).status()).toBe(200);
});
