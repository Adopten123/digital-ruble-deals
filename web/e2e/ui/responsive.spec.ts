import { randomUUID } from 'node:crypto';
import { test, expect, apiFor, draftInput, postDraft } from '../support.ts';

test('375px cabinet and catalog remain readable, keyboard search works, and no browser error occurs', async ({ page }) => {
  const failures: string[] = [];
  page.on('pageerror', (error) => failures.push(error.message));
  page.on('console', (message) => { if (message.type() === 'error') failures.push(message.text()); });
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Учебный счёт' })).toBeVisible();
  await expect(page.locator('.balance-card')).toHaveCount(3);
  const viewport = page.viewportSize()!;
  expect(viewport.width).toBe(375);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(viewport.width);
  for (const card of await page.locator('.balance-card').all()) {
    const box = await card.boundingBox();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.width).toBeGreaterThan(280);
    expect(box!.x + box!.width).toBeLessThanOrEqual(viewport.width);
  }
  await page.getByRole('button', { name: 'Контрагенты', exact: true }).click();
  await expect(page.locator('.counterparty').first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(viewport.width);
  await page.getByLabel('Название организации', { exact: true }).fill(`нет-результатов-🧵-${randomUUID()}`);
  await page.getByLabel('Название организации', { exact: true }).press('Enter');
  await expect(page.getByRole('heading', { name: 'Организации не найдены' })).toBeVisible();
  expect(failures).toEqual([]);
});

test('375px detail progress text and create form fit the viewport without horizontal scrolling', async ({ page, playwright }) => {
  const failures: string[] = [];
  page.on('pageerror', (error) => failures.push(error.message));
  const request = await apiFor(playwright, 'customer');
  const title = `Mobile deal ${randomUUID()}`;
  try {
    const created = await postDraft(request, draftInput({ title, description: 'Длинные, но читаемые учебные условия исполнения: весь согласованный результат передаётся заказчику целиком; частичные расчёты не выполняются.' }));
    expect(created.status()).toBe(201);
  } finally { await request.dispose(); }
  await page.goto('/');
  await page.getByRole('button', { name: 'Сделки', exact: true }).click();
  await expect(page.locator('.deal-row').filter({ hasText: title })).toBeVisible();
  await page.locator('.deal-row').filter({ hasText: title }).click();
  await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
  const progress = page.locator('.deal-progress');
  await expect(progress.getByRole('heading', { name: 'Условия сохранены. Средства свободны.' })).toBeVisible();
  const text = progress.locator('p');
  await expect(text).toContainText('создание черновика не означает согласие исполнителя');
  const textBox = await text.boundingBox();
  expect(textBox!.width).toBeGreaterThan(240);
  expect(textBox!.x).toBeGreaterThanOrEqual(0);
  expect(textBox!.x + textBox!.width).toBeLessThanOrEqual(375);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
  await page.getByRole('button', { name: '← К списку сделок' }).click();
  await page.getByRole('button', { name: 'Новая сделка', exact: true }).click();
  await expect(page.getByLabel('Название сделки', { exact: true })).toBeVisible();
  await expect(page.locator('.supplier-option').first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
  for (const field of await page.locator('.deal-form input, .deal-form textarea').all()) {
    const box = await field.boundingBox();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(375);
  }
  expect(failures).toEqual([]);
});
