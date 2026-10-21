import { randomUUID } from 'node:crypto';
import type { APIRequestContext, APIResponse } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, API_BASE_URL, E2E_BASE_URL } from '../support.ts';

async function created(response: APIResponse) {
  expect(response.status()).toBe(201);
  return response.json();
}

async function apiError(response: APIResponse, status: number, code: string) {
  expect(response.status()).toBe(status);
  const body = await response.json();
  expect(body).toMatchObject({ error: { code, message: expect.any(String) }, request_id: expect.any(String) });
  return body;
}

async function query(client: APIRequestContext, params: Record<string, string | number>) {
  const response = await client.get('/api/v1/deals', { params });
  expect(response.status()).toBe(200);
  return response.json();
}

test('a new draft is persisted and both parties read the exact same safe representation', async ({ playwright, actors }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  try {
    const accountsBefore = await Promise.all([customer, supplier].map(async client => {
      const response = await client.get('/api/v1/organisations/me');
      expect(response.status()).toBe(200);
      return response.json();
    }));
    const input = draftInput();
    const draft = await created(await postDraft(customer, input));
    expect(draft).toMatchObject({
      ...input, deadline_at: expect.any(String), id: expect.any(String), deal_version: 1, status: 'DRAFT',
      customer_party_id: actors.customer.organisation!.id,
      customer: { id: actors.customer.organisation!.id, name: actors.customer.organisation!.name },
      supplier: { id: actors.supplier.organisation!.id, name: actors.supplier.organisation!.name },
      document: null, agreements: [], signatures: [], reserve: null, payment_id: null,
      dispute: null, cancellation_request: null,
    });
    for (const field of ['created_at', 'updated_at', 'deadline_at']) {
      expect(draft[field]).toMatch(/Z$/);
      expect(Number.isNaN(Date.parse(draft[field]))).toBe(false);
    }
    expect(Date.parse(draft.deadline_at)).toBe(Date.parse(input.deadline_at));
    expect(draft.updated_at).toBe(draft.created_at);
    expect(Object.keys(draft.customer).sort()).toEqual(['id', 'kind', 'name']);
    expect(Object.keys(draft.supplier).sort()).toEqual(['id', 'kind', 'name']);
    for (const client of [customer, supplier]) {
      const response = await client.get(`/api/v1/deals/${encodeURIComponent(draft.id)}`);
      expect(response.status()).toBe(200);
      expect(await response.json()).toEqual(draft);
      expect((await query(client, { q: draft.id })).items).toEqual([draft]);
    }
    const accountsAfter = await Promise.all([customer, supplier].map(async client => {
      const response = await client.get('/api/v1/organisations/me');
      expect(response.status()).toBe(200);
      return response.json();
    }));
    expect(accountsAfter).toEqual(accountsBefore);
  } finally {
    await customer.dispose();
    await supplier.dispose();
  }
});

test('foreign organisations and operator cannot infer a private draft', async ({ playwright }) => {
  const customer = await apiFor(playwright, 'customer');
  const outsider = await apiFor(playwright, 'outsider');
  const operator = await apiFor(playwright, 'operator');
  try {
    const draft = await created(await postDraft(customer));
    for (const client of [outsider, operator]) {
      const hidden = await apiError(await client.get(`/api/v1/deals/${draft.id}`), 404, 'RESOURCE_NOT_FOUND');
      const missing = await apiError(await client.get(`/api/v1/deals/missing-${randomUUID()}`), 404, 'RESOURCE_NOT_FOUND');
      expect(hidden.error).toEqual(missing.error);
    }
    expect(await query(outsider, { q: draft.id })).toEqual({ items: [], next_cursor: null });
    await apiError(await operator.get('/api/v1/deals'), 403, 'FORBIDDEN');
    await apiError(await postDraft(operator), 403, 'FORBIDDEN');
  } finally {
    await customer.dispose();
    await outsider.dispose();
    await operator.dispose();
  }
});

