import { createHash, randomUUID } from 'node:crypto';
import type { APIRequestContext, APIResponse } from '@playwright/test';
import { test, expect, apiFor, csrf, draftInput, postDraft, E2E_BASE_URL, API_BASE_URL } from '../support.ts';
import { syntheticPdf } from '../document-fixtures.ts';

async function command(client: APIRequestContext, method: 'POST' | 'PATCH', path: string, data: unknown, key = randomUUID()) {
  return client.fetch(path, { method, data, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': key } });
}
async function ok(response: APIResponse, status = 200) {
  expect(response.status(), await response.text()).toBe(status);
  return response.json();
}
async function error(response: APIResponse, status: number, code: string) {
  expect(response.status(), await response.text()).toBe(status);
  expect(await response.json()).toMatchObject({ error: { code }, request_id: expect.any(String) });
}
async function generate(client: APIRequestContext, deal: { id: string; deal_version: number }, key = randomUUID()) {
  return command(client, 'POST', `/api/v1/deals/${deal.id}/documents/generate`, { expected_deal_version: deal.deal_version, template_id: 'printing-acceptance-v1' }, key);
}
async function upload(client: APIRequestContext, deal: { id: string; deal_version: number }, bytes = syntheticPdf(), key = randomUUID(), name = 'acceptance.pdf', mimeType = 'application/pdf') {
  return client.post(`/api/v1/deals/${deal.id}/documents`, {
    multipart: { expected_deal_version: String(deal.deal_version), file: { name, mimeType, buffer: bytes } },
    headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(client), 'Idempotency-Key': key },
  });
}
function acceptance(deal: { deal_version: number; document: { id: string; document_version: number; sha256: string } }) {
  return { deal_version: deal.deal_version, document_id: deal.document.id, document_version: deal.document.document_version, document_sha256: deal.document.sha256 };
}
async function read(client: APIRequestContext, id: string) { return ok(await client.get(`/api/v1/deals/${id}`)); }

test('draft editing creates immutable versions, preserves replay and rejects a changed old key', async ({ playwright }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  const outsider = await apiFor(playwright, 'outsider');
  try {
    const draft = await ok(await postDraft(customer), 201);
    const key = randomUUID();
    const patch = { expected_version: 1, title: `Edited ${randomUUID()}`, amount_kopecks: '9007199254740993' };
    const edited = await ok(await command(customer, 'PATCH', `/api/v1/deals/${draft.id}`, patch, key));
    expect(edited).toMatchObject({ id: draft.id, deal_version: 2, title: patch.title, amount_kopecks: patch.amount_kopecks, description: draft.description, document: null, agreements: [] });
    expect(await read(supplier, draft.id)).toEqual(edited);
    const noChange = await ok(await command(customer, 'PATCH', `/api/v1/deals/${draft.id}`, { expected_version: 2, title: patch.title }));
    expect(noChange.deal_version).toBe(2);
    const newer = await ok(await command(customer, 'PATCH', `/api/v1/deals/${draft.id}`, { expected_version: 2, description: 'Новая согласуемая редакция условий.' }));
    expect(newer.deal_version).toBe(3);
    expect(await ok(await command(customer, 'PATCH', `/api/v1/deals/${draft.id}`, patch, key))).toEqual(edited);
    expect((await read(customer, draft.id)).deal_version).toBe(3);
    await error(await command(customer, 'PATCH', `/api/v1/deals/${draft.id}`, { ...patch, title: 'Different body' }, key), 409, 'IDEMPOTENCY_CONFLICT');
    await error(await command(supplier, 'PATCH', `/api/v1/deals/${draft.id}`, { expected_version: 3, title: 'Forbidden' }), 403, 'FORBIDDEN');
    await error(await command(outsider, 'PATCH', `/api/v1/deals/${draft.id}`, { expected_version: 3, title: 'Hidden' }), 404, 'RESOURCE_NOT_FOUND');
  } finally { await Promise.all([customer.dispose(), supplier.dispose(), outsider.dispose()]); }
});

test('parallel edits serialize on the expected version and changing supplier revokes the old party', async ({ playwright, actors }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  const outsider = await apiFor(playwright, 'outsider');
  try {
    const draft = await ok(await postDraft(customer), 201);
    const token = await csrf(customer);
    const responses = await Promise.all(['first', 'second'].map(title => customer.patch(`/api/v1/deals/${draft.id}`, {
      data: { expected_version: 1, title }, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': token, 'Idempotency-Key': randomUUID() },
    })));
    expect(responses.map(response => response.status()).sort()).toEqual([200, 409]);
    await error(responses.find(response => response.status() === 409)!, 409, 'VERSION_CONFLICT');
    const latest = await read(customer, draft.id);
    const changed = await ok(await command(customer, 'PATCH', `/api/v1/deals/${draft.id}`, { expected_version: latest.deal_version, supplier_party_id: actors.outsider.organisation!.id }));
    expect(changed.deal_version).toBe(3);
    await error(await supplier.get(`/api/v1/deals/${draft.id}`), 404, 'RESOURCE_NOT_FOUND');
    expect(await read(outsider, draft.id)).toEqual(changed);
  } finally { await Promise.all([customer.dispose(), supplier.dispose(), outsider.dispose()]); }
});

test('generated and uploaded PDF versions preserve exact private bytes and a stable document ID', async ({ playwright }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  const outsider = await apiFor(playwright, 'outsider');
  try {
    const draft = await ok(await postDraft(customer, draftInput({ title: 'Учебный акт: печать и приёмка', description: 'Проверка кириллицы, условий и точных копеек.' })), 201);
    const key = randomUUID();
    const generated = await ok(await generate(customer, draft, key), 201);
    expect(generated.deal).toMatchObject({ deal_version: 2, status: 'DRAFT', document: generated.document, agreements: [] });
    const doc = generated.document;
    expect(doc).toMatchObject({ deal_id: draft.id, deal_version: 2, document_version: 1, content_type: 'application/pdf', sha256: expect.stringMatching(/^[a-f0-9]{64}$/) });
    const path = `/api/v1/documents/${doc.id}`;
    const view = await supplier.get(`${path}/view`, { params: { version: 1 } });
    expect(view.status()).toBe(200);
    expect(view.headers()['content-type']).toContain('application/pdf');
    expect(view.headers()['content-security-policy']).toContain('sandbox');
    const bytes = await view.body();
    expect(bytes.subarray(0, 5).toString()).toBe('%PDF-');
    expect(bytes.length).toBe(doc.size_bytes);
    expect(createHash('sha256').update(bytes).digest('hex')).toBe(doc.sha256);
    const download = await customer.get(`${path}/download`, { params: { version: 1 } });
    expect(download.headers()['content-disposition']).toContain('attachment');
    expect(await download.body()).toEqual(bytes);
    expect(await ok(await generate(customer, draft, key), 201)).toEqual(generated);
    const replacementBytes = syntheticPdf('Replacement PDF');
    const replacement = await ok(await upload(customer, generated.deal, replacementBytes), 201);
    expect(replacement.document.id).toBe(doc.id);
    expect(replacement.document.document_version).toBe(2);
    expect(replacement.deal.deal_version).toBe(3);
    expect(replacement.document.sha256).toBe(createHash('sha256').update(replacementBytes).digest('hex'));
    expect(await ok(await customer.get(path))).toEqual(replacement.document);
    expect(await ok(await customer.get(path, { params: { version: 1 } }))).toEqual(doc);
    expect(await (await supplier.get(`${path}/download`, { params: { version: 1 } })).body()).toEqual(bytes);
    for (const suffix of ['', '/view?version=1', '/download?version=2']) await error(await outsider.get(path + suffix), 404, 'RESOURCE_NOT_FOUND');
    await error(await customer.get(`${path}/view`), 400, 'INVALID_REQUEST');
    await error(await customer.get(`${path}/download?version=0`), 400, 'INVALID_REQUEST');
  } finally { await Promise.all([customer.dispose(), supplier.dispose(), outsider.dispose()]); }
});

test('two distinct parties agree to the exact current document and new PDF invalidates old agreements', async ({ playwright, actors }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  try {
    const draft = await ok(await postDraft(customer), 201);
    await error(await command(customer, 'POST', `/api/v1/deals/${draft.id}/agree`, { deal_version: 1, document_id: 'missing', document_version: 1, document_sha256: '0'.repeat(64) }), 409, 'DOCUMENT_REQUIRED');
    const attached = await ok(await generate(customer, draft), 201);
    const body = acceptance(attached.deal);
    await error(await command(supplier, 'POST', `/api/v1/deals/${draft.id}/agree`, { ...body, document_sha256: '0'.repeat(64) }), 409, 'DOCUMENT_MISMATCH');
    const agreedKey = randomUUID();
    const once = await ok(await command(customer, 'POST', `/api/v1/deals/${draft.id}/agree`, body, agreedKey));
    expect(once.status).toBe('DRAFT');
    expect(once.agreements).toHaveLength(1);
    expect(once.agreements[0]).toMatchObject({ organisation_id: actors.customer.organisation!.id, ...body });
    const sameParty = await ok(await command(customer, 'POST', `/api/v1/deals/${draft.id}/agree`, body));
    expect(sameParty.agreements).toHaveLength(1);
    expect(sameParty.status).toBe('DRAFT');
    const both = await ok(await command(supplier, 'POST', `/api/v1/deals/${draft.id}/agree`, body));
    expect(both.status).toBe('AGREED');
    expect(new Set(both.agreements.map((a: { organisation_id: string }) => a.organisation_id)).size).toBe(2);
    const replacement = await ok(await upload(customer, both, syntheticPdf('New accepted bytes')), 201);
    expect(replacement.deal).toMatchObject({ status: 'DRAFT', deal_version: 3, agreements: [] });
    expect(await ok(await command(customer, 'POST', `/api/v1/deals/${draft.id}/agree`, body, agreedKey))).toEqual(once);
    expect((await read(customer, draft.id)).agreements).toEqual([]);
    await error(await command(supplier, 'POST', `/api/v1/deals/${draft.id}/agree`, body), 409, 'VERSION_CONFLICT');
  } finally { await Promise.all([customer.dispose(), supplier.dispose()]); }
});

test('changing supplier cannot expose historical PDF bytes to the replacement or former supplier', async ({ playwright, actors }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  const replacement = await apiFor(playwright, 'outsider');
  try {
    const draft = await ok(await postDraft(customer), 201);
    const original = await ok(await generate(customer, draft), 201);
    const changed = await ok(await command(customer, 'PATCH', `/api/v1/deals/${draft.id}`, { expected_version: 2, supplier_party_id: actors.outsider.organisation!.id }));
    expect(changed.document).toBeNull();
    const path = `/api/v1/documents/${original.document.id}`;
    for (const client of [supplier, replacement]) await error(await client.get(`${path}/download?version=1`), 404, 'RESOURCE_NOT_FOUND');
    expect((await customer.get(`${path}/download?version=1`)).status()).toBe(200);
    const next = await ok(await generate(customer, changed), 201);
    expect(next.document.id).toBe(original.document.id);
    expect(next.document.document_version).toBe(2);
    expect((await replacement.get(`${path}/view?version=2`)).status()).toBe(200);
    await error(await replacement.get(`${path}/view?version=1`), 404, 'RESOURCE_NOT_FOUND');
    await error(await supplier.get(`${path}/view?version=2`), 404, 'RESOURCE_NOT_FOUND');
  } finally { await Promise.all([customer.dispose(), supplier.dispose(), replacement.dispose()]); }
});

test('rework creates one new version, clears the active PDF and permits editing without changing accounts', async ({ playwright }) => {
  const customer = await apiFor(playwright, 'customer');
  const supplier = await apiFor(playwright, 'supplier');
  try {
    const balances = await Promise.all([customer, supplier].map(async client => ok(await client.get('/api/v1/organisations/me'))));
    const draft = await ok(await postDraft(customer), 201);
    const attached = await ok(await generate(customer, draft), 201);
    const body = acceptance(attached.deal);
    await ok(await command(customer, 'POST', `/api/v1/deals/${draft.id}/agree`, body));
    const agreed = await ok(await command(supplier, 'POST', `/api/v1/deals/${draft.id}/agree`, body));
    await error(await command(customer, 'PATCH', `/api/v1/deals/${draft.id}`, { expected_version: agreed.deal_version, title: 'Requires rework first' }), 409, 'INVALID_STATE');
    const key = randomUUID(), rework = { deal_version: agreed.deal_version, reason: 'Уточнить объём партии перед резервом.' };
    const changed = await ok(await command(supplier, 'POST', `/api/v1/deals/${draft.id}/rework`, rework, key));
    expect(changed).toMatchObject({ status: 'DRAFT', deal_version: 3, document: null, agreements: [] });
    expect(await ok(await command(supplier, 'POST', `/api/v1/deals/${draft.id}/rework`, rework, key))).toEqual(changed);
    const edited = await ok(await command(customer, 'PATCH', `/api/v1/deals/${draft.id}`, { expected_version: 3, description: 'Уточнённый объём партии.' }));
    expect(edited.deal_version).toBe(4);
    expect((await ok(await supplier.get(`/api/v1/documents/${attached.document.id}`, { params: { version: 1 } }))).sha256).toBe(attached.document.sha256);
    expect(await Promise.all([customer, supplier].map(async client => ok(await client.get('/api/v1/organisations/me'))))).toEqual(balances);
  } finally { await Promise.all([customer.dispose(), supplier.dispose()]); }
});

test('PDF uploads reject active content, wrong media, malformed files and oversize without changing the deal', async ({ playwright }) => {
  const customer = await apiFor(playwright, 'customer');
  try {
    const draft = await ok(await postDraft(customer), 201);
    for (const [bytes, name, mime] of [[Buffer.from('%PDF-not-a-document'), 'broken.pdf', 'application/pdf'], [syntheticPdf('Active', true), 'active.pdf', 'application/pdf'], [syntheticPdf(), 'wrong.txt', 'application/pdf'], [syntheticPdf(), 'wrong.pdf', 'text/plain']] as const) {
      await error(await upload(customer, draft, bytes, randomUUID(), name, mime), 422, 'INVALID_DOCUMENT');
    }
    await error(await upload(customer, draft, Buffer.alloc(5 * 1024 * 1024 + 1, 32)), 413, 'PAYLOAD_TOO_LARGE');
    expect(await read(customer, draft.id)).toEqual(draft);
  } finally { await customer.dispose(); }
});

test('mutations require current actor, CSRF, a key and valid typed versions', async ({ playwright, actors }) => {
  const customer = await apiFor(playwright, 'customer');
  const anonymous = await playwright.request.newContext({ baseURL: API_BASE_URL, storageState: { cookies: [], origins: [] } });
  try {
    const draft = await ok(await postDraft(customer), 201);
    const path = `/api/v1/deals/${draft.id}`;
    await error(await anonymous.patch(path, { data: { expected_version: 1, title: 'Anonymous' } }), 401, 'AUTH_REQUIRED');
    await error(await customer.patch(path, { data: { expected_version: 1, title: 'No CSRF' }, headers: { Origin: E2E_BASE_URL, 'Idempotency-Key': randomUUID() } }), 403, 'CSRF_INVALID');
    await error(await customer.patch(path, { data: { expected_version: 1, title: 'No key' }, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(customer) } }), 400, 'IDEMPOTENCY_KEY_REQUIRED');
    await error(await customer.patch(path, { data: { expected_version: 1, title: 'Wrong displayed account' }, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(customer), 'Idempotency-Key': randomUUID(), 'X-Expected-User-ID': actors.supplier.user.id } }), 409, 'SESSION_CHANGED');
    for (const data of [{ expected_version: '1', title: 'Wrong type' }, { expected_version: 1, title: null }, { expected_version: 1, unknown: true }, { expected_version: 0, title: 'Invalid version' }]) await error(await command(customer, 'PATCH', path, data), 400, 'INVALID_REQUEST');
    expect(await read(customer, draft.id)).toEqual(draft);
  } finally { await Promise.all([customer.dispose(), anonymous.dispose()]); }
});
