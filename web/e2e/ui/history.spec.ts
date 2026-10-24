import { randomUUID } from 'node:crypto';
import type { APIRequestContext, Browser, Download, Page, PlaywrightWorkerArgs } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, API_BASE_URL, E2E_BASE_URL } from '../support.ts';
import type { AuditEvent, Deal, Page as AuditPage } from '../../src/lib/api.ts';

async function command(client: APIRequestContext, path: string, data: unknown) {
  return client.post(path, { data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': randomUUID() } });
}
async function fixture(playwright: PlaywrightWorkerArgs['playwright']) {
  const customer = await apiFor(playwright, 'customer'); const supplier = await apiFor(playwright, 'supplier');
  try {
    const response = await postDraft(customer, draftInput({ title: 'History UI ' + randomUUID(), amount_kopecks: '100' }));
    expect(response.status()).toBe(201); const deal = await response.json() as Deal;
    return { customer, supplier, deal, dispose: () => Promise.all([customer.dispose(), supplier.dispose()]) };
  } catch (error) { await Promise.all([customer.dispose(), supplier.dispose()]); throw error; }
}
async function openDeal(page: Page, id: string) {
  await page.goto('/'); await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(id);
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  await page.locator('.deal-row').filter({ hasText: id }).click();
  await expect(page.getByRole('region', { name: 'История сделки', exact: true }).locator('.audit-event').first()).toBeVisible();
}
async function downloadText(page: Page, format: 'CSV' | 'JSON') {
  const region = page.getByRole('region', { name: 'История сделки', exact: true });
  await region.getByRole('button', { name: 'Подготовить ' + format, exact: true }).click();
  const received = page.waitForEvent('download'); await region.getByRole('link', { name: `Скачать ${format} истории`, exact: true }).click();
  const download: Download = await received; const stream = await download.createReadStream(); const chunks: Buffer[] = [];
  for await (const chunk of stream!) chunks.push(Buffer.from(chunk));
  return { text: Buffer.concat(chunks).toString('utf8'), filename: download.suggestedFilename() };
}
// A same-origin external stylesheet keeps the layout fixture subject to the
// deployed style-src policy; inline styles and CSP bypass are unnecessary.
async function reserveScrollbarGutter(page: Page) {
  const url = new URL('/__e2e__/scrollbar-gutter.css', page.url()).href;
  await page.route(url, route => route.fulfill({
    status: 200, contentType: 'text/css',
    headers: { 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' },
    body: 'html { scrollbar-gutter: stable; overflow-y: scroll; }',
  }));
  try { await page.addStyleTag({ url }); }
  finally { await page.unroute(url); }
  expect(await page.evaluate(() => {
    const style = getComputedStyle(document.documentElement);
    return { gutter: style.scrollbarGutter, overflow: style.overflowY };
  })).toEqual({ gutter: 'stable', overflow: 'scroll' });
}

// Reserve a classic scrollbar gutter even in headless Chromium, where the
// normal viewport can otherwise hide overflow that native browser users see.
async function visibleLayoutWidth(page: Page) {
  const metrics = await page.evaluate(() => {
    const root = document.documentElement; const body = document.body.getBoundingClientRect();
    return { visible: root.clientWidth, content: root.scrollWidth, bodyLeft: body.left, bodyRight: body.right };
  });
  expect(metrics.content).toBeLessThanOrEqual(metrics.visible);
  expect(metrics.bodyLeft).toBeGreaterThanOrEqual(0); expect(metrics.bodyRight).toBeLessThanOrEqual(metrics.visible);
  return metrics.visible;
}
async function events(client: APIRequestContext, id: string) {
  const response = await client.get(`/api/v1/deals/${id}/audit`); expect(response.status()).toBe(200); return await response.json() as AuditPage<AuditEvent>;
}
async function fullLifecycle(playwright: PlaywrightWorkerArgs['playwright']) {
  const setup = await fixture(playwright); let deal = setup.deal;
  const generated = await command(setup.customer, `/api/v1/deals/${deal.id}/documents/generate`, { expected_deal_version: deal.deal_version, template_id: 'printing-acceptance-v1' });
  expect(generated.status()).toBe(201); deal = (await generated.json()).deal;
  const agree = { deal_version: deal.deal_version, document_id: deal.document!.id, document_version: deal.document!.document_version, document_sha256: deal.document!.sha256 };
  expect((await command(setup.customer, `/api/v1/deals/${deal.id}/agree`, agree)).status()).toBe(200);
  expect((await command(setup.supplier, `/api/v1/deals/${deal.id}/agree`, agree)).status()).toBe(200);
  const rework = await command(setup.supplier, `/api/v1/deals/${deal.id}/rework`, { deal_version: deal.deal_version, reason: 'Уточнить «макет»\nСохранить прежний PDF в истории' });
  expect(rework.status()).toBe(200); deal = await rework.json();
  const reason = '=Тестовая причина, "кавычки"\nВторая строка';
  const first = await command(setup.customer, `/api/v1/deals/${deal.id}/cancellation-requests`, { deal_version: deal.deal_version, reason });
  expect(first.status()).toBe(201); const requestId = (await first.json()).cancellation_request.id;
  const rejected = await command(setup.supplier, `/api/v1/deals/${deal.id}/cancellation-requests/${requestId}/reject`, { deal_version: deal.deal_version, reason: 'Работа подготовлена; нужно обсудить результат' });
  expect(rejected.status()).toBe(200);
  const second = await command(setup.customer, `/api/v1/deals/${deal.id}/cancellation-requests`, { deal_version: deal.deal_version, reason: 'Повторно согласовали отмену' });
  expect(second.status()).toBe(201); const secondId = (await second.json()).cancellation_request.id;
  expect((await command(setup.supplier, `/api/v1/deals/${deal.id}/cancellation-requests/${secondId}/accept`, { deal_version: deal.deal_version })).status()).toBe(200);
  return { ...setup, reason };
}

test('history displays immutable versions, both actors and closed cancellation reasons after reload', async ({ page, playwright, actors }) => {
  const setup = await fullLifecycle(playwright);
  try {
    await openDeal(page, setup.deal.id); const history = page.getByRole('region', { name: 'История сделки', exact: true });
    const actual = await events(setup.customer, setup.deal.id);
    await expect(history.locator('.audit-event')).toHaveCount(actual.items.length);
    await expect(history).toContainText(setup.reason);
    const rejection = history.getByRole('article', { name: 'Сторона отказала в отмене', exact: true });
    await expect(rejection).toContainText('Работа подготовлена; нужно обсудить результат');
    await expect(rejection).toContainText('Действие выполнено');
    await expect(history.locator('.audit-time').first()).toContainText('Europe/Moscow');
    await expect(history.locator('.audit-time').first()).toContainText('UTC:');
    for (const details of await history.locator('.audit-event-details').all()) await details.getByText('Подробности события', { exact: true }).click();
    await expect(history).toContainText(actors.customer.user.id); await expect(history).toContainText(actors.supplier.user.id);
    await expect(history).toContainText('Версия документа'); await expect(history).not.toContainText(actors.customer.user.email);
    await page.reload(); await openDeal(page, setup.deal.id);
    await expect(history).toContainText('Работа подготовлена; нужно обсудить результат');
  } finally { await setup.dispose(); }
});

test('CSV and JSON downloads use the same applied filters, preserve Cyrillic and protect spreadsheet formulas', async ({ page, playwright }) => {
  const setup = await fullLifecycle(playwright);
  try {
    await openDeal(page, setup.deal.id); const history = page.getByRole('region', { name: 'История сделки', exact: true });
    await history.getByLabel('Тип события', { exact: true }).selectOption('CANCELLATION_REQUESTED');
    await expect(history.getByRole('button', { name: 'Подготовить JSON', exact: true })).toBeDisabled();
    await history.getByRole('button', { name: 'Применить фильтры истории', exact: true }).click();
    await expect(history.locator('.audit-event')).toHaveCount(2);
    const json = await downloadText(page, 'JSON'); const parsed = JSON.parse(json.text) as { items: AuditEvent[] };
    expect(json.filename).toBe(`poaktu-audit-${setup.deal.id}.json`); expect(parsed.items).toHaveLength(2);
    expect(parsed.items.map(item => item.details.reason)).toContain(setup.reason);
    expect(parsed.items.every(item => item.type === 'CANCELLATION_REQUESTED' && item.deal_id === setup.deal.id)).toBe(true);
    const csv = await downloadText(page, 'CSV'); expect(csv.filename).toBe(`poaktu-audit-${setup.deal.id}.csv`);
    expect(csv.text.charCodeAt(0)).toBe(0xFEFF); expect(csv.text).toContain("'=Тестовая причина"); expect(csv.text).toContain('""кавычки""');
    expect(csv.text).toContain('Вторая строка'); expect(csv.text).not.toContain('DEAL_CREATED');
    await history.getByLabel('Тип события', { exact: true }).selectOption('DEAL_CREATED');
    await expect(history.getByRole('link', { name: 'Скачать CSV истории' })).toHaveCount(0);
    await expect(history.getByRole('button', { name: 'Подготовить CSV', exact: true })).toBeDisabled();
  } finally { await setup.dispose(); }
});

test('pagination appends unique real events while JSON exports every page and dates clear an invalid result', async ({ page, playwright }) => {
  const setup = await fixture(playwright); let deal = setup.deal;
  try {
    for (let index = 0; index < 22; index++) {
      const response = await setup.customer.patch(`/api/v1/deals/${deal.id}`, { data: { expected_version: deal.deal_version, description: 'Изменённые условия ' + index }, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(setup.customer), 'Idempotency-Key': randomUUID() } });
      expect(response.status()).toBe(200); deal = await response.json();
    }
    await openDeal(page, deal.id); const history = page.getByRole('region', { name: 'История сделки', exact: true });
    await expect(history.locator('.audit-event')).toHaveCount(20);
    const json = JSON.parse((await downloadText(page, 'JSON')).text) as { items: AuditEvent[] }; expect(json.items).toHaveLength(23);
    expect(new Set(json.items.map(item => item.id)).size).toBe(23);
    await history.getByRole('button', { name: 'Показать ещё события', exact: true }).click(); await expect(history.locator('.audit-event')).toHaveCount(23);
    await expect(history.getByRole('button', { name: 'Показать ещё события', exact: true })).toHaveCount(0);
    await history.getByLabel('События с даты', { exact: true }).fill('2026-10-25'); await history.getByLabel('По дату включительно', { exact: true }).fill('2026-10-24');
    await history.getByRole('button', { name: 'Применить фильтры истории', exact: true }).click();
    await expect(history.getByRole('alert')).toContainText('Начало периода'); await expect(history.locator('.audit-event')).toHaveCount(0);
    await expect(history.getByRole('button', { name: 'Подготовить JSON', exact: true })).toBeDisabled();
    await history.getByRole('button', { name: 'Сбросить фильтры истории', exact: true }).click(); await expect(history.locator('.audit-event')).toHaveCount(20);
  } finally { await setup.dispose(); }
});

test('a delayed real history response cannot replace a newer applied type filter', async ({ page, playwright }) => {
  const setup = await fixture(playwright); let release!: () => void; const held = new Promise<void>(resolve => { release = resolve; }); let began!: () => void; const started = new Promise<void>(resolve => { began = resolve; });
  try {
    await openDeal(page, setup.deal.id); const history = page.getByRole('region', { name: 'История сделки', exact: true });
    await page.route(`**/api/v1/deals/${setup.deal.id}/audit?*`, async route => {
      if (new URL(route.request().url()).searchParams.get('type') !== 'DEAL_CREATED') { await route.continue(); return; }
      const response = await route.fetch(); expect(response.status()).toBe(200); began(); await held; await route.fulfill({ response });
    });
    await history.getByLabel('Тип события', { exact: true }).selectOption('DEAL_CREATED'); await history.getByRole('button', { name: 'Применить фильтры истории', exact: true }).click(); await started;
    await history.getByLabel('Тип события', { exact: true }).selectOption('DEAL_SETTLED'); await history.getByRole('button', { name: 'Применить фильтры истории', exact: true }).click();
    await expect(history.locator('.audit-empty')).toBeVisible(); release(); await page.unrouteAll({ behavior: 'wait' });
    await expect(history.locator('.audit-event')).toHaveCount(0); await expect(history.locator('.audit-empty')).toBeVisible();
  } finally { release?.(); await setup.dispose(); }
});

test('export errors leave history readable and an explicit retry downloads the authentic server file', async ({ page, playwright }) => {
  const setup = await fixture(playwright);
  try {
    await openDeal(page, setup.deal.id); const history = page.getByRole('region', { name: 'История сделки', exact: true });
    await page.route(`**/api/v1/deals/${setup.deal.id}/audit/export?*`, route => route.fulfill({ status: 503, json: { error: { code: 'TEMPORARILY_UNAVAILABLE' }, request_id: 'history-export-test' } }));
    await history.getByRole('button', { name: 'Подготовить JSON', exact: true }).click();
    await expect(history.getByRole('alert')).toContainText('history-export-test'); await expect(history.locator('.audit-event')).toHaveCount(1); await expect(history.getByRole('link')).toHaveCount(0);
    await page.unrouteAll({ behavior: 'wait' }); const json = JSON.parse((await downloadText(page, 'JSON')).text);
    expect(json.items[0]).toMatchObject({ deal_id: setup.deal.id, type: 'DEAL_CREATED' });
  } finally { await setup.dispose(); }
});

test('operator audit has protected deal filters and participant navigation has no operator tab', async ({ page, browser, playwright, actors }) => {
  const setup = await fixture(playwright); const operator = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: actors.operator.statePath }); const view = await operator.newPage();
  try {
    await page.goto('/'); await expect(page.getByRole('button', { name: 'Аудит', exact: true })).toHaveCount(0);
    await view.goto('/'); await view.getByRole('button', { name: 'Аудит', exact: true }).click(); const history = view.getByRole('region', { name: 'Аудит оператора', exact: true });
    await history.getByLabel('ID сделки в аудите', { exact: true }).fill(setup.deal.id); await history.getByLabel('Тип события', { exact: true }).selectOption('DEAL_CREATED');
    await history.getByRole('button', { name: 'Применить фильтры истории', exact: true }).click(); await expect(history.locator('.audit-event')).toHaveCount(1);
    await history.getByText('Подробности события', { exact: true }).click(); await expect(history).toContainText(setup.deal.id);
    await expect(history.getByRole('button', { name: 'Подготовить CSV', exact: true })).toHaveCount(0);
    await expect(history).not.toContainText(actors.customer.user.email);
    await reserveScrollbarGutter(view);
    const navigation = view.getByRole('navigation', { name: 'Разделы кабинета', exact: true });
    for (const width of [320, 375, 1440]) {
      await view.setViewportSize({ width, height: 1000 });
      const visibleWidth = await visibleLayoutWidth(view);
      const boxes = await navigation.getByRole('button').evaluateAll(elements => elements.map(element => { const rect = element.getBoundingClientRect(); return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom }; }));
      for (const box of boxes) { expect(box.left).toBeGreaterThanOrEqual(0); expect(box.right).toBeLessThanOrEqual(visibleWidth); }
      for (let left = 0; left < boxes.length; left++) for (let right = left + 1; right < boxes.length; right++) {
        const a = boxes[left]; const b = boxes[right];
        expect(Math.max(0, Math.min(a.right, b.right) - Math.max(a.left, b.left)) * Math.max(0, Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top))).toBeLessThan(1);
      }
      if (width === 320) {
        for (const name of ['События ЭДО', 'Учебные счета', 'Контрагенты', 'Обзор', 'Аудит']) {
          const button = navigation.getByRole('button', { name, exact: true });
          await button.click(); await expect(button).toHaveAttribute('aria-current', 'page');
        }
        await expect(history).toBeVisible();
      }
    }
  } finally { await operator.close(); await setup.dispose(); }
});

