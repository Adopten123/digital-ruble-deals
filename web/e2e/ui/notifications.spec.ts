import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import type { APIRequestContext, Browser, Page, PlaywrightWorkerArgs } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, API_BASE_URL, E2E_BASE_URL, loadActors } from '../support.ts';
import type { Deal, NotificationPage, Session } from '../../src/lib/api.ts';

test.describe.configure({ timeout: 90_000 });
async function register(client: APIRequestContext): Promise<Session> {
  const input = { email: 'notifications-' + randomUUID() + '@example.test', password: 'NotificationUiOnly2026!', organisation: { name: 'Уведомления UI ' + randomUUID(), kind: 'COMPANY' } };
  for (let attempt = 0; attempt < 2; attempt++) {
    const response = await client.post('/api/v1/auth/register', { data: input, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) } });
    if (response.status() !== 429 || attempt === 1) { expect(response.status(), await response.text()).toBe(201); return response.json(); }
    expect((await response.json()).error.code).toBe('RATE_LIMITED'); const seconds = Number(response.headers()['retry-after']);
    expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true); test.setTimeout(test.info().timeout + seconds * 1000); await delay(seconds * 1000);
  }
  throw new Error('Registration budget exhausted');
}
async function notices(client: APIRequestContext, unread?: boolean): Promise<NotificationPage> {
  const response = await client.get('/api/v1/notifications', { params: { limit: '100', ...(unread === undefined ? {} : { unread: String(unread) }) } });
  expect(response.status()).toBe(200); return response.json();
}
async function delivered(client: APIRequestContext, count: number) { await expect.poll(async () => (await notices(client)).items.length, { timeout: 15_000 }).toBe(count); }
async function fixture(playwright: PlaywrightWorkerArgs['playwright'], browser: Browser) {
  const client = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  try {
    const session = await register(client); const input = draftInput({ title: 'Уведомление ' + randomUUID(), amount_kopecks: '100' }); const key = randomUUID();
    const response = await postDraft(client, input, key); expect(response.status()).toBe(201); const deal = await response.json() as Deal;
    await delivered(client, 1);
    const context = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: await client.storageState() }); const page = await context.newPage();
    return { client, session, input, key, deal, context, page, dispose: () => Promise.all([context.close(), client.dispose()]) };
  } catch (error) { await client.dispose(); throw error; }
}
async function open(page: Page) {
  await page.goto('/'); await page.getByRole('button', { name: 'Уведомления', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Уведомления организации', exact: true }).locator('.notification-item').first()).toBeVisible();
}
const panel = (page: Page) => page.getByRole('region', { name: 'Уведомления организации', exact: true });
async function readRequest(client: APIRequestContext, id: string) {
  return client.post(`/api/v1/notifications/${id}/read`, { headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) } });
}

test('delivered facts have distinct times, persist read state and open the actual protected deal', async ({ browser, playwright }) => {
  const setup = await fixture(playwright, browser);
  try {
    await open(setup.page); const region = panel(setup.page); const item = region.locator('.notification-item').first();
    await expect(item).toContainText('Сделка создана'); await expect(item).toContainText('Событие сохранено'); await expect(item).toContainText('Доставлено в кабинет');
    const source = (await notices(setup.client)).items[0];
    expect(await item.locator('time').evaluateAll(elements => elements.map(element => element.getAttribute('datetime')))).toEqual([source.event.created_at, source.created_at]);
    await expect(item.locator('.notification-reference')).toBeHidden(); await expect(region).not.toContainText(setup.session.user.email);
    await item.getByRole('button', { name: 'Отметить прочитанным', exact: true }).click();
    await expect(item).toContainText('Прочитано'); await expect(region.locator('.notifications-summary')).toContainText('Непрочитанных: 0');
    await region.getByRole('button', { name: 'Непрочитанные', exact: true }).click(); await expect(region.locator('.notifications-empty')).toHaveText('Непрочитанных уведомлений нет.');
    await region.getByRole('button', { name: 'Прочитанные', exact: true }).click(); await expect(region.locator('.notification-item')).toHaveCount(1);
    const readAt = (await notices(setup.client)).items[0].read_at; expect(readAt).not.toBeNull();
    await setup.page.reload(); await setup.page.getByRole('button', { name: 'Уведомления', exact: true }).click(); await expect(panel(setup.page)).toContainText('Прочитано');
    expect((await notices(setup.client)).items[0].read_at).toBe(readAt);
    await panel(setup.page).getByRole('button', { name: 'Открыть сделку', exact: true }).click(); await expect(setup.page.getByRole('heading', { name: setup.deal.title, exact: true })).toBeVisible();
  } finally { await setup.dispose(); }
});

