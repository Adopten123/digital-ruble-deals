import { randomUUID } from 'node:crypto';
import { test, expect, apiFor } from '../support.ts';

test('organisation catalogue reveals only public fields and operator has no account', async ({ playwright, actors }) => {
  for (const role of ['customer', 'supplier', 'outsider', 'operator'] as const) {
    const client = await apiFor(playwright, role);
    try {
      const response = await client.get('/api/v1/organisations', { params: { limit: 100 } });
      expect(response.status()).toBe(200);
      const page = await response.json();
      expect(page.items.length).toBeGreaterThanOrEqual(3);
      for (const item of page.items) {
        expect(Object.keys(item).sort()).toEqual(['id', 'is_demo', 'kind', 'name']);
        expect(item).toMatchObject({ id: expect.any(String), name: expect.any(String), is_demo: true });
        expect(['COMPANY', 'IP']).toContain(item.kind);
      }
      const ownResponse = await client.get('/api/v1/organisations/me');
      if (role === 'operator') {
        expect(ownResponse.status()).toBe(403);
        expect((await ownResponse.json()).error.code).toBe('FORBIDDEN');
      } else {
        expect(ownResponse.status()).toBe(200);
        const own = await ownResponse.json();
        expect(own.organisation).toEqual(actors[role].organisation);
        const { total_kopecks: total, reserved_kopecks: reserved, available_kopecks: available } = own.account;
        for (const value of [total, reserved, available]) expect(value).toMatch(/^(0|[1-9][0-9]*)$/);
        expect(BigInt(total) - BigInt(reserved)).toBe(BigInt(available));
        expect(own.account.currency).toBe('RUB');
        if (role === 'outsider') expect([total, reserved, available]).toEqual(['0', '0', '0']);
      }
    } finally {
      await client.dispose();
    }
  }
});

test('catalogue search and signed pagination are stable and filter-bound', async ({ playwright, actors }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const firstResponse = await client.get('/api/v1/organisations', { params: { limit: 1 } });
    expect(firstResponse.status()).toBe(200);
    const first = await firstResponse.json();
    expect(first.items).toHaveLength(1);
    expect(first.next_cursor).toEqual(expect.any(String));
    const secondResponse = await client.get('/api/v1/organisations', { params: { limit: 1, cursor: first.next_cursor } });
    expect(secondResponse.status()).toBe(200);
    const second = await secondResponse.json();
    expect(second.items).toHaveLength(1);
    expect(second.items[0].id).not.toBe(first.items[0].id);
    const invalidCursors: Record<string, string | number>[] = [
      { limit: 1, cursor: `${first.next_cursor}x` },
      { limit: 1, cursor: first.next_cursor, q: 'different-search' },
    ];
    for (const params of invalidCursors) {
      const response = await client.get('/api/v1/organisations', { params });
      expect(response.status()).toBe(400);
      expect((await response.json()).error.code).toBe('INVALID_REQUEST');
    }
    const search = await client.get('/api/v1/organisations', { params: { q: actors.supplier.organisation!.id } });
    expect(search.status()).toBe(200);
    expect((await search.json()).items.map((item: { id: string }) => item.id)).toEqual([actors.supplier.organisation!.id]);
    const empty = await client.get('/api/v1/organisations', { params: { q: `missing-${randomUUID()}` } });
    expect(await empty.json()).toEqual({ items: [], next_cursor: null });
  } finally {
    await client.dispose();
  }
});

test('catalogue rejects invalid bounds and treats SQL wildcard input literally', async ({ playwright }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const invalidQueries: Record<string, string | number>[] = [{ limit: 0 }, { limit: 101 }, { limit: 'NaN' }, { q: 'я'.repeat(201) }, { cursor: 'forged' }];
    for (const params of invalidQueries) {
      const response = await client.get('/api/v1/organisations', { params });
      expect(response.status()).toBe(400);
      expect((await response.json()).error.code).toBe('INVALID_REQUEST');
    }
    for (const q of [`%_${randomUUID()}`, `' OR '1'='1 ${randomUUID()}`]) {
      const response = await client.get('/api/v1/organisations', { params: { q } });
      expect(response.status()).toBe(200);
      expect(await response.json()).toEqual({ items: [], next_cursor: null });
    }
  } finally {
    await client.dispose();
  }
});
