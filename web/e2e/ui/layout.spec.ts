import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import type { APIRequestContext, Page, TestInfo } from '@playwright/test';
import type { Organisation } from '../../src/lib/api.ts';
import { test, expect, API_BASE_URL, E2E_BASE_URL, apiFor, csrf, draftInput, postDraft } from '../support.ts';

// The narrow heights also exercise a sticky banner on a short screen. The adjacent
// widths cover both sides of each layout breakpoint, without pixel baselines.
const sizes = [320, 360, 375, 390, 400, 401, 700, 701, 768, 850, 851, 1024, 1100, 1101, 1366, 1440, 1920]
  .map((width) => ({ width, height: width < 700 ? 568 : width < 1101 ? 600 : 900 }));
const unique = randomUUID().replaceAll('-', '');
const longName = `${'Ж'.repeat(150)}${unique}${'X'.repeat(18)}`;
const longTitle = `${'У'.repeat(150)}${unique}${'T'.repeat(18)}`;
const longDescription = `${'Д'.repeat(2500)}\n${'Р'.repeat(2499)}`;
let longOrganisation: Organisation;
let longState: Awaited<ReturnType<APIRequestContext['storageState']>>;
let dealId = '';

test.beforeAll(async ({ playwright }) => {
  // The complete suite shares the proxy's auth budget. Only this setup hook
  // needs room to respect an explicit 60-second Retry-After from the server.
  test.setTimeout(90_000);
  const registration = await playwright.request.newContext({ baseURL: API_BASE_URL, maxRedirects: 0, storageState: { cookies: [], origins: [] } });
  try {
    let response;
    for (let attempt = 0; attempt < 2; attempt++) {
      response = await registration.post('/api/v1/auth/register', {
        headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(registration) },
        data: { email: `layout-${unique}@demo.poaktu.local`, password: 'LayoutSyntheticOnly2026!', organisation: { name: longName, kind: 'COMPANY' } },
      });
      if (response.status() !== 429 || attempt === 1) break;
      expect((await response.json()).error.code).toBe('RATE_LIMITED');
      const seconds = Number(response.headers()['retry-after']);
      expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true);
      await test.step(`respect layout setup auth budget: wait ${seconds}s`, async () => { await delay(seconds * 1000); });
    }
    if (!response) throw new Error('Layout registration did not run');
    expect(response.status()).toBe(201);
    longOrganisation = (await response.json()).organisation;
    longState = await registration.storageState();
  } finally { await registration.dispose(); }
  const customer = await apiFor(playwright, 'customer');
  try {
    const response = await postDraft(customer, draftInput({
      supplier_party_id: longOrganisation.id, title: longTitle, description: longDescription,
      amount_kopecks: '9223372036854775807', deadline_at: '2030-12-25T23:59:00.000Z',
    }));
    expect(response.status()).toBe(201);
    dealId = (await response.json()).id;
  } finally { await customer.dispose(); }
});