test('real notifications paginate once, retrying a source command does not duplicate delivery and read resets its filter page', async ({ browser, playwright }) => {
  const setup = await fixture(playwright, browser);
  try {
    for (let index = 0; index < 22; index++) expect((await postDraft(setup.client, { ...setup.input, title: 'Страница уведомлений ' + index + ' ' + randomUUID() })).status()).toBe(201);
    expect((await postDraft(setup.client, setup.input, setup.key)).status()).toBe(201); await delivered(setup.client, 23);
    await open(setup.page); const region = panel(setup.page); await expect(region.locator('.notification-item')).toHaveCount(20);
    await region.getByRole('button', { name: 'Показать ещё уведомления', exact: true }).click(); await expect(region.locator('.notification-item')).toHaveCount(23);
    const ids = await region.locator('.notification-item').evaluateAll(elements => elements.map(element => element.getAttribute('data-notification-id'))); expect(new Set(ids).size).toBe(23);
    await region.getByRole('button', { name: 'Непрочитанные', exact: true }).click(); await expect(region.locator('.notification-item')).toHaveCount(20);
    await region.locator('.notification-item').first().getByRole('button', { name: 'Отметить прочитанным', exact: true }).click();
    await expect(region.locator('.notifications-summary')).toContainText('Непрочитанных: 22'); await expect(region.locator('.notification-item')).toHaveCount(20);
    await region.getByRole('button', { name: 'Показать ещё уведомления', exact: true }).click(); await expect(region.locator('.notification-item')).toHaveCount(22);
    await region.getByRole('button', { name: 'Прочитанные', exact: true }).click(); await expect(region.locator('.notification-item')).toHaveCount(1);
  } finally { await setup.dispose(); }
});

test('a lost read response is checked by repeating the same committed read without changing its first timestamp', async ({ browser, playwright }) => {
  const setup = await fixture(playwright, browser); let committedAt: string | null = null; const keys: (string | undefined)[] = [];
  try {
    await open(setup.page); const id = (await notices(setup.client)).items[0].id; let first = true;
    await setup.page.route(`**/api/v1/notifications/${id}/read`, async route => {
      keys.push(route.request().headers()['idempotency-key']); expect(route.request().postData()).toBeNull();
      const response = await route.fetch(); expect(response.status()).toBe(204);
      if (first) { first = false; committedAt = (await notices(setup.client)).items[0].read_at; expect(committedAt).not.toBeNull(); await route.abort('failed'); } else await route.fulfill({ response });
    });
    await panel(setup.page).getByRole('button', { name: 'Отметить прочитанным', exact: true }).click();
    await expect(panel(setup.page).getByRole('button', { name: 'Проверить отметку о прочтении', exact: true })).toBeVisible();
    await panel(setup.page).getByRole('button', { name: 'Проверить отметку о прочтении', exact: true }).click(); await expect(panel(setup.page)).toContainText('Прочитано');
    expect((await notices(setup.client)).items[0].read_at).toBe(committedAt); expect(keys).toEqual([undefined, undefined]);
  } finally { await setup.dispose(); }
});

