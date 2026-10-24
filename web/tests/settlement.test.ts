import assert from 'node:assert/strict';
import test from 'node:test';
import { ApiClient, ApiError } from '../src/lib/api.ts';
import type { Deal, EDODelivery, EDOEventTemplate } from '../src/lib/api.ts';
import { readPendingAgreement, definitiveAgreementFailure } from '../src/lib/agreement.ts';
import { blockingCommand, blockingReason, canDecideCancellation, canDispute, canRequestCancellation, statusLabels, terminalDeal } from '../src/lib/blockers.ts';
import { definitiveEDOFailure, edoInput, edoOutcome, edoStorageKey, edoTemplates, readPendingEDO, removePendingEDO } from '../src/lib/edo.ts';

const deal = { id: 'deal-a', deal_version: 3, status: 'FUNDED', customer_party_id: 'customer', supplier_party_id: 'supplier', dispute: null, cancellation_request: null, review_reasons: [] } as unknown as Deal;
const request = { id: 'request-a', deal_id: deal.id, deal_version: 3, initiator_organisation_id: 'customer', initiator_user_id: 'user-customer', reason: 'Причина', status: 'PENDING' as const, created_at: '2026-10-23T00:00:00Z', decision: null };
const key = 'fixed-command-key-2026';

test('a cancellation decision belongs to the other party and remains possible with every independent blocker', () => {
  for (const status of ['CANCELLATION_PENDING', 'DISPUTED', 'REVIEW_REQUIRED'] as const) {
    const pending = { ...deal, status, cancellation_request: request, review_reasons: ['CANCELLATION_REJECTED', 'DEADLINE_PASSED'] };
    assert.equal(canDecideCancellation(pending, 'customer'), false);
    assert.equal(canDecideCancellation(pending, 'operator'), false);
    assert.equal(canDecideCancellation(pending, 'supplier'), true);
    assert.deepEqual(blockingCommand(pending, 'supplier', 'cancel-accept'), { kind: 'cancel-accept', requestId: 'request-a', input: { deal_version: 3 } });
    assert.deepEqual(blockingCommand(pending, 'supplier', 'cancel-reject', '  Не согласен  '), { kind: 'cancel-reject', requestId: 'request-a', input: { deal_version: 3, reason: 'Не согласен' } });
    assert.throws(() => blockingCommand(pending, 'customer', 'cancel-accept'));
    assert.equal(canRequestCancellation(pending, 'supplier'), false);
  }
  assert.equal(canDecideCancellation({ ...deal, cancellation_request: { ...request, deal_version: 2 } }, 'supplier'), false);
  assert.equal(canDecideCancellation({ ...deal, cancellation_request: { ...request, status: 'ACCEPTED' } }, 'supplier'), false);
});

test('terminal outcomes close every blocking action while an expired deal can request agreed cancellation', () => {
  for (const status of ['SETTLED', 'CANCELLED', 'REFUNDED'] as const) {
    const ended = { ...deal, status, cancellation_request: request };
    assert.equal(terminalDeal(ended), true);
    assert.equal(canDispute(ended, 'customer'), false);
    assert.equal(canRequestCancellation(ended, 'supplier'), false);
    assert.equal(canDecideCancellation(ended, 'supplier'), false);
    for (const kind of ['dispute', 'cancel-request', 'cancel-accept', 'cancel-reject'] as const) assert.throws(() => blockingCommand(ended, 'supplier', kind, 'Причина'));
  }
  assert.equal(canDispute({ ...deal, status: 'DRAFT' }, 'customer'), false);
  assert.equal(canRequestCancellation({ ...deal, status: 'REVIEW_REQUIRED', review_reasons: ['DEADLINE_PASSED'] }, 'supplier'), true);
  assert.equal(Object.keys(statusLabels).length, 10);
  assert.notEqual(statusLabels.REFUNDED, statusLabels.SETTLED);
});

test('reasons count Unicode characters, trim outer whitespace and refuse invisible controls', () => {
  assert.equal(blockingReason('  Работа\n\tне принята  '), 'Работа\n\tне принята');
  assert.doesNotThrow(() => blockingReason('😀'.repeat(2000)));
  for (const reason of ['', '  ', '😀'.repeat(2001), 'Причина\u0000', 'Причина\u0085']) assert.throws(() => blockingReason(reason));
});

test('all blocking commands preserve original targets in pending data and refuse a corrupt cancellation ID', () => {
  const pending = { ...deal, cancellation_request: request };
  const commands = [blockingCommand(deal, 'customer', 'dispute', 'Проверить работу'), blockingCommand(deal, 'supplier', 'cancel-request', 'Завершить'), blockingCommand(pending, 'supplier', 'cancel-accept'), blockingCommand(pending, 'supplier', 'cancel-reject', 'Возражение')];
  for (const command of commands) {
    const attempt = { ...command, dealId: deal.id, key };
    assert.deepEqual(readPendingAgreement(JSON.stringify(attempt), deal.id), attempt);
    if ('requestId' in attempt) assert.throws(() => readPendingAgreement(JSON.stringify({ ...attempt, requestId: '' }), deal.id));
  }
  assert.equal(definitiveAgreementFailure(new ApiError(409, 'CANCELLATION_SELF_APPROVAL')), true);
  assert.equal(definitiveAgreementFailure(new ApiError(503, 'TEMPORARILY_UNAVAILABLE')), false);
});

