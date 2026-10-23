import assert from 'node:assert/strict';
import test from 'node:test';
import { ApiError } from '../src/lib/api.ts';
import type { Deal } from '../src/lib/api.ts';
import { agreementStorageKey, agreeDealInput, definitiveAgreementFailure, MAX_PDF_BYTES, pdfIdentity, readPendingAgreement, removePendingAgreement, reserveDealInput, reworkDealInput, updateDealInput, verifyPendingPdf } from '../src/lib/agreement.ts';
import { localDateTime } from '../src/lib/deals.ts';

const deal = {
  id: 'deal-a', deal_version: 3, supplier_party_id: 'supplier-a', title: 'Печать', description: 'Печать по макету',
  amount_kopecks: '30000000', currency: 'RUB', deadline_at: '2027-01-05T12:00:00.000Z',
  document: { id: 'doc-a', deal_id: 'deal-a', deal_version: 3, document_version: 2, sha256: 'a'.repeat(64), file_name: 'акт.pdf', size_bytes: 512, content_type: 'application/pdf', created_at: '2026-10-22T00:00:00Z' },
} as Deal;
const key = 'a-fixed-command-key-123';
const now = Date.parse('2026-10-22T00:00:00Z');
const fields = { title: '  Новый акт  ', description: '  Уточнённые условия  ', amount: '150000,25', deadline: '2027-01-05T15:00:00+03:00' };

test('editing binds the displayed version, exact money and explicitly selected counterparty', () => {
  const input = updateDealInput(deal, { ...fields, supplierId: 'supplier-b' }, now);
  assert.deepEqual(input, { expected_version: 3, supplier_party_id: 'supplier-b', title: 'Новый акт', description: 'Уточнённые условия', amount_kopecks: '15000025', currency: 'RUB', deadline_at: '2027-01-05T12:00:00.000Z' });
  assert.equal(updateDealInput(deal, fields, now).supplier_party_id, 'supplier-a');
  const exactDeadline = { ...deal, deadline_at: '2027-01-05T12:00:31.123456Z' };
  assert.equal(updateDealInput(exactDeadline, { ...fields, deadline: localDateTime(exactDeadline.deadline_at) }, now).deadline_at, exactDeadline.deadline_at);
  assert.throws(() => updateDealInput(deal, { ...fields, amount: '92233720368547758,08' }, now));
  assert.throws(() => updateDealInput(deal, { ...fields, deadline: '2020-01-01T00:00:00Z' }, now));
});

test('agreement snapshots all document bindings; a missing or obsolete PDF cannot be agreed locally', () => {
  assert.deepEqual(agreeDealInput(deal), { deal_version: 3, document_id: 'doc-a', document_version: 2, document_sha256: 'a'.repeat(64) });
  assert.throws(() => agreeDealInput({ ...deal, document: null }), /PDF/);
  assert.throws(() => agreeDealInput({ ...deal, document: { ...deal.document!, deal_version: 2 } }), /PDF/);
  assert.deepEqual(reworkDealInput(deal, '  Исправить сумму  '), { deal_version: 3, reason: 'Исправить сумму' });
  assert.throws(() => reworkDealInput(deal, '  '));
  assert.doesNotThrow(() => reworkDealInput(deal, '😀'.repeat(2000)));
  assert.throws(() => reworkDealInput(deal, '😀'.repeat(2001)));
});

test('every intact pending command survives serialization without replacing its old versions or deadline', () => {
  const update = updateDealInput(deal, fields, now);
  update.deadline_at = '2020-01-01T12:00:00.000Z';
  const commands = [
    { kind: 'update', input: update },
    { kind: 'generate', input: { expected_deal_version: 3, template_id: 'printing-acceptance-v1' } },
    { kind: 'agree', input: agreeDealInput(deal) },
    { kind: 'rework', input: reworkDealInput(deal, 'Уточнить условия') },
    { kind: 'upload', input: { expected_deal_version: 3, file_name: 'акт.pdf', size_bytes: 500, sha256: 'a'.repeat(64) } },
    { kind: 'reserve', input: { deal_version: 3 } },
  ];
  for (const command of commands) {
    const attempt = { ...command, key, dealId: deal.id };
    assert.deepEqual(readPendingAgreement(JSON.stringify(attempt), deal.id), attempt);
  }
});

test('corrupt pending commands block a new attempt instead of silently replacing identity', () => {
  assert.equal(readPendingAgreement(null, deal.id), null);
  const valid = { kind: 'agree', input: agreeDealInput(deal), key, dealId: deal.id };
  for (const serialized of ['', '{broken', JSON.stringify({ ...valid, dealId: 'other-deal' }), JSON.stringify({ ...valid, key: 'short' }), JSON.stringify({ ...valid, extra: true }), JSON.stringify({ ...valid, input: { ...valid.input, deal_version: 0 } }), JSON.stringify({ ...valid, input: { ...valid.input, document_sha256: 'not-a-hash' } }), JSON.stringify({ ...valid, input: { ...valid.input, unexpected: true } })]) {
    assert.throws(() => readPendingAgreement(serialized, deal.id), /Сохранённую команду/);
  }
});