test('same key replays a persisted result across HTTP contexts; changed body conflicts', async ({ playwright }) => {
  const first = await apiFor(playwright, 'customer');
  const second = await apiFor(playwright, 'customer');
  const anonymous = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    const input = draftInput();
    const key = randomUUID();
    const original = await postDraft(first, input, key);
    expect(original.status()).toBe(201);
    const originalText = await original.text();
    const replay = await postDraft(second, input, key);
    expect(replay.status()).toBe(201);
    expect(await replay.text()).toBe(originalText);
    await apiError(await postDraft(second, { ...input, title: `${input.title} changed` }, key), 409, 'IDEMPOTENCY_CONFLICT');
    const draft = JSON.parse(originalText);
    expect((await query(first, { q: input.title })).items.map((item: { id: string }) => item.id)).toEqual([draft.id]);
    await apiError(await anonymous.post('/api/v1/deals', {
      data: input, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(anonymous), 'Idempotency-Key': key },
    }), 401, 'AUTH_REQUIRED');
  } finally {
    await first.dispose();
    await second.dispose();
    await anonymous.dispose();
  }
});

test('concurrent duplicate commands create exactly one draft', async ({ playwright }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const input = draftInput();
    const key = randomUUID();
    const responses = await Promise.all(Array.from({ length: 8 }, () => postDraft(client, input, key)));
    for (const response of responses) expect(response.status()).toBe(201);
    const bodies = await Promise.all(responses.map(response => response.text()));
    expect(new Set(bodies).size).toBe(1);
    const draft = JSON.parse(bodies[0]);
    expect((await query(client, { q: input.title })).items.map((item: { id: string }) => item.id)).toEqual([draft.id]);
  } finally {
    await client.dispose();
  }
});

test('idempotency scope is the acting organisation and canonical body ignores insignificant formatting', async ({ playwright, actors }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  try {
    const key = randomUUID();
    const input = draftInput({ deadline_at: new Date(Date.now() + 7 * 86400000).toISOString().replace(/\.\d{3}Z$/, '.123456789Z') });
    const original = await created(await postDraft(customer, { ...input, title: `  ${input.title}  ` }, key));
    const canonical = await created(await postDraft(customer, { ...input, deadline_at: input.deadline_at.replace('789Z', '001Z') }, key));
    expect(canonical).toEqual(original);
    expect(canonical.title).toBe(input.title);
    expect(canonical.deadline_at).toMatch(/\.123456Z$/);
    const opposite = await created(await postDraft(supplier, { ...input, supplier_party_id: actors.customer.organisation!.id }, key));
    expect(opposite.id).not.toBe(original.id);
    expect(opposite.customer_party_id).toBe(actors.supplier.organisation!.id);
  } finally {
    await customer.dispose();
    await supplier.dispose();
  }
});

test('semantic failures are replayed, cannot be replaced with a corrected body and create no draft', async ({ playwright }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const key = randomUUID();
    const invalid = draftInput({ deadline_at: '2020-01-01T00:00:00Z' });
    const first = await postDraft(client, invalid, key);
    await apiError(first, 422, 'DEADLINE_PASSED');
    const replay = await postDraft(client, invalid, key);
    expect(replay.status()).toBe(422);
    expect(await replay.text()).toBe(await first.text());
    expect(await query(client, { q: invalid.title })).toEqual({ items: [], next_cursor: null });
    const corrected = { ...invalid, deadline_at: draftInput().deadline_at };
    await apiError(await postDraft(client, corrected, key), 409, 'IDEMPOTENCY_CONFLICT');
    expect((await postDraft(client, corrected, randomUUID())).status()).toBe(201);
  } finally {
    await client.dispose();
  }
});

