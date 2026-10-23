import { randomUUID } from 'node:crypto';
import type { Page } from '@playwright/test';
import { test, expect, apiFor, draftInput, postDraft } from '../support.ts';

type SentCommand = { key: string | undefined; body: string | null };
const deadline = '2030-12-25T15:45';

async function openDeals(page: Page) {
  await page.goto('/');
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await expect(page.getByRole('search', { name: 'Поиск сделок' })).toBeVisible();
}

async function fillDraft(page: Page, supplier: { id: string; name: string }, title: string) {
  await openDeals(page);
  await page.getByRole('button', { name: 'Новая сделка', exact: true }).click();
  await page.getByLabel('Исполнитель', { exact: true }).fill(supplier.name);
  await page.locator('.supplier-picker').getByRole('button', { name: 'Найти', exact: true }).click();
  await page.locator('.supplier-option').filter({ hasText: supplier.id }).click();
  await page.getByLabel('Название сделки', { exact: true }).fill(title);
  await page.getByLabel('Условия и результат работы', { exact: true }).fill('Учебные условия: выполнить всю работу и передать один согласованный результат.');
  await page.getByLabel('Полная сумма, ₽', { exact: true }).fill('1234,56');
  await page.getByLabel('Срок сделки', { exact: true }).fill(deadline);
}

async function createFixture(playwright: Parameters<typeof apiFor>[0], title: string) {
  const request = await apiFor(playwright, 'customer');
  try {
    const response = await postDraft(request, draftInput({ title }));
    expect(response.status()).toBe(201);
    return await response.json();
  } finally { await request.dispose(); }
}

test('create stores exact kopecks and UTC; both participants see the card and an outsider cannot', async ({ page, browser, actors }) => {
  const title = `UI exact amount ${randomUUID()}`;
  await fillDraft(page, actors.supplier.organisation!, title);
  await page.getByLabel('Полная сумма, ₽', { exact: true }).fill('90071992547409,93');
  const expectedUtc = await page.evaluate((value) => new Date(value).toISOString(), deadline);
  const created = page.waitForResponse((response) => response.url().endsWith('/api/v1/deals') && response.request().method() === 'POST');
  await page.getByRole('button', { name: 'Создать черновик' }).click();
  const response = await created;
  expect(response.status()).toBe(201);
  expect(response.request().postDataJSON()).toMatchObject({ title, amount_kopecks: '9007199254740993', currency: 'RUB', deadline_at: expectedUtc, supplier_party_id: actors.supplier.organisation!.id });
  const deal = await response.json();
  expect(deal).toMatchObject({ customer_party_id: actors.customer.organisation!.id, amount_kopecks: '9007199254740993', deal_version: 1, status: 'DRAFT', reserve: null, payment_id: null, agreements: [], signatures: [] });
  expect(Date.parse(deal.deadline_at)).toBe(Date.parse(expectedUtc));
  await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
  await expect(page.locator('.deal-condition-amount strong')).toHaveText('90 071 992 547 409,93 ₽');
  await expect(page.locator('.deal-detail-heading')).toContainText('Версия 1 · Вы — заказчик');
  await expect(page.getByRole('region', { name: 'Документ и согласование' })).toContainText('Согласия сторон на текущую версию');
  await expect(page.locator('.agreement-consents .tag')).toHaveText(['Ожидает согласия', 'Ожидает согласия']);
  await page.getByRole('button', { name: '← К списку сделок' }).click();
  await expect(page.locator('.deal-row').filter({ hasText: title })).toHaveCount(1);

  for (const role of ['supplier', 'outsider'] as const) {
    const context = await browser.newContext({ baseURL: test.info().project.use.baseURL, storageState: actors[role].statePath });
    try {
      const participant = await context.newPage();
      await openDeals(participant);
      await participant.getByLabel('Поиск по названию, ID или контрагенту').fill(deal.id);
      await participant.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
      if (role === 'supplier') {
        await participant.locator('.deal-row').filter({ hasText: title }).click();
        await expect(participant.getByRole('heading', { name: title, exact: true })).toBeVisible();
        await expect(participant.locator('.deal-detail-heading')).toContainText('Вы — исполнитель');
        expect((await participant.request.get(`/api/v1/deals/${deal.id}`)).status()).toBe(200);
      } else {
        await expect(participant.getByRole('heading', { name: 'Сделки не найдены' })).toBeVisible();
        expect((await participant.request.get(`/api/v1/deals/${deal.id}`)).status()).toBe(404);
      }
    } finally { await context.close(); }
  }
});