test('a delayed real all-items response cannot replace the newer read-only filter', async ({ browser, playwright }) => {
  const setup = await fixture(playwright, browser); let release!: () => void; const held = new Promise<void>(resolve => { release = resolve; }); let began!: () => void; const started = new Promise<void>(resolve => { began = resolve; });
  try {
    await open(setup.page); let delayed = false;
    await setup.page.route('**/api/v1/notifications?*', async route => {
      const query = new URL(route.request().url()).searchParams;
      if (query.has('unread') || query.get('limit') !== '20' || delayed) { await route.continue(); return; }
      delayed = true; const response = await route.fetch(); expect(response.status()).toBe(200); began(); await held; await route.fulfill({ response });
    });
    await panel(setup.page).getByRole('button', { name: 'Обновить уведомления', exact: true }).click(); await started;
    await panel(setup.page).getByRole('button', { name: 'Прочитанные', exact: true }).click(); await expect(panel(setup.page).locator('.notifications-empty')).toBeVisible();
    release(); await setup.page.unrouteAll({ behavior: 'wait' }); await expect(panel(setup.page).locator('.notification-item')).toHaveCount(0);
  } finally { release?.(); await setup.dispose(); }
});

test('timer and focus update real delivery while a busy poll is never overlapped', async ({ browser, playwright }) => {
  const setup = await fixture(playwright, browser); let release!: () => void; const held = new Promise<void>(resolve => { release = resolve; }); let began!: () => void; const started = new Promise<void>(resolve => { began = resolve; });
  try {
    await setup.page.clock.install(); await open(setup.page);
    expect((await postDraft(setup.client, { ...setup.input, title: 'Доставка по таймеру ' + randomUUID() })).status()).toBe(201); await delivered(setup.client, 2);
    await setup.page.clock.runFor(15_001); await expect(panel(setup.page).locator('.notification-item')).toHaveCount(2);
    let requests = 0; await setup.page.route('**/api/v1/notifications?*', async route => { requests++; const response = await route.fetch(); expect(response.status()).toBe(200); began(); await held; await route.fulfill({ response }); });
    await setup.page.evaluate(() => window.dispatchEvent(new Event('focus'))); await started;
    await setup.page.evaluate(() => { window.dispatchEvent(new Event('focus')); window.dispatchEvent(new Event('focus')); }); await setup.page.clock.runFor(45_001);
    expect(requests).toBe(1); release(); await setup.page.unrouteAll({ behavior: 'wait' }); await expect(panel(setup.page).locator('.notification-item')).toHaveCount(2);
  } finally { release?.(); await setup.dispose(); }
});

test('a revoked tab discards a delayed notification page and stops polling after logout', async ({ browser, playwright }) => {
  const setup = await fixture(playwright, browser); let release!: () => void; const held = new Promise<void>(resolve => { release = resolve; }); let began!: () => void; const started = new Promise<void>(resolve => { began = resolve; });
  try {
    await setup.page.clock.install(); await open(setup.page); let reads = 0;
    setup.page.on('request', request => { if (request.method() === 'GET' && new URL(request.url()).pathname === '/api/v1/notifications') reads++; });
    await setup.page.route('**/api/v1/notifications?*', async route => { const response = await route.fetch(); expect(response.status()).toBe(200); began(); await held; await route.fulfill({ response }); });
    await panel(setup.page).getByRole('button', { name: 'Обновить уведомления', exact: true }).click(); await started;
    const logout = setup.page.waitForResponse(response => response.url().endsWith('/auth/logout') && response.request().method() === 'POST'); await setup.page.getByRole('button', { name: 'Выйти', exact: true }).click(); expect((await logout).status()).toBe(204);
    await expect(setup.page.getByRole('heading', { name: 'Рады видеть вас', exact: true })).toBeVisible(); release(); await setup.page.unrouteAll({ behavior: 'wait' });
    const afterLogout = reads; await setup.page.clock.runFor(45_001); await setup.page.evaluate(() => window.dispatchEvent(new Event('focus'))); expect(reads).toBe(afterLogout);
    await expect(setup.page.locator('.notification-item')).toHaveCount(0); await expect(setup.page.locator('.notification-badge')).toHaveCount(0);
    await setup.page.getByLabel('Email', { exact: true }).fill('operator@demo.poaktu.local'); await setup.page.getByLabel('Пароль', { exact: true }).fill('PoaktuDemo2026!'); await setup.page.getByRole('button', { name: 'Войти в кабинет', exact: true }).click();
    await expect(setup.page.locator('.workspace-heading')).toContainText('Панель оператора'); await expect(setup.page.getByRole('button', { name: 'Уведомления', exact: true })).toHaveCount(0);
  } finally { release?.(); await setup.dispose(); }
});