/** Check real sibling regions, not every parent/child intersection on the page. */
async function inspectLayout(page: Page, state: string, info: TestInfo) {
  const viewport = page.viewportSize()!;
  const issues = await page.evaluate(() => {
    const failures: string[] = [];
    const tolerance = 1;
    const visible = (element: Element) => {
      const style = getComputedStyle(element);
      return style.display !== 'none' && style.visibility !== 'hidden' && element.getClientRects().length > 0;
    };
    const label = (element: Element) => `${element.tagName.toLowerCase()}.${element.className}`;
    const textRects = (element: Element): DOMRect[] => {
      const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
      const rectangles: DOMRect[] = [];
      let node: Node | null;
      while ((node = walker.nextNode())) {
        if (!node.textContent?.trim() || !node.parentElement || !visible(node.parentElement)) continue;
        const range = document.createRange();
        range.selectNodeContents(node);
        rectangles.push(...range.getClientRects());
      }
      return rectangles;
    };
    const intersects = (a: DOMRect, b: DOMRect) => Math.min(a.right, b.right) - Math.max(a.left, b.left) > tolerance
      && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > tolerance;
    const pair = (a: Element | null, b: Element | null, name: string, glyphs = false) => {
      if (!a || !b || !visible(a) || !visible(b)) return;
      const aRects = glyphs ? textRects(a) : [a.getBoundingClientRect()];
      const bRects = glyphs ? textRects(b) : [b.getBoundingClientRect()];
      if (aRects.some((aRect) => bRects.some((bRect) => intersects(aRect, bRect)))) failures.push(`overlap ${name}: ${label(a)} / ${label(b)}`);
    };
    const siblings = (rootSelector: string, childSelectors: string[], glyphs = false) => {
      for (const root of document.querySelectorAll(rootSelector)) {
        const children = childSelectors.map((selector) => root.querySelector(selector));
        for (let a = 0; a < children.length; a += 1) {
          for (let b = a + 1; b < children.length; b += 1) pair(children[a], children[b], rootSelector, glyphs);
        }
      }
    };
    if (document.documentElement.scrollWidth > innerWidth + tolerance) failures.push(`horizontal overflow: ${document.documentElement.scrollWidth} > ${innerWidth}`);
    siblings('.header', ['.brand', '.header-right']);
    siblings('.header-right', ['.readiness', '.logout']);
    siblings('.auth-tabs', ['button:first-child', 'button:last-child']);
    siblings('.auth-layout', ['.intro', '.auth-card']);
    siblings('.workspace-heading', [':scope > div', '.environment-badge']);
    siblings('.section-heading', [':scope > div', ':scope > button', ':scope > .tag']);
    siblings('.balance-grid', ['.balance-total', '.balance-reserved', '.balance-available']);
    siblings('.overview-grid', ['.next-stage', '.organisation-info']);
    siblings('.counterparty', ['.counterparty-icon', '.counterparty-name', ':scope > code'], true);
    siblings('.deal-search-row', [':scope > input, :scope > .search-field', ':scope > button']);
    siblings('.deal-filter-fields', [':scope > div:nth-child(1)', ':scope > div:nth-child(2)', ':scope > div:nth-child(3)', ':scope > div:nth-child(4)']);
    siblings('.deal-row', ['.deal-row-main', '.deal-row-amount']);
    siblings('.deal-row-amount', [':scope > span:not(.deal-open)', '.deal-open'], true);
    siblings('.selected-supplier', ['.counterparty-icon', ':scope > div:nth-child(2)', ':scope > button'], true);
    siblings('.deal-form-footer', [':scope > p', ':scope > button']);
    siblings('.deal-create-layout', ['.deal-form', '.deal-guide']);
    siblings('.deal-detail-heading', [':scope > div', ':scope > button']);
    siblings('.deal-detail-grid', ['.deal-conditions', '.deal-parties']);
    siblings('.deal-facts > div', ['dt', 'dd'], true);
    siblings('.agreement-heading', [':scope > div', ':scope > button']);
    siblings('.document-facts > div', ['dt', 'dd'], true);
    siblings('.agreement-consents li', [':scope > div', ':scope > .tag']);
    for (const element of document.querySelectorAll('input, select, textarea, button, .brand')) {
      if (!visible(element)) continue;
      const box = element.getBoundingClientRect();
      if (box.left < -tolerance || box.right > innerWidth + tolerance) failures.push(`control outside viewport: ${label(element)} ${Math.round(box.left)}..${Math.round(box.right)}`);
    }
    // Check the actual glyphs clipped by hidden overflow. An overflowing code box
    // with fully visible UUID text is not a visual defect. Scrollable pickers are
    // intentionally excluded: their offscreen options must remain scrollable.
    const texts = 'h1, h2, h3, .brand-caption, .readiness, .logout, .balance-card > strong, .balance-card > p, .counterparty h3, .counterparty > code, .deal-row h3, .deal-row p, .deal-row-amount > strong, .deal-description, .deal-party > strong, .agreement-heading p, .agreement-consents strong, .agreement-consents .tag, .document-hash, .next-stage > p, .next-stage > .tag';
    for (const element of document.querySelectorAll(texts)) {
      if (!visible(element)) continue;
      const rectangles = textRects(element);
      for (let ancestor = element.parentElement; ancestor; ancestor = ancestor.parentElement) {
        const style = getComputedStyle(ancestor);
        const clipX = ['hidden', 'clip'].includes(style.overflowX);
        const clipY = ['hidden', 'clip'].includes(style.overflowY);
        if (!clipX && !clipY) continue;
        const clip = ancestor.getBoundingClientRect();
        if (rectangles.some((rect) => (clipX && (rect.left < clip.left - tolerance || rect.right > clip.right + tolerance))
          || (clipY && (rect.top < clip.top - tolerance || rect.bottom > clip.bottom + tolerance)))) failures.push(`clipped text: ${label(element)} by ${label(ancestor)}`);
      }
    }
    return [...new Set(failures)];
  });

  const controls = page.locator('header button:visible:not(:disabled), .auth-tabs button:visible:not(:disabled), .auth-card button[type="submit"]:visible:not(:disabled), .workspace-tabs button:visible:not(:disabled), .deals-heading > button:visible:not(:disabled), .deal-form-footer > button:visible:not(:disabled), .deal-detail-heading > button:visible:not(:disabled), .back-link:visible:not(:disabled)');
  // Test a bounded set on every viewport; these checks scroll through the actual
  // page and detect interception by the sticky banner or another foreground box.
  for (let index = 0; index < Math.min(await controls.count(), 8); index += 1) {
    const control = controls.nth(index);
    await control.evaluate((element) => element.scrollIntoView({ block: 'center', inline: 'nearest', behavior: 'instant' }));
    const interception = await control.evaluate((element) => {
      const box = element.getBoundingClientRect();
      const x = box.left + box.width / 2;
      const y = box.top + box.height / 2;
      const target = document.elementFromPoint(x, y);
      return target && (element === target || element.contains(target)) ? null
        : `${element.textContent?.trim()} intercepted by ${target?.tagName}.${target?.className}`;
    });
    if (interception) issues.push(interception);
  }
  await page.evaluate(() => scrollTo({ top: 0, behavior: 'instant' }));
  if ([320, 768, 1440].includes(viewport.width) || issues.length) {
    const name = `${state}-${viewport.width}x${viewport.height}`;
    const path = info.outputPath(`${name}.png`);
    await page.screenshot({ path, fullPage: true, animations: 'disabled' });
    await info.attach(name, { path, contentType: 'image/png' });
  }
  expect.soft(issues, `${state} at ${viewport.width}x${viewport.height}`).toEqual([]);
}

