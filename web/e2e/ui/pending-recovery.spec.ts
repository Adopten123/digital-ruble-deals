import { randomUUID } from 'node:crypto';
import { test, expect, draftInput } from '../support.ts';

test('semantically corrupt pending JSON blocks creation without sending or silently deleting it', async ({ page, actors }) => {
  const key = `poaktu:pending-deal:${actors.customer.organisation!.id}`;
  let posts = 0;
  page.on('request', request => { if (request.method() === 'POST' && request.url().endsWith('/api/v1/deals')) posts++; });
  await page.goto('/');
  await expect(page.locator('.balance-card')).toHaveCount(3);
  const input = draftInput();
  for (const value of ['', JSON.stringify({ key: randomUUID(), input: { ...input, deadline_at: 'not-a-date' }, supplier: { ...actors.supplier.organisation, is_demo: true } })]) {
    await page.evaluate(({ key, value }) => sessionStorage.setItem(key, value), { key, value });
    await page.reload();
    await page.getByRole('button', { name: 'Сделки', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('Не удалось прочитать сохранённый запрос создания');
    await expect(page.getByRole('button', { name: 'Новая сделка', exact: true })).toBeDisabled();
    expect(await page.evaluate(key => sessionStorage.getItem(key), key)).toBe(value);
  }
  expect(posts).toBe(0);
});
