import { ApiError } from './api.ts';
import type { EDOCommandInput, EDODelivery, EDOEventTemplate } from './api.ts';

export const edoTemplates: Record<EDOEventTemplate, string> = {
  valid: 'Корректное событие', single_signature: 'Только одна подпись', wrong_amount: 'Неверная сумма', wrong_parties: 'Неверные стороны',
  wrong_document: 'Другой документ', wrong_document_version: 'Другая версия PDF', wrong_document_hash: 'Неверный хеш PDF',
  wrong_deal_version: 'Другая версия условий', invalid_hmac: 'Неподтверждённый источник', replay: 'Повтор прежнего события', same_id_different_body: 'Прежний ID с другим содержимым',
};
export interface PendingEDO { userId: string; key: string; input: EDOCommandInput }
export function edoStorageKey(userId: string): string { return `poaktu:pending-edo:${userId}`; }
export function edoWatchKey(userId: string): string { return `poaktu:edo-watch:${userId}`; }
export function edoId(value: string): string {
  value = value.trim();
  if (!value || [...value].length > 128 || /\p{Cc}/u.test(value)) throw new Error('Укажите ID длиной от 1 до 128 символов без управляющих знаков.');
  return value;
}
export function edoInput(dealId: string, template: EDOEventTemplate, replayId: string): EDOCommandInput {
  if (!Object.prototype.hasOwnProperty.call(edoTemplates, template)) throw new Error('Выберите предусмотренный учебный сценарий.');
  return { deal_id: edoId(dealId), template, ...((template === 'replay' || template === 'same_id_different_body') ? { replay_event_id: edoId(replayId) } : {}) };
}
export function readPendingEDO(value: string | null, userId: string): PendingEDO | null {
  if (value === null) return null;
  try {
    const item: PendingEDO = JSON.parse(value);
    if (!item || Object.keys(item).length !== 3 || Object.keys(item).some(key => !['userId', 'key', 'input'].includes(key))
      || item.userId !== userId || typeof item.key !== 'string' || !/^[\x21-\x7e]{16,128}$/.test(item.key)
      || !item.input || typeof item.input.deal_id !== 'string' || typeof item.input.template !== 'string'
      || (item.input.replay_event_id !== undefined && typeof item.input.replay_event_id !== 'string')) throw new Error();
    const expected = edoInput(item.input.deal_id, item.input.template, item.input.replay_event_id ?? '');
    if (Object.keys(item.input).length !== Object.keys(expected).length || Object.keys(item.input).some(key => !Object.prototype.hasOwnProperty.call(expected, key))
      || expected.deal_id !== item.input.deal_id || expected.replay_event_id !== item.input.replay_event_id) throw new Error();
    return item;
  } catch { throw new Error('Сохранённый запрос ЭДО нельзя прочитать. Новая отправка закрыта до проверки исходного запроса.'); }
}
export function removePendingEDO(storage: Pick<Storage, 'getItem' | 'removeItem'>, item: PendingEDO): void {
  const key = edoStorageKey(item.userId);
  const saved = storage.getItem(key);
  if (saved !== null && readPendingEDO(saved, item.userId)?.key === item.key) storage.removeItem(key);
}
export function definitiveEDOFailure(error: unknown): boolean {
  return error instanceof ApiError && ['INVALID_REQUEST', 'RESOURCE_NOT_FOUND', 'FORBIDDEN', 'IDEMPOTENCY_KEY_REQUIRED', 'INVALID_STATE', 'SIGNATURES_MISSING', 'DOCUMENT_REQUIRED'].includes(error.code);
}
export function edoOutcome(delivery: EDODelivery): 'pending' | 'settled' | 'already_settled' | 'rejected' | 'unknown' {
  if (delivery.status === 'PENDING') return 'pending';
  if (delivery.status !== 'DELIVERED') return 'unknown';
  if (delivery.http_status != null && delivery.http_status >= 400 && delivery.http_status <= 499) return 'rejected';
  if (delivery.http_status === 200 && delivery.result && 'result' in delivery.result && delivery.result.payment_id
    && delivery.result.event_id === delivery.event_id && delivery.result.deal_id === delivery.deal_id) {
    if (delivery.result.result === 'settled' || delivery.result.result === 'already_settled') return delivery.result.result;
  }
  return 'unknown';
}
