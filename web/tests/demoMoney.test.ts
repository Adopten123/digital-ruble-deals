import assert from 'node:assert/strict';
import test from 'node:test';
import { ApiClient, ApiError } from '../src/lib/api.ts';
import { definitiveDemoMoneyFailure, demoMoneyInput, demoMoneyStorageKey, readPendingDemoCommands, readPendingDemoMoney, removePendingDemoMoney, reserveBalanceSnapshot, validateDemoAccount } from '../src/lib/demoMoney.ts';

const attempt = { userId: 'operator-a', accountId: 'account-a', accountName: 'Учебный счёт А', operation: 'debits' as const, key: 'a-fixed-money-key-123', input: { amount_kopecks: '123456', reason: 'Служебная учебная проверка' } };
function memoryStorage() {
  const values = new Map<string, string>();
  return { values, get length() { return values.size; }, key: (index: number) => [...values.keys()][index] ?? null, getItem: (key: string) => values.get(key) ?? null, removeItem: (key: string) => { values.delete(key); } };
}

test('the balance preview stays exact beyond JS safe integers and never treats reserved funds as available', () => {
  const snapshot = reserveBalanceSnapshot({ total_kopecks: '9223372036854775807', reserved_kopecks: '9223372036854774807', available_kopecks: '1000' }, '999');
  assert.deepEqual(snapshot, { canCover: true, availableAfterKopecks: '1' });
  assert.deepEqual(reserveBalanceSnapshot({ total_kopecks: '50000000', reserved_kopecks: '30000000', available_kopecks: '20000000' }, '20000001'), { canCover: false, availableAfterKopecks: null });
  assert.throws(() => reserveBalanceSnapshot({ total_kopecks: '10', reserved_kopecks: '11', available_kopecks: '0' }, '1'));
  assert.throws(() => validateDemoAccount({ id: 'a', organisation: { id: 'o', name: 'Test', kind: 'COMPANY' }, currency: 'RUB', total_kopecks: '100', reserved_kopecks: '10', available_kopecks: '100' }));
});

test('operator inputs require positive exact money and an explicit bounded reason', () => {
  assert.deepEqual(demoMoneyInput('90071992547409,93', '  Учебное основание  '), { amount_kopecks: '9007199254740993', reason: 'Учебное основание' });
  for (const [amount, reason] of [['0', 'Test'], ['1,001', 'Test'], ['92233720368547758,08', 'Test'], ['1', '  '], ['1', '😀'.repeat(2001)]]) assert.throws(() => demoMoneyInput(amount, reason));
  assert.doesNotThrow(() => demoMoneyInput('0,01', '😀'.repeat(2000)));
});

test('money commands are isolated by user, account and operation; a corrupt scoped command blocks discovery', () => {
  const storage = memoryStorage();
  const key = demoMoneyStorageKey(attempt.userId, attempt.accountId, attempt.operation);
  storage.values.set(key, JSON.stringify(attempt));
  storage.values.set(demoMoneyStorageKey('other-user', 'a', 'credits'), '{corrupt but another user');
  assert.deepEqual(readPendingDemoCommands(storage, attempt.userId), [attempt]);
  assert.notEqual(key, demoMoneyStorageKey(attempt.userId, attempt.accountId, 'credits'));
  assert.notEqual(key, demoMoneyStorageKey(attempt.userId, 'another-account', attempt.operation));
  assert.notEqual(key, demoMoneyStorageKey('another-user', attempt.accountId, attempt.operation));
  storage.values.set(demoMoneyStorageKey(attempt.userId, 'wrong-resource', attempt.operation), JSON.stringify(attempt));
  assert.throws(() => readPendingDemoCommands(storage, attempt.userId), /Область/);
});

test('malformed money pending data cannot silently become a new money command', () => {
  assert.equal(readPendingDemoMoney(null, attempt.userId), null);
  for (const serialized of ['', '{broken', JSON.stringify({ ...attempt, userId: 'another-user' }), JSON.stringify({ ...attempt, operation: 'payment' }), JSON.stringify({ ...attempt, input: { ...attempt.input, amount_kopecks: '9223372036854775808' } }), JSON.stringify({ ...attempt, input: { ...attempt.input, amount_kopecks: '1.00' } }), JSON.stringify({ ...attempt, input: { ...attempt.input, reason: ' ' } }), JSON.stringify({ ...attempt, input: { ...attempt.input, injected: true } })]) assert.throws(() => readPendingDemoMoney(serialized, attempt.userId), /сохранённую/);
});

test('a late monetary reply removes only its exact original pending key', () => {
  const storage = memoryStorage();
  const key = demoMoneyStorageKey(attempt.userId, attempt.accountId, attempt.operation);
  storage.values.set(key, JSON.stringify({ ...attempt, key: 'a-newer-money-command' }));
  assert.equal(removePendingDemoMoney(storage, attempt), false);
  assert.equal(storage.values.has(key), true);
  storage.values.set(key, JSON.stringify(attempt));
  assert.equal(removePendingDemoMoney(storage, attempt), true);
  assert.equal(storage.values.has(key), false);
});

test('a lost operator reply can only be checked with the same stored key and exact body', async () => {
  const serialized = JSON.stringify(attempt);
  const calls: { body: BodyInit | null | undefined; key: string | null }[] = [];
  const client = new ApiClient(async (url, options) => {
    if (String(url).endsWith('/csrf')) return Response.json({ csrf_token: 'money-csrf' });
    calls.push({ body: options?.body, key: new Headers(options?.headers).get('Idempotency-Key') });
    if (calls.length === 1) throw new TypeError('lost response after commit');
    return Response.json({ transaction_id: 'same-transaction', account: {} }, { status: 201 });
  });
  client.bindIdentity(attempt.userId);
  await assert.rejects(client.demoMoney(attempt.accountId, attempt.operation, attempt.input, attempt.key), TypeError);
  assert.equal(calls.length, 1);
  const restored = readPendingDemoMoney(serialized, attempt.userId)!;
  await client.demoMoney(restored.accountId, restored.operation, restored.input, restored.key);
  assert.deepEqual(calls[1], calls[0]);
});

test('only a definitive money refusal frees the command, never a timeout or session conflict', () => {
  for (const error of [new TypeError('lost'), new ApiError(503, 'TEMPORARILY_UNAVAILABLE'), new ApiError(409, 'SESSION_CHANGED'), new ApiError(409, 'IDEMPOTENCY_CONFLICT'), new ApiError(403, 'CSRF_INVALID')]) assert.equal(definitiveDemoMoneyFailure(error), false);
  for (const error of [new ApiError(422, 'INSUFFICIENT_AVAILABLE_FUNDS'), new ApiError(422, 'AMOUNT_OVERFLOW'), new ApiError(400, 'INVALID_AMOUNT')]) assert.equal(definitiveDemoMoneyFailure(error), true);
});