async function matrix(page: Page, state: string, info: TestInfo) {
  for (const size of sizes) {
    await page.setViewportSize(size);
    await inspectLayout(page, state, info);
  }
}

test('layout matrix: login, expanded demo accounts, local error and registration', async ({ browser }, info) => {
  test.setTimeout(120_000);
  const context = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: { cookies: [], origins: [] }, locale: 'ru-RU', timezoneId: 'Europe/Moscow' });
  try {
    const page = await context.newPage();
    await page.route('**/readyz', (route) => route.fulfill({ status: 503, body: '' }));
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Рады видеть вас' })).toBeVisible();
    await expect(page.locator('.readiness')).toContainText('Сервис недоступен');
    await matrix(page, 'login', info);
    await page.getByText('Демоаккаунты для проверки', { exact: true }).click();
    await page.getByLabel('Email', { exact: true }).fill('synthetic-layout@demo.poaktu.local');
    await page.getByLabel('Пароль', { exact: true }).fill('x'.repeat(129));
    await page.getByRole('button', { name: 'Войти в кабинет' }).click();
    await expect(page.getByRole('alert')).toContainText('от 12 до 128 символов');
    await matrix(page, 'login-demo-local-error', info);
    await page.getByRole('button', { name: 'Регистрация', exact: true }).click();
    await page.getByLabel('Название организации', { exact: true }).fill(longName);
    await page.getByLabel('Форма организации').selectOption('IP');
    await page.getByLabel('Пароль', { exact: true }).fill('short');
    await page.getByRole('button', { name: 'Создать организацию', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('от 12 до 128 символов');
    await matrix(page, 'register-local-error', info);
  } finally { await context.close(); }
});