test('amounts stay exact through the HTTP and PostgreSQL roundtrip up to MaxInt64', async ({ playwright }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const before = await (await client.get('/api/v1/organisations/me')).json();
    for (const amount of ['1', '9007199254740993', '9223372036854775807']) {
      const draft = await created(await postDraft(client, draftInput({ amount_kopecks: amount })));
      expect(draft.amount_kopecks).toBe(amount);
      const fetched = await client.get(`/api/v1/deals/${draft.id}`);
      expect(fetched.status()).toBe(200);
      expect((await fetched.json()).amount_kopecks).toBe(amount);
    }
    for (const amount of ['0', '-1', '+1', '01', '1.00', '1e2', ' 1', '9223372036854775808']) {
      await apiError(await postDraft(client, draftInput({ amount_kopecks: amount })), 400, 'INVALID_AMOUNT');
    }
    const after = await (await client.get('/api/v1/organisations/me')).json();
    expect(after).toEqual(before);
  } finally {
    await client.dispose();
  }
});

test('text boundaries, parties, currency and deadlines are validated', async ({ playwright, actors }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const valid = await created(await postDraft(client, draftInput({ title: 'я'.repeat(200), description: 'д'.repeat(5000) })));
    expect(valid.title).toHaveLength(200);
    expect(valid.description).toHaveLength(5000);
    const invalidInputs: Record<string, unknown>[] = [
      { title: '' }, { title: 'я'.repeat(201) }, { title: 'hidden\u0001control' },
      { description: '   ' }, { description: 'д'.repeat(5001) },
      { supplier_party_id: actors.customer.organisation!.id }, { currency: 'USD' }, { deadline_at: 'tomorrow' },
    ];
    for (const overrides of invalidInputs) {
      await apiError(await postDraft(client, { ...draftInput(), ...overrides }), 400, 'INVALID_REQUEST');
    }
    await apiError(await postDraft(client, draftInput({ supplier_party_id: `missing-${randomUUID()}` })), 404, 'RESOURCE_NOT_FOUND');
  } finally {
    await client.dispose();
  }
});

test('list role, search and date filters remain restricted to the acting organisation', async ({ playwright, actors }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  const outsider = await apiFor(playwright, 'outsider');
  try {
    const prefix = `scope-${randomUUID()}`;
    const outgoing = await created(await postDraft(customer, draftInput({ title: `${prefix}-outgoing` })));
    const incoming = await created(await postDraft(supplier, draftInput({ title: `${prefix}-incoming`, supplier_party_id: actors.customer.organisation!.id })));
    const hidden = await created(await postDraft(outsider, draftInput({ title: `${prefix}-hidden` })));
    expect((await query(customer, { q: prefix, role: 'customer' })).items.map((item: { id: string }) => item.id)).toEqual([outgoing.id]);
    expect((await query(customer, { q: prefix, role: 'supplier' })).items.map((item: { id: string }) => item.id)).toEqual([incoming.id]);
    const all = await query(customer, { q: prefix, status: 'DRAFT' });
    expect(all.items.map((item: { id: string }) => item.id).sort()).toEqual([outgoing.id, incoming.id].sort());
    expect(all.items.some((item: { id: string }) => item.id === hidden.id)).toBe(false);
    const exactTime = await query(customer, { q: outgoing.id, created_from: outgoing.created_at, created_to: outgoing.created_at });
    expect(exactTime.items.map((item: { id: string }) => item.id)).toEqual([outgoing.id]);
    expect((await query(customer, { q: outgoing.id, created_to: '2020-01-01T00:00:00Z' })).items).toEqual([]);
    expect((await query(customer, { q: actors.supplier.organisation!.id })).items.some((item: { id: string }) => item.id === outgoing.id)).toBe(true);
  } finally {
    await customer.dispose();
    await supplier.dispose();
    await outsider.dispose();
  }
});