test('the supplier picker excludes own organisation and local validation sends no invalid command', async ({ page, actors }) => {
  await openDeals(page);
  await page.getByRole('button', { name: 'Новая сделка', exact: true }).click();
  await expect(page.locator('.supplier-option').first()).toBeVisible();
  await expect(page.locator('.supplier-option').filter({ hasText: actors.customer.organisation!.id })).toHaveCount(0);
  await page.getByLabel('Исполнитель', { exact: true }).fill(actors.supplier.organisation!.name);
  await page.locator('.supplier-picker').getByRole('button', { name: 'Найти', exact: true }).click();
  await page.locator('.supplier-option').filter({ hasText: actors.supplier.organisation!.id }).click();
  await page.getByLabel('Название сделки', { exact: true }).fill(`Local validation ${randomUUID()}`);
  await page.getByLabel('Условия и результат работы', { exact: true }).fill('Полные учебные условия');
  await page.getByLabel('Срок сделки', { exact: true }).fill(deadline);
  let posts = 0;
  page.on('request', (request) => { if (request.method() === 'POST' && request.url().endsWith('/api/v1/deals')) posts += 1; });
  for (const [amount, message] of [['0', 'Сумма должна быть больше нуля'], ['1,001', 'до двух знаков после запятой'], ['92233720368547758,08', 'Сумма превышает допустимый предел']] as const) {
    await page.getByLabel('Полная сумма, ₽', { exact: true }).fill(amount);
    await page.getByRole('button', { name: 'Создать черновик' }).click();
    await expect(page.getByRole('alert')).toContainText(message);
    await expect(page.getByLabel('Название сделки', { exact: true })).toBeEnabled();
  }
  await page.getByLabel('Полная сумма, ₽', { exact: true }).fill('0,01');
  await page.getByLabel('Срок сделки', { exact: true }).fill('2000-01-01T00:00');
  await page.getByRole('button', { name: 'Создать черновик' }).click();
  await expect(page.getByRole('alert')).toContainText('Укажите срок сделки в будущем');
  expect(posts).toBe(0);
  expect(await page.evaluate((key) => sessionStorage.getItem(key), `poaktu:pending-deal:${actors.customer.organisation!.id}`)).toBeNull();
});

test('a lost response after a real commit survives reload and replays the identical command once', async ({ page, actors }) => {
  const title = `UI committed response lost ${randomUUID()}`;
  await fillDraft(page, actors.supplier.organisation!, title);
  const commands: SentCommand[] = [];
  let committedId = '';
  await page.route('**/api/v1/deals', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    commands.push({ key: route.request().headers()['idempotency-key'], body: route.request().postData() });
    if (commands.length === 1) {
      const committed = await route.fetch();
      expect(committed.status()).toBe(201);
      committedId = (await committed.json()).id;
      await route.abort('failed');
    } else await route.continue();
  });
  await page.getByRole('button', { name: 'Создать черновик' }).click();
  await expect(page.getByRole('alert')).toContainText('Нет связи с сервером');
  await expect(page.getByLabel('Название сделки', { exact: true })).toBeDisabled();
  expect(committedId).not.toBe('');
  await page.reload();
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await expect(page.getByText('Один запрос ожидает подтверждения')).toBeVisible();
  expect(commands).toHaveLength(1);
  await page.getByRole('button', { name: 'Продолжить создание', exact: true }).click();
  await expect(page.getByLabel('Название сделки', { exact: true })).toBeDisabled();
  await expect(page.getByLabel('Название сделки', { exact: true })).toHaveValue(title);
  expect(commands).toHaveLength(1);
  await page.getByRole('button', { name: 'Проверить результат создания', exact: true }).click();
  await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
  expect(commands).toHaveLength(2);
  expect(commands[1]).toEqual(commands[0]);
  const list = await (await page.request.get('/api/v1/deals', { params: { q: title } })).json();
  expect(list.items.map((deal: { id: string }) => deal.id)).toEqual([committedId]);
  expect(await page.evaluate((key) => sessionStorage.getItem(key), `poaktu:pending-deal:${actors.customer.organisation!.id}`)).toBeNull();
});

test('in-flight double click creates one command and locks all editable conditions', async ({ page, actors }) => {
  const title = `UI double click ${randomUUID()}`;
  await fillDraft(page, actors.supplier.organisation!, title);
  let posts = 0;
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/api/v1/deals', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    posts += 1;
    await gate;
    await route.continue();
  });
  await page.getByRole('button', { name: 'Создать черновик' }).dblclick({ force: true });
  await expect.poll(() => posts).toBe(1);
  await expect(page.getByRole('button', { name: 'Подтверждаем…' })).toBeDisabled();
  for (const name of ['Название сделки', 'Условия и результат работы', 'Полная сумма, ₽', 'Срок сделки']) await expect(page.getByLabel(name, { exact: true })).toBeDisabled();
  release();
  await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
  expect(posts).toBe(1);
  const list = await (await page.request.get('/api/v1/deals', { params: { q: title } })).json();
  expect(list.items).toHaveLength(1);
});