test('pending completion removes only its own key and organisation/deal scope', () => {
  const values = new Map<string, string>();
  const storage = { getItem: (key: string) => values.get(key) ?? null, removeItem: (key: string) => { values.delete(key); } };
  const scoped = agreementStorageKey('org-a', deal.id);
  assert.notEqual(scoped, agreementStorageKey('org-b', deal.id));
  assert.notEqual(scoped, agreementStorageKey('org-a', 'deal-b'));
  values.set(scoped, JSON.stringify({ key: 'newer-command' }));
  assert.equal(removePendingAgreement(storage, 'org-a', deal.id, key), false);
  assert.equal(values.has(scoped), true);
  assert.equal(removePendingAgreement(storage, 'org-a', deal.id, 'newer-command'), true);
  assert.equal(values.has(scoped), false);
});

test('PDF retry requires identical bytes, preserves the original name and rejects another file', async () => {
  const bytes = '%PDF-1.7\noriginal bytes\n%%EOF';
  const original = new File([bytes], 'исходный.pdf', { type: 'application/pdf' });
  const identity = await pdfIdentity(original);
  assert.match(identity.sha256, /^[a-f0-9]{64}$/);
  assert.equal(identity.size_bytes, new TextEncoder().encode(bytes).length);
  const attempt = { kind: 'upload' as const, key, dealId: deal.id, input: { expected_deal_version: 3, ...identity } };
  const restored = readPendingAgreement(JSON.stringify(attempt), deal.id)!;
  assert.equal(restored.kind, 'upload');
  await verifyPendingPdf(attempt, new File([bytes], 'переименованный.pdf', { type: 'application/pdf' }));
  assert.equal(attempt.input.file_name, 'исходный.pdf');
  await assert.rejects(verifyPendingPdf(attempt, new File(['%PDF-1.7\ndifferent bytes\n%%EOF'], 'исходный.pdf', { type: 'application/pdf' })), /другой PDF/);
  for (const file of [new File(['not PDF'], 'fake.pdf', { type: 'application/pdf' }), new File([bytes], 'fake.html', { type: 'text/html' }), new File([], 'empty.pdf'), new File([new Uint8Array(MAX_PDF_BYTES + 1)], 'huge.pdf')]) {
    await assert.rejects(pdfIdentity(file));
  }
});

test('unknown outcome and identity conflicts retain the command; semantic failures allow correction', () => {
  for (const error of [new TypeError('lost response'), new ApiError(503, 'TEMPORARILY_UNAVAILABLE'), new ApiError(409, 'IDEMPOTENCY_CONFLICT'), new ApiError(409, 'SESSION_CHANGED'), new ApiError(403, 'CSRF_INVALID'), new ApiError(401, 'AUTH_REQUIRED'), new ApiError(500, 'INTERNAL_ERROR')]) assert.equal(definitiveAgreementFailure(error), false);
  for (const error of [new ApiError(409, 'VERSION_CONFLICT'), new ApiError(422, 'INVALID_PDF'), new ApiError(422, 'DEADLINE_PASSED')]) assert.equal(definitiveAgreementFailure(error), true);
});

test('reserve requires both distinct parties to agree the same current document, never duplicate old consents', () => {
  const agreed = { ...deal, status: 'AGREED' as const, reserve: null, customer_party_id: 'customer-a', agreements: ['customer-a', 'supplier-a'].map(organisation_id => ({ id: organisation_id, organisation_id, user_id: `u-${organisation_id}`, created_at: '2026-10-22T00:00:00Z', ...agreeDealInput(deal) })) };
  assert.deepEqual(reserveDealInput(agreed), { deal_version: 3 });
  assert.throws(() => reserveDealInput({ ...agreed, status: 'DRAFT' }));
  assert.throws(() => reserveDealInput({ ...agreed, agreements: [agreed.agreements[0], agreed.agreements[0]] }));
  assert.throws(() => reserveDealInput({ ...agreed, agreements: agreed.agreements.map(item => ({ ...item, document_version: 1 })) }));
  assert.throws(() => reserveDealInput({ ...agreed, agreements: agreed.agreements.map(item => ({ ...item, document_sha256: 'b'.repeat(64) })) }));
});

test('server PDF validation and insufficient funds are final refusals with safe messages', () => {
  const invalid = new ApiError(422, 'INVALID_DOCUMENT');
  assert.match(invalid.message, /PDF/);
  assert.equal(definitiveAgreementFailure(invalid), true);
  assert.equal(definitiveAgreementFailure(new ApiError(422, 'INSUFFICIENT_AVAILABLE_FUNDS')), true);
});