test('signed draft cursors cannot be tampered with, borrowed by another organisation or rebound to filters', async ({ playwright }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  try {
    const prefix = `pagination-${randomUUID()}`;
    const ids = [];
    for (let index = 0; index < 3; index++) {
      ids.push((await created(await postDraft(customer, draftInput({ title: `${prefix}-${index}` })))).id);
    }
    const first = await query(customer, { q: prefix, limit: 1 });
    expect(first.items.map((item: { id: string }) => item.id)).toEqual([ids[2]]);
    expect(first.next_cursor).toEqual(expect.any(String));
    const second = await query(customer, { q: prefix, limit: 1, cursor: first.next_cursor });
    expect(second.items.map((item: { id: string }) => item.id)).toEqual([ids[1]]);
    const third = await query(customer, { q: prefix, limit: 1, cursor: second.next_cursor });
    expect(third.items.map((item: { id: string }) => item.id)).toEqual([ids[0]]);
    expect(third.next_cursor).toBeNull();
    const rebound: Record<string, string | number>[] = [
      { q: prefix, limit: 1, cursor: `${first.next_cursor}x` },
      { q: `${prefix}-changed`, limit: 1, cursor: first.next_cursor },
      { q: prefix, limit: 2, cursor: first.next_cursor },
      { q: prefix, limit: 1, role: 'customer', cursor: first.next_cursor },
      { q: prefix, limit: 1, status: 'DRAFT', cursor: first.next_cursor },
    ];
    for (const params of rebound) {
      await apiError(await customer.get('/api/v1/deals', { params }), 400, 'INVALID_REQUEST');
    }
    await apiError(await supplier.get('/api/v1/deals', { params: { q: prefix, limit: 1, cursor: first.next_cursor } }), 400, 'INVALID_REQUEST');
  } finally {
    await customer.dispose();
    await supplier.dispose();
  }
});

test('invalid list syntax and injection-shaped searches fail safely without broadening results', async ({ playwright }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    for (const suffix of [
      '?limit=0', '?limit=101', '?limit=NaN', '?limit=1&limit=2', '?unexpected=value',
      '?role=operator', '?status=UNKNOWN', '?created_from=tomorrow',
      '?created_from=2030-01-01T00:00:00Z&created_to=2020-01-01T00:00:00Z', '?q=%ZZ',
      `?q=${encodeURIComponent('я'.repeat(201))}`,
    ]) {
      await apiError(await client.get(`/api/v1/deals${suffix}`), 400, 'INVALID_REQUEST');
    }
    for (const q of [`%_${randomUUID()}`, `' OR '1'='1 ${randomUUID()}`]) {
      expect(await query(client, { q })).toEqual({ items: [], next_cursor: null });
    }
    const prefix = `literal-${randomUUID()}`;
    const literal = await created(await postDraft(client, draftInput({ title: `${prefix}-%_marker` })));
    await created(await postDraft(client, draftInput({ title: `${prefix}-XXmarker` })));
    expect((await query(client, { q: `${prefix}-%_marker` })).items.map((item: { id: string }) => item.id)).toEqual([literal.id]);
  } finally {
    await client.dispose();
  }
});

test('expired drafts read as REVIEW_REQUIRED while their original create result remains replayable', async ({ playwright }) => {
  const client = await apiFor(playwright, 'customer');
  try {
    const key = randomUUID();
    const input = draftInput({ deadline_at: new Date(Date.now() + 4000).toISOString() });
    const original = await postDraft(client, input, key);
    const draft = await created(original);
    expect(draft.status).toBe('DRAFT');
    await expect.poll(async () => {
      const response = await client.get(`/api/v1/deals/${draft.id}`);
      expect(response.status()).toBe(200);
      return (await response.json()).status;
    }, { timeout: 12000, intervals: [250, 500] }).toBe('REVIEW_REQUIRED');
    expect((await query(client, { q: draft.id, status: 'REVIEW_REQUIRED' })).items.map((item: { id: string }) => item.id)).toEqual([draft.id]);
    expect((await query(client, { q: draft.id, status: 'DRAFT' })).items).toEqual([]);
    const replay = await postDraft(client, input, key);
    expect(replay.status()).toBe(201);
    expect(await replay.text()).toBe(await original.text());
  } finally {
    await client.dispose();
  }
});
