import { ApiError } from './api.ts';
import type { DemoAccount, DemoMoneyInput } from './api.ts';
import { parseKopecks, rublesToKopecks, validateBalances } from './money.ts';

export type DemoMoneyOperation = 'credits' | 'debits';
export interface PendingDemoMoney {
  key: string; userId: string; accountId: string; accountName: string;
  operation: DemoMoneyOperation; input: DemoMoneyInput;
}
const storagePrefix = (userId: string) => `poaktu:pending-demo-money:${userId}:`;
export const demoMoneyStorageKey = (userId: string, accountId: string, operation: DemoMoneyOperation) => `${storagePrefix(userId)}${accountId}:${operation}`;

export function demoMoneyInput(amount: string, reason: string): DemoMoneyInput {
  reason = reason.trim();
  if (!reason || [...reason].length > 2000) throw new Error('Укажите основание операции: от 1 до 2 000 символов.');
  return { amount_kopecks: rublesToKopecks(amount), reason };
}

export function validateDemoAccount(account: DemoAccount): void {
  if (account.currency !== 'RUB' || !account.id || !account.organisation?.id) throw new Error('Некорректные данные учебного счёта. Обновите список.');
  validateBalances(account.total_kopecks, account.reserved_kopecks, account.available_kopecks);
}

/** Informational only: the server rechecks available funds under the account lock. */
export function reserveBalanceSnapshot(account: { total_kopecks: string; reserved_kopecks: string; available_kopecks: string }, amount: string) {
  validateBalances(account.total_kopecks, account.reserved_kopecks, account.available_kopecks);
  const required = parseKopecks(amount);
  if (required <= 0n) throw new Error('Сумма резерва должна быть положительной.');
  const available = parseKopecks(account.available_kopecks);
  return { canCover: available >= required, availableAfterKopecks: available >= required ? (available - required).toString() : null };
}

export function readPendingDemoMoney(serialized: string | null, userId: string): PendingDemoMoney | null {
  if (serialized === null) return null;
  try {
    const item: PendingDemoMoney = JSON.parse(serialized);
    if (!item || typeof item !== 'object' || Object.keys(item).some(key => !['key', 'userId', 'accountId', 'accountName', 'operation', 'input'].includes(key))
      || item.userId !== userId || typeof item.accountId !== 'string' || !item.accountId || item.accountId.length > 128
      || typeof item.accountName !== 'string' || !item.accountName || [...item.accountName].length > 200
      || typeof item.key !== 'string' || !/^[\x21-\x7e]{16,128}$/.test(item.key)
      || !['credits', 'debits'].includes(item.operation) || !item.input || typeof item.input !== 'object'
      || Object.keys(item.input).length !== 2 || Object.keys(item.input).some(key => !['amount_kopecks', 'reason'].includes(key))
      || typeof item.input.reason !== 'string' || !item.input.reason.trim() || [...item.input.reason].length > 2000
      || typeof item.input.amount_kopecks !== 'string' || item.input.amount_kopecks.length > 19 || parseKopecks(item.input.amount_kopecks) <= 0n) throw new Error();
    return item;
  } catch { throw new Error('Не удалось прочитать сохранённую учебную денежную команду. Новые операции закрыты до проверки её результата.'); }
}

export function readPendingDemoCommands(storage: Pick<Storage, 'length' | 'key' | 'getItem'>, userId: string): PendingDemoMoney[] {
  const items: PendingDemoMoney[] = [];
  for (let index = 0; index < storage.length; index++) {
    const key = storage.key(index);
    if (!key?.startsWith(storagePrefix(userId))) continue;
    const item = readPendingDemoMoney(storage.getItem(key), userId);
    if (!item || demoMoneyStorageKey(item.userId, item.accountId, item.operation) !== key) throw new Error('Область сохранённой учебной команды не совпадает. Новые операции закрыты до проверки.');
    items.push(item);
  }
  return items;
}

export function removePendingDemoMoney(storage: Pick<Storage, 'getItem' | 'removeItem'>, item: PendingDemoMoney): boolean {
  const storageKey = demoMoneyStorageKey(item.userId, item.accountId, item.operation);
  const serialized = storage.getItem(storageKey);
  if (serialized === null) return false;
  let saved: unknown;
  try { saved = JSON.parse(serialized); } catch { return false; }
  if (!saved || typeof saved !== 'object' || !('key' in saved) || saved.key !== item.key) return false;
  storage.removeItem(storageKey);
  return true;
}

export function definitiveDemoMoneyFailure(error: unknown): boolean {
  return error instanceof ApiError && ['INVALID_REQUEST', 'INVALID_AMOUNT', 'AMOUNT_OVERFLOW', 'INSUFFICIENT_AVAILABLE_FUNDS', 'RESOURCE_NOT_FOUND', 'FORBIDDEN', 'IDEMPOTENCY_KEY_REQUIRED'].includes(error.code);
}