test('a 503 preserves the locked pending key and manual retry sends exactly the same conditions', async ({ page, actors }) => {
  const title = `UI unavailable ${randomUUID()}`;
  await fillDraft(page, actors.supplier.organisation!, title);
  const commands: SentCommand[] = [];
  await page.route('**/api/v1/deals', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    commands.push({ key: route.request().headers()['idempotency-key'], body: route.request().postData() });
    if (commands.length === 1) await route.fulfill({ status: 503, json: { error: { code: 'TEMPORARILY_UNAVAILABLE', message: 'Synthetic test unavailable' }, request_id: 'ui-create-503' } });
    else await route.continue();
  });
  await page.getByRole('button', { name: 'Создать черновик' }).click();
  await expect(page.getByRole('alert')).toContainText('ui-create-503');
  await expect(page.getByLabel('Полная сумма, ₽', { exact: true })).toBeDisabled();
  const pending = await page.evaluate((key) => sessionStorage.getItem(key), `poaktu:pending-deal:${actors.customer.organisation!.id}`);
  expect(JSON.parse(pending!).key).toBe(commands[0].key);
  await page.getByRole('button', { name: 'Проверить результат создания', exact: true }).click();
  await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
  expect(commands).toHaveLength(2);
  expect(commands[1]).toEqual(commands[0]);
});

test('a definitive validation rejection unlocks editing and the corrected command uses a fresh key', async ({ page, actors }) => {
  const title = `UI definitive rejection ${randomUUID()}`;
  await fillDraft(page, actors.supplier.organisation!, title);
  const commands: SentCommand[] = [];
  await page.route('**/api/v1/deals', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    commands.push({ key: route.request().headers()['idempotency-key'], body: route.request().postData() });
    if (commands.length === 1) await route.fulfill({ status: 400, json: { error: { code: 'INVALID_AMOUNT', message: 'Synthetic definitive validation failure' } } });
    else await route.continue();
  });
  await page.getByRole('button', { name: 'Создать черновик' }).click();
  await expect(page.getByRole('alert')).toContainText('Проверьте сумму');
  await expect(page.getByLabel('Полная сумма, ₽', { exact: true })).toBeEnabled();
  expect(await page.evaluate((key) => sessionStorage.getItem(key), `poaktu:pending-deal:${actors.customer.organisation!.id}`)).toBeNull();
  await page.getByLabel('Полная сумма, ₽', { exact: true }).fill('1234,57');
  await page.getByRole('button', { name: 'Создать черновик' }).click();
  await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
  expect(commands).toHaveLength(2);
  expect(commands[1].key).not.toBe(commands[0].key);
  expect(JSON.parse(commands[1].body!).amount_kopecks).toBe('123457');
});

test('a corrupt saved command blocks new creation instead of silently replacing its identity', async ({ page, actors }) => {
  await page.addInitScript((key) => sessionStorage.setItem(key, '{broken'), `poaktu:pending-deal:${actors.customer.organisation!.id}`);
  let posts = 0;
  page.on('request', (request) => { if (request.method() === 'POST' && request.url().endsWith('/api/v1/deals')) posts += 1; });
  await openDeals(page);
  await expect(page.getByRole('alert')).toContainText('Не удалось прочитать сохранённый запрос создания');
  await expect(page.getByRole('button', { name: 'Новая сделка', exact: true })).toBeDisabled();
  expect(posts).toBe(0);
});