test('the eleven EDO templates bind an original event only for replay and collision tests', () => {
  assert.equal(Object.keys(edoTemplates).length, 11);
  assert.deepEqual(edoInput(' deal-a ', 'valid', 'discard-unused'), { deal_id: 'deal-a', template: 'valid' });
  for (const template of ['replay', 'same_id_different_body'] as const) {
    assert.deepEqual(edoInput('deal-a', template, ' event-a '), { deal_id: 'deal-a', template, replay_event_id: 'event-a' });
    assert.throws(() => edoInput('deal-a', template, ''));
  }
  for (const template of ['constructor', '__proto__', 'unknown']) assert.throws(() => edoInput('deal-a', template as EDOEventTemplate, ''));
  assert.throws(() => edoInput('deal\u0000', 'valid', ''));
});

test('operator EDO pending is scoped by the original user, exact body and key, including late completions', () => {
  const attempt = { userId: 'operator-a', key, input: edoInput('deal-a', 'replay', 'event-a') };
  assert.deepEqual(readPendingEDO(JSON.stringify(attempt), 'operator-a'), attempt);
  for (const value of [JSON.stringify(attempt), JSON.stringify({ ...attempt, input: { ...attempt.input, unexpected: true } })]) assert.throws(() => readPendingEDO(value, 'operator-b'));
  assert.throws(() => readPendingEDO(JSON.stringify({ ...attempt, input: { deal_id: 'deal-a', template: 'valid', replay_event_id: 'event-a' } }), 'operator-a'));
  assert.throws(() => readPendingEDO(JSON.stringify({ ...attempt, key: 'short' }), 'operator-a'));
  const values = new Map([[edoStorageKey('operator-a'), JSON.stringify({ ...attempt, key: 'newer-command-key-2026' })]]);
  const storage = { getItem: (key: string) => values.get(key) ?? null, removeItem: (key: string) => { values.delete(key); } };
  removePendingEDO(storage, attempt);
  assert.equal(values.size, 1);
  removePendingEDO(storage, { ...attempt, key: 'newer-command-key-2026' });
  assert.equal(values.size, 0);
  for (const code of ['SESSION_CHANGED', 'CSRF_INVALID', 'IDEMPOTENCY_CONFLICT', 'TEMPORARILY_UNAVAILABLE']) assert.equal(definitiveEDOFailure(new ApiError(503, code)), false);
  assert.equal(definitiveEDOFailure(new ApiError(400, 'INVALID_REQUEST')), true);
  assert.equal(definitiveEDOFailure(new ApiError(422, 'DOCUMENT_REQUIRED')), true);
});

test('DELIVERED and 202 never imply a payout; only a bound successful processor result confirms it', () => {
  const base = { event_id: 'event-a', deal_id: 'deal-a', status: 'DELIVERED', http_status: 200, result: { event_id: 'event-a', deal_id: 'deal-a', result: 'settled', payment_id: 'payment-a' } } as EDODelivery;
  assert.equal(edoOutcome(base), 'settled');
  assert.equal(edoOutcome({ ...base, result: { ...base.result as { event_id: string; deal_id: string; result: 'settled'; payment_id: string }, result: 'already_settled' } }), 'already_settled');
  assert.equal(edoOutcome({ ...base, status: 'PENDING', http_status: null, result: null }), 'pending');
  assert.equal(edoOutcome({ ...base, http_status: 422, result: null }), 'rejected');
  assert.equal(edoOutcome({ ...base, http_status: 202 }), 'unknown');
  assert.equal(edoOutcome({ ...base, result: null }), 'unknown');
  assert.equal(edoOutcome({ ...base, event_id: 'another-event' }), 'unknown');
});

test('blocking and EDO transport keep actor, CSRF, exact body, key and encoded request identifiers', async () => {
  const calls: { url: string; options: RequestInit }[] = [];
  const client = new ApiClient(async (url, options) => { calls.push({ url: String(url), options: options! }); return Response.json(String(url).endsWith('/csrf') ? { csrf_token: 'csrf' } : {}); });
  client.bindIdentity('visible-user');
  await client.openDispute('deal/a', { deal_version: 3, reason: 'Спор' }, key);
  await client.requestCancellation('deal/a', { deal_version: 3, reason: 'Отмена' }, key);
  await client.decideCancellation('deal/a', 'request/a', 'accept', { deal_version: 3 }, key);
  await client.queueEDO({ deal_id: 'deal-a', template: 'valid' }, key);
  await client.edoEvent('event/a');
  assert.deepEqual(calls.slice(1).map(call => call.url), ['/api/v1/deals/deal%2Fa/disputes', '/api/v1/deals/deal%2Fa/cancellation-requests', '/api/v1/deals/deal%2Fa/cancellation-requests/request%2Fa/accept', '/api/v1/demo/edo/events', '/api/v1/demo/edo/events/event%2Fa']);
  for (const call of calls.slice(1)) assert.equal(new Headers(call.options.headers).get('X-Expected-User-ID'), 'visible-user');
  for (const call of calls.slice(1, 5)) { assert.equal(new Headers(call.options.headers).get('Idempotency-Key'), key); assert.equal(new Headers(call.options.headers).get('X-CSRF-Token'), 'csrf'); }
});