test('layout matrix: long organisation cabinet, catalogue and operator', async ({ page, browser, actors }, info) => {
  test.setTimeout(120_000);
  await page.route('**/readyz', (route) => route.fulfill({ status: 503, body: '' }));
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Учебный счёт' })).toBeVisible();
  await expect(page.locator('.balance-card')).toHaveCount(3);
  await matrix(page, 'customer-account', info);
  const longContext = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: longState, locale: 'ru-RU', timezoneId: 'Europe/Moscow' });
  const operatorContext = await browser.newContext({ baseURL: E2E_BASE_URL, storageState: actors.operator.statePath, locale: 'ru-RU', timezoneId: 'Europe/Moscow' });
  try {
    const organisationPage = await longContext.newPage();
    await organisationPage.goto('/');
    await expect(organisationPage.getByRole('heading', { name: longName, exact: true })).toBeVisible();
    await expect(organisationPage.locator('.balance-card')).toHaveCount(3);
    await matrix(organisationPage, 'long-organisation-account', info);
    await page.getByRole('button', { name: 'Контрагенты', exact: true }).click();
    await page.getByLabel('Название организации', { exact: true }).fill(longName);
    await page.getByRole('search').getByRole('button', { name: 'Найти', exact: true }).click();
    await expect(page.locator('.counterparty').filter({ hasText: longOrganisation.id })).toBeVisible();
    await matrix(page, 'long-organisation-catalogue', info);
    const operator = await operatorContext.newPage();
    await operator.goto('/');
    await expect(operator.getByRole('heading', { name: 'Отдельный доступ оператора' })).toBeVisible();
    await matrix(operator, 'operator', info);
  } finally { await longContext.close(); await operatorContext.close(); }
});

test('layout matrix: real maximum amount, long deal list and complete detail', async ({ page }, info) => {
  test.setTimeout(120_000);
  await page.goto('/');
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(dealId);
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  const row = page.locator('.deal-row').filter({ hasText: dealId });
  await expect(row).toHaveCount(1);
  await expect(row.locator('.deal-row-amount > strong')).toHaveText('92 233 720 368 547 758,07 ₽');
  await matrix(page, 'max-amount-long-deal-list', info);
  await row.click();
  await expect(page.getByRole('heading', { name: longTitle, exact: true })).toBeVisible();
  await expect(page.locator('.deal-description')).toHaveText(longDescription);
  await expect(page.locator('.deal-condition-amount > strong')).toHaveText('92 233 720 368 547 758,07 ₽');
  await matrix(page, 'max-amount-long-deal-detail', info);
});

test('layout matrix: selected long supplier and explicitly injected pending response', async ({ page, actors }, info) => {
  test.setTimeout(120_000);
  await page.goto('/');
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await page.getByRole('button', { name: 'Новая сделка', exact: true }).click();
  await page.getByLabel('Исполнитель', { exact: true }).fill(longName);
  await page.locator('.supplier-picker').getByRole('button', { name: 'Найти', exact: true }).click();
  await page.locator('.supplier-option').filter({ hasText: longOrganisation.id }).click();
  await page.getByLabel('Название сделки', { exact: true }).fill(longTitle);
  await page.getByLabel('Условия и результат работы', { exact: true }).fill(longDescription);
  await page.getByLabel('Полная сумма, ₽', { exact: true }).fill('92233720368547758,07');
  await page.getByLabel('Срок сделки', { exact: true }).fill('2030-12-25T23:59');
  await matrix(page, 'form-selected-long-supplier', info);
  let injected = 0;
  await page.route('**/api/v1/deals', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    injected += 1;
    await route.fulfill({ status: 503, json: { error: { code: 'TEMPORARILY_UNAVAILABLE', message: 'Explicit layout-only failure injection; no server command executed' }, request_id: 'layout-pending-synthetic-response' } });
  });
  try {
    await page.getByRole('button', { name: 'Создать черновик', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('layout-pending-synthetic-response');
    await expect(page.getByRole('button', { name: 'Проверить результат создания', exact: true })).toBeEnabled();
    await expect(page.getByLabel('Название сделки', { exact: true })).toBeDisabled();
    expect(injected).toBe(1);
    await matrix(page, 'form-pending-injected-503', info);
  } finally {
    await page.evaluate((key) => sessionStorage.removeItem(key), `poaktu:pending-deal:${actors.customer.organisation!.id}`);
    await page.unroute('**/api/v1/deals');
  }
});
