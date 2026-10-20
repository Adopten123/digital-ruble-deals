import type { Counterparty, CreateDealInput } from './api.ts';
import { rublesToKopecks } from './money.ts';

export interface PendingDealAttempt {
  key: string;
  input: CreateDealInput;
  supplier: Counterparty;
}

export function createDealInput(fields: {
  supplierId: string; title: string; description: string; amount: string; deadline: string;
}, now = Date.now()): CreateDealInput {
  const title = fields.title.trim();
  const description = fields.description.trim();
  if (!fields.supplierId) throw new Error('Выберите исполнителя из каталога.');
  if (!title || [...title].length > 200) throw new Error('Название должно содержать от 1 до 200 символов.');
  if (!description || [...description].length > 5000) throw new Error('Опишите условия: от 1 до 5 000 символов.');
  const deadline = new Date(fields.deadline);
  if (!fields.deadline || !Number.isFinite(deadline.getTime()) || deadline.getUTCFullYear() < 1 || deadline.getUTCFullYear() > 9999 || deadline.getTime() <= now) {
    throw new Error('Укажите срок сделки в будущем.');
  }
  const input: CreateDealInput = {
    supplier_party_id: fields.supplierId, title, description,
    amount_kopecks: rublesToKopecks(fields.amount), currency: 'RUB', deadline_at: deadline.toISOString(),
  };
  if (new TextEncoder().encode(JSON.stringify(input)).length > 16384) {
    throw new Error('Условия занимают слишком много места. Сократите описание, сохранив существенные условия.');
  }
  return input;
}

export function pendingStorageKey(organisationId: string): string { return `poaktu:pending-deal:${organisationId}`; }

/** A late response must never clear a newer command saved by another mounted form. */
export function removePendingDeal(storage: Pick<Storage, 'getItem' | 'removeItem'>, organisationId: string, commandKey: string): boolean {
  const storageKey = pendingStorageKey(organisationId);
  const current = storage.getItem(storageKey);
  if (!current) return false;
  let parsed: unknown;
  try { parsed = JSON.parse(current); } catch { return false; }
  if (!parsed || typeof parsed !== 'object' || !('key' in parsed) || parsed.key !== commandKey) return false;
  storage.removeItem(storageKey);
  return true;
}

export function readPendingDeal(serialized: string | null): PendingDealAttempt | null {
  if (!serialized) return null;
  try {
    const attempt: PendingDealAttempt = JSON.parse(serialized);
    if (!attempt || typeof attempt.key !== 'string' || !/^[a-zA-Z0-9-]{16,128}$/.test(attempt.key)
      || !attempt.input || typeof attempt.input.title !== 'string' || typeof attempt.input.description !== 'string'
      || typeof attempt.input.deadline_at !== 'string' || typeof attempt.input.amount_kopecks !== 'string'
      || !/^[1-9][0-9]*$/.test(attempt.input.amount_kopecks) || attempt.input.currency !== 'RUB'
      || !attempt.supplier || typeof attempt.supplier.id !== 'string' || typeof attempt.supplier.name !== 'string'
      || !['COMPANY', 'IP'].includes(attempt.supplier.kind) || attempt.supplier.id !== attempt.input.supplier_party_id) {
      throw new Error('invalid attempt');
    }
    return attempt;
  } catch {
    throw new Error('Не удалось прочитать сохранённый запрос создания. Проверьте список сделок перед новой попыткой.');
  }
}

export function plainRubles(kopecks: string): string {
  const amount = BigInt(kopecks);
  return `${amount / 100n},${(amount % 100n).toString().padStart(2, '0')}`;
}

export function localDateTime(utc: string): string {
  const date = new Date(utc);
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** Date filters cover the selected calendar days in the browser's timezone. */
export function dateBoundary(date: string, end: boolean): string {
  if (!date) return '';
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date) || date.startsWith('0000')) throw new Error('Проверьте даты периода. Допустимы годы с 1 по 9999.');
  const boundary = new Date(`${date}T${end ? '23:59:59.999' : '00:00:00.000'}`).toISOString();
  return end ? boundary.replace(/\.999Z$/, '.999999Z') : boundary;
}
