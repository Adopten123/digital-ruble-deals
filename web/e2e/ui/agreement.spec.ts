import { createHash, randomUUID } from 'node:crypto';
import type { APIRequestContext, Page, Request } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, E2E_BASE_URL } from '../support.ts';
import { syntheticPdf } from '../document-fixtures.ts';
import type { Deal } from '../../src/lib/api.ts';

async function fixture(client: APIRequestContext): Promise<Deal> {
  const response = await postDraft(client, draftInput({ title: `Agreement UI ${randomUUID()}` }));
  expect(response.status()).toBe(201);
  return response.json();
}
async function command(client: APIRequestContext, path: string, input: unknown, method = 'POST') {
  return client.fetch(path, { method, data: input, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': randomUUID() } });
}
async function openDeal(page: Page, dealId: string) {
  await page.goto('/');
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await page.getByLabel('Поиск по названию, ID или контрагенту').fill(dealId);
  await page.getByRole('search', { name: 'Поиск сделок' }).getByRole('button', { name: 'Найти', exact: true }).click();
  await page.locator('.deal-row').filter({ hasText: dealId }).click();
  await expect(page.getByRole('region', { name: 'Документ и согласование' })).toBeVisible();
}
async function agree(page: Page, id: string) {
  await expect(page.getByRole('button', { name: 'Согласовать документ', exact: true })).toBeDisabled();
  await page.getByRole('checkbox', { name: /Согласен с условиями версии/ }).check();
  const received = page.waitForResponse(response => response.url().endsWith(`/deals/${id}/agree`) && response.request().method() === 'POST');
  await page.getByRole('button', { name: 'Согласовать документ', exact: true }).click();
  expect((await received).status()).toBe(200);
  await expect(page.getByRole('button', { name: 'Согласовать документ', exact: true })).toHaveCount(0);
}
function sent(request: Request) {
  return { body: request.postData(), key: request.headers()['idempotency-key'], actor: request.headers()['x-expected-user-id'] };
}

test('editing and a PDF require two explicit consents; either party can return the agreed deal for rework', async ({ page, browser, playwright, actors }) => {
  const client = await apiFor(playwright, 'customer');
  const supplierContext = await browser.newContext({ baseURL: test.info().project.use.baseURL, storageState: actors.supplier.statePath });
  try {
    const draft = await fixture(client);
    await openDeal(page, draft.id);
    await page.getByRole('button', { name: 'Редактировать условия', exact: true }).click();
    const newTitle = `Updated agreement ${randomUUID()}`;
    await page.getByLabel('Название сделки', { exact: true }).fill(newTitle);
    await page.getByLabel('Полная сумма, ₽', { exact: true }).fill('123456,78');
    await page.getByRole('button', { name: 'Сохранить новую версию', exact: true }).click();
    await expect(page.getByRole('heading', { name: newTitle, exact: true })).toBeVisible();
    await expect(page.locator('.deal-detail-heading')).toContainText('Версия 2');
    await expect(page.locator('.deal-condition-amount strong')).toHaveText('123 456,78 ₽');
    const generated = page.waitForResponse(response => response.url().endsWith(`/deals/${draft.id}/documents/generate`));
    await page.getByRole('button', { name: 'Сформировать акт', exact: true }).click();
    expect((await generated).status()).toBe(201);
    await expect(page.locator('.document-facts')).toContainText('Условия 3 · PDF 1');
    const actual = await (await client.get(`/api/v1/deals/${draft.id}`)).json() as Deal;
    await expect(page.locator('.document-hash')).toHaveText(actual.document!.sha256);
    const loaded = page.waitForResponse(response => response.url().includes(`/documents/${actual.document!.id}/view?version=1`));
    await page.getByRole('button', { name: 'Открыть PDF', exact: true }).click();
    expect((await loaded).status()).toBe(200);
    await expect(page.getByRole('link', { name: 'Открыть загруженный PDF ↗', exact: true })).toHaveAttribute('href', /^blob:/);
    const downloaded = page.waitForResponse(response => response.url().includes(`/documents/${actual.document!.id}/download?version=1`));
    await page.getByRole('button', { name: 'Скачать PDF', exact: true }).click();
    expect((await downloaded).status()).toBe(200);
    await expect(page.getByRole('link', { name: 'Сохранить загруженный PDF', exact: true })).toHaveAttribute('download', actual.document!.file_name);
    await agree(page, draft.id);
    await expect(page.locator('.agreement-consents .agreement-confirmed')).toHaveCount(1);
    expect((await (await client.get(`/api/v1/deals/${draft.id}`)).json()).status).toBe('DRAFT');
    const supplier = await supplierContext.newPage();
    await openDeal(supplier, draft.id);
    await expect(supplier.getByRole('button', { name: 'Редактировать условия', exact: true })).toHaveCount(0);
    await expect(supplier.getByRole('button', { name: 'Сформировать акт', exact: true })).toHaveCount(0);
    await agree(supplier, draft.id);
    await expect(supplier.getByText('Обе стороны согласовали документ', { exact: true })).toBeVisible();
    await expect(supplier.locator('.agreement-consents .agreement-confirmed')).toHaveCount(2);
    await supplier.getByRole('button', { name: 'Вернуть на доработку', exact: true }).click();
    await supplier.getByLabel('Причина доработки', { exact: true }).fill('Уточнить объём учебных печатных работ.');
    await supplier.getByRole('button', { name: 'Подтвердить возврат на доработку', exact: true }).click();
    await expect(supplier.locator('.deal-detail-heading')).toContainText('Версия 4');
    await expect(supplier.locator('.agreement-document')).toContainText('PDF ещё не добавлен');
    await expect(supplier.locator('.agreement-consents .agreement-confirmed')).toHaveCount(0);
    const reworked = await (await client.get(`/api/v1/deals/${draft.id}`)).json();
    expect(reworked).toMatchObject({ status: 'DRAFT', deal_version: 4, document: null, agreements: [], reserve: null, payment_id: null });
  } finally { await supplierContext.close(); await client.dispose(); }
});

test('a committed JSON command survives reload and replay uses its old body while GET shows newer actual conditions', async ({ page, playwright, actors }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const draft = await fixture(client);
    await openDeal(page, draft.id);
    const commands: ReturnType<typeof sent>[] = [];
    await page.route(`**/api/v1/deals/${draft.id}/documents/generate`, async route => {
      commands.push(sent(route.request()));
      if (commands.length === 1) {
        const response = await route.fetch();
        expect(response.status()).toBe(201);
        await route.abort('failed');
      } else await route.continue();
    });
    await page.getByRole('button', { name: 'Сформировать акт', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('Нет связи с сервером');
    const storageKey = `poaktu:pending-agreement:${actors.customer.organisation!.id}:${draft.id}`;
    const saved = await page.evaluate(key => sessionStorage.getItem(key), storageKey);
    expect(JSON.parse(saved!).input).toEqual({ expected_deal_version: 1, template_id: 'printing-acceptance-v1' });
    const newerTitle = `Newer actual conditions ${randomUUID()}`;
    const changed = await command(client, `/api/v1/deals/${draft.id}`, { expected_version: 2, title: newerTitle }, 'PATCH');
    expect(changed.status()).toBe(200);
    await page.reload();
    await openDeal(page, draft.id);
    await expect(page.getByText('Команда ожидает подтверждения: формирование акта', { exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: newerTitle, exact: true })).toBeVisible();
    expect(commands).toHaveLength(1);
    await page.getByRole('button', { name: 'Проверить результат команды', exact: true }).click();
    await expect(page.getByText('Команда подтверждена сервером.', { exact: true })).toBeVisible();
    await expect(page.locator('.deal-detail-heading')).toContainText('Версия 3');
    await expect(page.locator('.agreement-document')).toContainText('PDF ещё не добавлен');
    expect(commands).toHaveLength(2);
    expect(commands[1]).toEqual(commands[0]);
    expect(commands[0].actor).toBe(actors.customer.user.id);
    expect(await page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBeNull();
    expect((await (await client.get(`/api/v1/deals/${draft.id}`)).json()).deal_version).toBe(3);
  } finally { await client.dispose(); }
});

test('a lost PDF upload survives reload, rejects changed bytes locally and replays identical bytes with its original filename', async ({ page, playwright, actors }, testInfo) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const draft = await fixture(client);
    await openDeal(page, draft.id);
    const bytes = syntheticPdf('Original upload bytes');
    const name = `${'original-'.repeat(16)}acceptance.pdf`;
    const commands: { key: string | undefined; actor: string | undefined; contentType: string; body: Buffer }[] = [];
    await page.route(`**/api/v1/deals/${draft.id}/documents`, async route => {
      const request = route.request();
      commands.push({ key: request.headers()['idempotency-key'], actor: request.headers()['x-expected-user-id'], contentType: request.headers()['content-type'], body: request.postDataBuffer()! });
      if (commands.length === 1) {
        const response = await route.fetch();
        expect(response.status()).toBe(201);
        await route.abort('failed');
      } else await route.continue();
    });
    await page.getByLabel('PDF-документ', { exact: true }).setInputFiles({ name, mimeType: 'application/pdf', buffer: bytes });
    await page.getByRole('button', { name: 'Загрузить PDF', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('Нет связи с сервером');
    const storageKey = `poaktu:pending-agreement:${actors.customer.organisation!.id}:${draft.id}`;
    const saved = await page.evaluate(key => sessionStorage.getItem(key), storageKey);
    expect(JSON.parse(saved!).input).toMatchObject({ expected_deal_version: 1, file_name: name, size_bytes: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex') });
    await page.reload();
    await page.setViewportSize({ width: 375, height: 812 });
    await openDeal(page, draft.id);
    await expect(page.getByRole('button', { name: 'Проверить результат команды', exact: true })).toBeDisabled();
    await expect(page.getByRole('button', { name: 'Сформировать акт', exact: true })).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
    await page.getByLabel('Исходный PDF для проверки результата', { exact: true }).setInputFiles({ name, mimeType: 'application/pdf', buffer: syntheticPdf('A different valid PDF') });
    await page.getByRole('button', { name: 'Проверить результат команды', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('Выбран другой PDF');
    expect(commands).toHaveLength(1);
    expect(await page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBe(saved);
    await testInfo.attach('mobile-pending-pdf-command', { body: await page.screenshot({ fullPage: true }), contentType: 'image/png' });
    await page.getByLabel('Исходный PDF для проверки результата', { exact: true }).setInputFiles({ name: 'renamed.pdf', mimeType: 'application/pdf', buffer: bytes });
    await page.getByRole('button', { name: 'Проверить результат команды', exact: true }).click();
    await expect(page.getByText('Команда подтверждена сервером.', { exact: true })).toBeVisible();
    await expect(page.locator('.document-facts')).toContainText('Условия 2 · PDF 1');
    expect(commands).toHaveLength(2);
    expect(commands[1].key).toBe(commands[0].key);
    expect(commands[1].actor).toBe(commands[0].actor);
    for (const sent of commands) {
      expect(sent.body.includes(bytes)).toBe(true);
      expect(sent.body.toString()).toContain(`filename="${name}"`);
      expect(sent.body.toString()).toContain('name="expected_deal_version"\r\n\r\n1');
    }
    expect(await page.evaluate(key => sessionStorage.getItem(key), storageKey)).toBeNull();
    expect((await (await client.get(`/api/v1/deals/${draft.id}`)).json()).deal_version).toBe(2);
  } finally { await client.dispose(); }
});

test('a stale agreement is rejected clearly, refreshed consent binds the current PDF and mobile controls fit', async ({ page, playwright }, testInfo) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const draft = await fixture(client);
    const generated = await command(client, `/api/v1/deals/${draft.id}/documents/generate`, { expected_deal_version: 1, template_id: 'printing-acceptance-v1' });
    expect(generated.status()).toBe(201);
    await openDeal(page, draft.id);
    await expect(page.locator('.document-facts')).toContainText('Условия 2 · PDF 1');
    const newer = await command(client, `/api/v1/deals/${draft.id}/documents/generate`, { expected_deal_version: 2, template_id: 'printing-acceptance-v1' });
    expect(newer.status()).toBe(201);
    const received = page.waitForResponse(response => response.url().endsWith(`/deals/${draft.id}/agree`));
    await page.getByRole('checkbox', { name: /Согласен с условиями версии/ }).check();
    await page.getByRole('button', { name: 'Согласовать документ', exact: true }).click();
    expect((await received).status()).toBe(409);
    await expect(page.getByRole('alert')).toContainText('Условия или документ уже изменились');
    await page.getByRole('button', { name: 'Обновить', exact: true }).click();
    await expect(page.locator('.document-facts')).toContainText('Условия 3 · PDF 2');
    await expect(page.getByRole('checkbox', { name: /Согласен с условиями версии 3 и PDF версии 2/ })).not.toBeChecked();
    await expect(page.getByRole('button', { name: 'Согласовать документ', exact: true })).toBeDisabled();
    let uploadPosts = 0;
    page.on('request', request => { if (request.method() === 'POST' && request.url().endsWith(`/deals/${draft.id}/documents`)) uploadPosts++; });
    await page.getByLabel('PDF-документ', { exact: true }).setInputFiles({ name: 'fake.pdf', mimeType: 'application/pdf', buffer: Buffer.from('<html>not PDF</html>') });
    await page.getByRole('button', { name: 'Загрузить PDF', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('Файл не похож на PDF');
    expect(uploadPosts).toBe(0);
    await page.setViewportSize({ width: 375, height: 812 });
    const panel = page.getByRole('region', { name: 'Документ и согласование' });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
    for (const control of await panel.locator('button, input, textarea, a').all()) {
      if (!await control.isVisible()) continue;
      const box = await control.boundingBox();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(375);
    }
    await testInfo.attach('mobile-current-document-consent', { body: await page.screenshot({ fullPage: true }), contentType: 'image/png' });
    await agree(page, draft.id);
    await expect(page.locator('.agreement-consents .agreement-confirmed')).toHaveCount(1);
  } finally { await client.dispose(); }
});