test('late export bytes are discarded after logout and do not create a link for a different account', async ({ browser, playwright }) => {
  const setup = await fixture(playwright); const client = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } }); let context: Awaited<ReturnType<Browser['newContext']>> | undefined;
  let release!: () => void; const held = new Promise<void>(resolve => { release = resolve; }); let began!: () => void; const started = new Promise<void>(resolve => { began = resolve; });
  try {
    expect((await client.post('/api/v1/auth/login', { data: { email: 'customer@demo.poaktu.local', password: 'PoaktuDemo2026!' }, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client) } })).status()).toBe(200);
    context = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: await client.storageState() }); const page = await context.newPage();
    await openDeal(page, setup.deal.id);
    await page.route(`**/api/v1/deals/${setup.deal.id}/audit/export?*`, async route => { const response = await route.fetch(); expect(response.status()).toBe(200); began(); await held; await route.fulfill({ response }); });
    await page.getByRole('button', { name: 'Подготовить JSON', exact: true }).click(); await started;
    await page.getByRole('button', { name: 'Выйти', exact: true }).click(); await expect(page.getByRole('heading', { name: 'Рады видеть вас', exact: true })).toBeVisible();
    await page.getByLabel('Email', { exact: true }).fill('supplier@demo.poaktu.local'); await page.getByLabel('Пароль', { exact: true }).fill('PoaktuDemo2026!'); await page.getByRole('button', { name: 'Войти в кабинет', exact: true }).click();
    await expect(page.locator('.workspace-heading')).toBeVisible(); release(); await page.unrouteAll({ behavior: 'wait' });
    await expect(page.getByRole('link', { name: 'Скачать JSON истории', exact: true })).toHaveCount(0); await expect(page.getByRole('alert')).toHaveCount(0);
    await openDeal(page, setup.deal.id); await expect(page.getByRole('region', { name: 'История сделки', exact: true }).locator('.audit-event')).toHaveCount(1);
  } finally { release?.(); await context?.close(); await client.dispose(); await setup.dispose(); }
});

