import assert from 'node:assert/strict';
import test from 'node:test';
import { ApiClient, ApiError } from '../src/lib/api.ts';
import type { Deal, DealSignature } from '../src/lib/api.ts';
import { currentSignatures, definitiveAgreementFailure, readPendingAgreement, signDocumentCommand } from '../src/lib/agreement.ts';

const binding = { deal_version: 2, document_id: 'document-a', document_version: 1, document_sha256: 'a'.repeat(64) };
const deal: Deal = {
  id: 'deal-a', deal_version: 2, status: 'FUNDED', title: 'Учебная печать', description: 'Синтетические условия',
  customer_party_id: 'customer-a', supplier_party_id: 'supplier-a', amount_kopecks: '30000', currency: 'RUB', deadline_at: '2027-01-05T12:00:00Z',
  customer: { id: 'customer-a', name: 'Заказчик', kind: 'COMPANY' }, supplier: { id: 'supplier-a', name: 'Исполнитель', kind: 'IP' },
  document: { id: 'document-a', deal_id: 'deal-a', deal_version: 2, document_version: 1, sha256: binding.document_sha256, file_name: 'акт.pdf', size_bytes: 512, content_type: 'application/pdf', created_at: '2026-10-23T00:00:00Z' },
  agreements: ['customer-a', 'supplier-a'].map(organisation_id => ({ id: `agreement-${organisation_id}`, organisation_id, user_id: `user-${organisation_id}`, created_at: '2026-10-23T00:00:00Z', ...binding })),
  signatures: [], review_reasons: [], reserve: { id: 'reserve-a', deal_id: 'deal-a', deal_version: 2, account_id: 'account-a', amount_kopecks: '30000', currency: 'RUB', status: 'ACTIVE', created_at: '2026-10-23T00:00:00Z' },
  payment_id: null, dispute: null, cancellation_request: null, created_at: '2026-10-23T00:00:00Z', updated_at: '2026-10-23T00:00:00Z',
};
function signature(organisation_id: string): DealSignature {
  return { id: `signature-${organisation_id}`, organisation_id, user_id: `user-${organisation_id}`, ...binding, created_at: '2026-10-23T00:00:00Z' };
}

test('signing snapshots the exact PDF and permits only an unsigned party after the full reserve', () => {
  const expected = { kind: 'sign', documentId: 'document-a', input: { deal_version: 2, document_version: 1, document_sha256: binding.document_sha256 } };
  assert.deepEqual(signDocumentCommand(deal, 'customer-a'), expected);
  assert.deepEqual(signDocumentCommand(deal, 'supplier-a'), expected);
  assert.throws(() => signDocumentCommand(deal, 'operator'), /сторона/);
  for (const status of ['DRAFT', 'AGREED', 'READY_TO_SETTLE', 'REVIEW_REQUIRED'] as const) assert.throws(() => signDocumentCommand({ ...deal, status }, 'customer-a'));
  assert.throws(() => signDocumentCommand({ ...deal, signatures: [signature('customer-a')] }, 'customer-a'), /уже подписала/);
  assert.deepEqual(signDocumentCommand({ ...deal, signatures: [signature('customer-a')] }, 'supplier-a'), expected);
});

test('a stale consent, missing reserve or mismatching reserve never enables a signature', () => {
  for (const changed of [
    { reserve: null }, { reserve: { ...deal.reserve!, amount_kopecks: '29999' } }, { reserve: { ...deal.reserve!, deal_id: 'another-deal' } },
    { reserve: { ...deal.reserve!, deal_version: 1 } }, { document: null }, { document: { ...deal.document!, deal_version: 1 } },
    { agreements: [deal.agreements[0], deal.agreements[0]] }, { agreements: deal.agreements.map(item => ({ ...item, document_sha256: 'b'.repeat(64) })) },
  ]) assert.throws(() => signDocumentCommand({ ...deal, ...changed }, 'customer-a'));
});

test('signature display excludes every stale binding and non-party fact', () => {
  const actual = signature('customer-a');
  const obsolete = [
    { ...signature('supplier-a'), deal_version: 1 }, { ...signature('supplier-a'), document_version: 2 },
    { ...signature('supplier-a'), document_id: 'other-document' }, { ...signature('supplier-a'), document_sha256: 'b'.repeat(64) }, signature('operator'),
  ];
  assert.deepEqual(currentSignatures({ ...deal, signatures: [...obsolete, actual] }), [actual]);
  assert.deepEqual(currentSignatures({ ...deal, document: null, signatures: [actual] }), []);
});

test('saved signatures survive reload without changing their original target, versions, hash or key', () => {
  const attempt = { ...signDocumentCommand(deal, 'customer-a'), key: 'fixed-signature-key-2026', dealId: deal.id };
  assert.deepEqual(readPendingAgreement(JSON.stringify(attempt), deal.id), attempt);
  const malformed = [
    { ...attempt, documentId: '' }, { ...attempt, documentId: undefined }, { ...attempt, documentId: 'a'.repeat(129) },
    { ...attempt, input: { ...attempt.input, document_version: 0 } }, { ...attempt, input: { ...attempt.input, document_sha256: 'A'.repeat(64) } },
    { ...attempt, input: { ...attempt.input, organisation_id: 'supplier-a' } }, { ...attempt, input: { ...attempt.input, deal_version: '2' } },
  ];
  for (const saved of malformed) assert.throws(() => readPendingAgreement(JSON.stringify(saved), deal.id), /Сохранённую команду/);
  assert.equal(definitiveAgreementFailure(new ApiError(422, 'RESERVE_REQUIRED')), true);
  assert.equal(definitiveAgreementFailure(new ApiError(503, 'TEMPORARILY_UNAVAILABLE')), false);
  assert.equal(definitiveAgreementFailure(new ApiError(409, 'IDEMPOTENCY_CONFLICT')), false);
});

test('the signature request sends only its exact body, original key and bound actor to the encoded document', async () => {
  const calls: { url: string; options: RequestInit }[] = [];
  const client = new ApiClient(async (url, options) => {
    calls.push({ url: String(url), options: options! });
    return Response.json(String(url).endsWith('/csrf') ? { csrf_token: 'signature-csrf' } : {});
  });
  client.bindIdentity('displayed-signer');
  const { input } = signDocumentCommand(deal, 'customer-a');
  await client.signDocument('document/a', input, 'unchanged-signature-key');
  assert.equal(calls.length, 2);
  assert.equal(calls[1].url, '/api/v1/documents/document%2Fa/sign');
  assert.deepEqual(JSON.parse(calls[1].options.body as string), input);
  const headers = new Headers(calls[1].options.headers);
  assert.equal(headers.get('Idempotency-Key'), 'unchanged-signature-key');
  assert.equal(headers.get('X-Expected-User-ID'), 'displayed-signer');
  assert.equal(headers.get('X-CSRF-Token'), 'signature-csrf');
});
