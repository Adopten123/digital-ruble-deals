import type { Deal, DealStatus } from './api.ts';
import type { AgreementCommand } from './agreement.ts';

export const statusLabels: Record<DealStatus, string> = {
  DRAFT: 'Черновик', AGREED: 'Согласовано', FUNDED: 'Средства зарезервированы', READY_TO_SETTLE: 'Ожидает расчёта',
  SETTLED: 'Расчёт завершён', DISPUTED: 'Открыт спор', CANCELLATION_PENDING: 'Ожидает решения об отмене',
  REVIEW_REQUIRED: 'Требует проверки', CANCELLED: 'Сделка отменена', REFUNDED: 'Резерв освобождён',
};
export function terminalDeal(deal: Pick<Deal, 'status'>): boolean { return ['SETTLED', 'CANCELLED', 'REFUNDED'].includes(deal.status); }
export function partyOf(deal: Deal, ownId: string): boolean { return ownId === deal.customer_party_id || ownId === deal.supplier_party_id; }
export function blockingReason(reason: string): string {
  reason = reason.trim();
  if (!reason || [...reason].length > 2000 || [...reason].some(char => /\p{Cc}/u.test(char) && char !== '\n' && char !== '\t')) throw new Error('Укажите причину: от 1 до 2 000 символов без управляющих знаков.');
  return reason;
}
export function canDispute(deal: Deal, ownId: string): boolean {
  return partyOf(deal, ownId) && !deal.dispute && ['AGREED', 'FUNDED', 'READY_TO_SETTLE', 'REVIEW_REQUIRED', 'CANCELLATION_PENDING'].includes(deal.status);
}
export function canRequestCancellation(deal: Deal, ownId: string): boolean { return partyOf(deal, ownId) && !terminalDeal(deal) && !deal.cancellation_request; }
export function canDecideCancellation(deal: Deal, ownId: string): boolean {
  const request = deal.cancellation_request;
  return partyOf(deal, ownId) && !terminalDeal(deal) && !!request && request.status === 'PENDING'
    && request.deal_id === deal.id && request.deal_version === deal.deal_version && request.initiator_organisation_id !== ownId;
}
export function blockingCommand(deal: Deal, ownId: string, kind: 'dispute' | 'cancel-request' | 'cancel-accept' | 'cancel-reject', reason = ''): AgreementCommand {
  if (kind === 'dispute' || kind === 'cancel-request') {
    if (!(kind === 'dispute' ? canDispute(deal, ownId) : canRequestCancellation(deal, ownId))) throw new Error('Действие недоступно в текущем состоянии сделки. Обновите карточку.');
    return { kind, input: { deal_version: deal.deal_version, reason: blockingReason(reason) } };
  }
  if (!canDecideCancellation(deal, ownId)) throw new Error('Решение по активному запросу должна принять другая организация.');
  const requestId = deal.cancellation_request!.id;
  return kind === 'cancel-accept' ? { kind, requestId, input: { deal_version: deal.deal_version } }
    : { kind, requestId, input: { deal_version: deal.deal_version, reason: blockingReason(reason) } };
}