test('history long reasons, IDs and filter actions fit 320–1440px without overlap or horizontal scrolling', async ({ page, playwright }) => {
  const setup = await fullLifecycle(playwright);
  try {
    await openDeal(page, setup.deal.id); const history = page.getByRole('region', { name: 'История сделки', exact: true });
    for (const details of await history.locator('.audit-event-details').all()) await details.getByText('Подробности события', { exact: true }).click();
    await reserveScrollbarGutter(page);
    for (const width of [320, 375, 768, 1440]) {
      await page.setViewportSize({ width, height: 1000 }); const visibleWidth = await visibleLayoutWidth(page);
      const boxes = await history.locator('input,select,.audit-actions>.button').evaluateAll(elements => elements.map(element => { const rect = element.getBoundingClientRect(); return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom }; }));
      for (const box of boxes) { expect(box.left).toBeGreaterThanOrEqual(0); expect(box.right).toBeLessThanOrEqual(visibleWidth); }
      for (let left = 0; left < boxes.length; left++) for (let right = left + 1; right < boxes.length; right++) {
        const a = boxes[left]; const b = boxes[right]; expect(Math.max(0, Math.min(a.right, b.right) - Math.max(a.left, b.left)) * Math.max(0, Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top))).toBeLessThan(1);
      }
    }
  } finally { await setup.dispose(); }
});