test('list failure has an explicit retry and read status is private to each recipient', async ({ browser, playwright }) => {
  const setup = await fixture(playwright, browser); const supplier = await apiFor(playwright, 'supplier');
  try {
    await setup.page.goto('/'); await setup.page.route('**/api/v1/notifications?*', route => route.fulfill({ status: 503, json: { error: { code: 'TEMPORARILY_UNAVAILABLE' }, request_id: 'notification-list-error' } }));
    await setup.page.getByRole('button', { name: 'Уведомления', exact: true }).click(); await expect(panel(setup.page).getByRole('alert').first()).toContainText('notification-list-error'); await expect(panel(setup.page).locator('.notification-item')).toHaveCount(0);
    await setup.page.unrouteAll({ behavior: 'wait' }); await panel(setup.page).getByRole('button', { name: 'Обновить уведомления', exact: true }).click(); await expect(panel(setup.page).locator('.notification-item')).toHaveCount(1);
    const own = (await notices(setup.client)).items[0]; await expect.poll(async () => (await notices(supplier)).items.some(item => item.event.id === own.event.id), { timeout: 15_000 }).toBe(true);
    const peer = (await notices(supplier)).items.find(item => item.event.id === own.event.id)!; expect(peer.read_at).toBeNull();
    await panel(setup.page).getByRole('button', { name: 'Отметить прочитанным', exact: true }).click(); await expect(panel(setup.page)).toContainText('Прочитано');
    expect((await notices(supplier)).items.find(item => item.id === peer.id)?.read_at).toBeNull(); expect((await readRequest(setup.client, peer.id)).status()).toBe(404);
  } finally { await supplier.dispose(); await setup.dispose(); }
});

test('notification text stays literal and filters, navigation and actions fit native scrollbar widths', async ({ browser, playwright }) => {
  const setup = await fixture(playwright, browser);
  try {
    const reason = '<script>window.notificationUnsafe=true</script>\n' + 'ОченьДлиннаяПричина'.repeat(30);
    const response = await setup.client.post(`/api/v1/deals/${setup.deal.id}/cancellation-requests`, { data: { deal_version: setup.deal.deal_version, reason }, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(setup.client), 'Idempotency-Key': randomUUID() } });
    expect(response.status()).toBe(201); await delivered(setup.client, 2); await open(setup.page); await expect(panel(setup.page).locator('.notification-reason')).toHaveText(reason);
    expect(await setup.page.evaluate(() => Object.hasOwn(window, 'notificationUnsafe'))).toBe(false);
    const css = new URL('/__e2e__/notification-scrollbar.css', E2E_BASE_URL).href; await setup.page.route(css, route => route.fulfill({ contentType: 'text/css', body: 'html { scrollbar-gutter:stable;overflow-y:scroll }' })); await setup.page.addStyleTag({ url: css });
    for (const width of [320, 375, 768, 1440]) {
      await setup.page.setViewportSize({ width, height: 900 });
      const metrics = await setup.page.evaluate(() => ({ width: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth, body: document.body.getBoundingClientRect().right }));
      expect(metrics.scroll).toBeLessThanOrEqual(metrics.width); expect(metrics.body).toBeLessThanOrEqual(metrics.width);
      const boxes = await setup.page.locator('.workspace-tabs button,.notification-filters button,.notification-actions button').evaluateAll(elements => elements.map(element => { const b = element.getBoundingClientRect(); return { left: b.left, right: b.right, top: b.top, bottom: b.bottom }; }));
      for (const box of boxes) { expect(box.left).toBeGreaterThanOrEqual(0); expect(box.right).toBeLessThanOrEqual(metrics.width); }
      for (let a = 0; a < boxes.length; a++) for (let b = a + 1; b < boxes.length; b++) expect(Math.max(0, Math.min(boxes[a].right, boxes[b].right) - Math.max(boxes[a].left, boxes[b].left)) * Math.max(0, Math.min(boxes[a].bottom, boxes[b].bottom) - Math.max(boxes[a].top, boxes[b].top))).toBeLessThan(1);
    }
  } finally { await setup.dispose(); }
});