test('list filters reach the API, invalid dates stay local, and reset restores visible deals', async ({ page, playwright }) => {
  const title = `UI filters ${randomUUID()}`;
  const deal = await createFixture(playwright, title);
  await openDeals(page);
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(title);
  await page.getByLabel('Состояние', { exact: true }).selectOption('DRAFT');
  await page.getByLabel('Моя роль', { exact: true }).selectOption('customer');
  await page.getByLabel('Создана с', { exact: true }).fill('2000-01-01');
  await page.getByLabel('По дату включительно', { exact: true }).fill('2099-12-31');
  const filtered = page.waitForResponse((response) => response.url().includes('/api/v1/deals?') && new URL(response.url()).searchParams.get('q') === title);
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  const response = await filtered;
  const query = new URL(response.url()).searchParams;
  expect(query.get('status')).toBe('DRAFT');
  expect(query.get('role')).toBe('customer');
  expect(query.get('created_from')).toMatch(/Z$/);
  expect(query.get('created_to')).toMatch(/999999Z$/);
  await expect(page.locator('.deal-row')).toHaveCount(1);
  await expect(page.locator('.deal-row')).toContainText(deal.id);
  await page.getByLabel('Моя роль', { exact: true }).selectOption('supplier');
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Сделки не найдены' })).toBeVisible();
  await page.getByLabel('Создана с', { exact: true }).fill('2099-12-31');
  await page.getByLabel('По дату включительно', { exact: true }).fill('2000-01-01');
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Начало периода не может быть позже его окончания');
  await page.getByRole('button', { name: 'Сбросить фильтры', exact: true }).click();
  await expect(page.getByLabel('Поиск по названию, ID или контрагенту')).toHaveValue('');
  await expect(page.getByLabel('Моя роль', { exact: true })).toHaveValue('');
  await expect(page.locator('.deal-row').filter({ hasText: deal.id })).toBeVisible();
});

test('list pagination keeps its search and appends each visible deal exactly once', async ({ page, playwright }) => {
  const prefix = `UI pagination ${randomUUID()}`;
  const request = await apiFor(playwright, 'customer');
  const createdIds: string[] = [];
  try {
    for (let index = 0; index < 21; index += 1) {
      const response = await postDraft(request, draftInput({ title: `${prefix} ${String(index).padStart(2, '0')}` }));
      expect(response.status()).toBe(201);
      createdIds.push((await response.json()).id);
    }
  } finally { await request.dispose(); }
  await openDeals(page);
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(prefix);
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  await expect(page.locator('.deal-row')).toHaveCount(20);
  const nextPage = page.waitForResponse((response) => response.url().includes('/api/v1/deals?') && new URL(response.url()).searchParams.has('cursor'));
  await page.getByRole('button', { name: 'Показать ещё', exact: true }).click();
  expect(new URL((await nextPage).url()).searchParams.get('q')).toBe(prefix);
  await expect(page.locator('.deal-row')).toHaveCount(21);
  const shownIds = await page.locator('.deal-row-main > code').allTextContents();
  expect(new Set(shownIds).size).toBe(21);
  expect([...shownIds].sort()).toEqual(createdIds.sort());
  await expect(page.getByRole('button', { name: 'Показать ещё', exact: true })).toHaveCount(0);
});

test('list errors retry real reads and a delayed response from an old view cannot replace fresh results', async ({ page, playwright }) => {
  const prefix = `UI stale ${randomUUID()}`;
  const first = await createFixture(playwright, `${prefix} first`);
  const second = await createFixture(playwright, `${prefix} second`);
  await page.route('**/api/v1/deals?*', async (route) => route.fulfill({ status: 503, json: { error: { code: 'TEMPORARILY_UNAVAILABLE', message: 'Synthetic list failure' } } }));
  await openDeals(page);
  await expect(page.getByRole('alert')).toContainText('Сервис временно недоступен');
  await expect(page.locator('.deal-row')).toHaveCount(0);
  await page.unroute('**/api/v1/deals?*');
  await page.getByRole('button', { name: 'Повторить запрос' }).click();
  await expect(page.locator('.deal-row').filter({ hasText: first.id })).toBeVisible();
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  let fetched = false;
  await page.route('**/api/v1/deals?*', async (route) => {
    if (new URL(route.request().url()).searchParams.get('q') !== first.title) return route.continue();
    const response = await route.fetch();
    fetched = true;
    await gate;
    await route.fulfill({ response });
  });
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(first.title);
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  await expect.poll(() => fetched).toBe(true);
  await page.getByRole('button', { name: 'Контрагенты', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Каталог контрагентов' })).toBeVisible();
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await expect(page.locator('.deal-row').filter({ hasText: second.id })).toBeVisible();
  const delayedResponse = page.waitForResponse((response) => response.url().includes('/api/v1/deals?') && new URL(response.url()).searchParams.get('q') === first.title);
  release();
  await (await delayedResponse).finished();
  // Wait for the response's browser render turn before checking that its stale data was ignored.
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  await expect(page.locator('.deal-row').filter({ hasText: first.id })).toBeVisible();
  await expect(page.locator('.deal-row').filter({ hasText: second.id })).toBeVisible();
  await expect(page.getByLabel('Поиск по названию, ID или контрагенту')).toHaveValue('');
});
