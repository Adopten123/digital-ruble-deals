import type { AuditEvent, DemoAuditFilters } from './api.ts';
import { dateBoundary } from './deals.ts';

export const auditTypeLabels: Record<string, string> = {
  DEAL_CREATED: 'Сделка создана', DEAL_CREATE_REJECTED: 'Создание сделки отклонено',
  PATCH_DEAL: 'Условия изменены', PATCH_DEAL_REJECTED: 'Изменение условий отклонено',
  UPLOAD_DOCUMENT: 'PDF загружен', GENERATE_DOCUMENT: 'PDF сформирован',
  GENERATE_DOCUMENT_REJECTED: 'Формирование PDF отклонено', UPLOAD_DOCUMENT_REJECTED: 'Загрузка PDF отклонена',
  AGREE_DEAL: 'Условия согласованы стороной', AGREE_DEAL_REJECTED: 'Согласование отклонено',
  DEAL_REWORKED: 'Сделка возвращена на доработку', REWORK_DEAL_REJECTED: 'Доработка отклонена',
  DEAL_RESERVED: 'Создан резерв сделки', RESERVE_DEAL_REJECTED: 'Резервирование отклонено',
  DOCUMENT_SIGNED: 'Тестовая подпись добавлена', SIGN_DOCUMENT_REJECTED: 'Тестовая подпись отклонена',
  DISPUTE_OPENED: 'Спор открыт', OPEN_DISPUTE_REJECTED: 'Открытие спора отклонено',
  CANCELLATION_REQUESTED: 'Отмена запрошена', REQUEST_CANCELLATION_REJECTED: 'Запрос отмены отклонён',
  CANCELLATION_ACCEPTED: 'Отмена согласована', ACCEPT_CANCELLATION_REJECTED: 'Согласование отмены отклонено',
  CANCELLATION_REJECTED: 'Сторона отказала в отмене', REJECT_CANCELLATION_REJECTED: 'Отказ в отмене не принят',
  EDO_DELIVERY_REQUESTED: 'Доставка ЭДО запрошена', EDO_EVENT_REJECTED: 'Событие ЭДО отклонено',
  EDO_EVENT_READY: 'Событие ЭДО подготовлено', EDO_EVENT_PROCESSED: 'Событие ЭДО обработано',
  DEAL_SETTLED: 'Расчёт выполнен', DEMO_CREDITED: 'Учебное зачисление', DEMO_DEBITED: 'Учебное списание',
  DEMO_CREDIT_REJECTED: 'Учебное зачисление отклонено', DEMO_DEBIT_REJECTED: 'Учебное списание отклонено',
};
export const auditResultLabels: Record<AuditEvent['result'], string> = {
  SUCCEEDED: 'Действие выполнено', REJECTED: 'Команда отклонена', RECORDED: 'Факт сохранён',
};

export function auditFilters(fields: { type: string; from: string; to: string; dealId?: string }): DemoAuditFilters {
  const type = fields.type.trim();
  if (type && !/^[A-Z][A-Z0-9_]{0,63}$/.test(type)) throw new Error('Проверьте тип события.');
  for (const value of [fields.from, fields.to]) {
    if (value && (!/^\d{4}-\d{2}-\d{2}$/.test(value) || value.startsWith('0000')
      || !Number.isFinite(Date.parse(value)) || new Date(value).toISOString().slice(0, 10) !== value)) {
      throw new Error('Проверьте даты периода.');
    }
  }
  if (fields.from && fields.to && fields.from > fields.to) throw new Error('Начало периода не может быть позже его окончания.');
  const dealId = fields.dealId?.trim();
  if (dealId && !/^[A-Za-z0-9_-]{1,128}$/.test(dealId)) throw new Error('Проверьте ID сделки.');
  return {
    ...(type ? { type } : {}), ...(fields.from ? { from: dateBoundary(fields.from, false) } : {}),
    ...(fields.to ? { to: dateBoundary(fields.to, true) } : {}), ...(dealId ? { deal_id: dealId } : {}),
  };
}

export function auditActor(event: AuditEvent): string {
  if (event.actor_source) return event.actor_source === 'edo-emulator' ? 'Учебный источник ЭДО' : 'Источник события';
  return event.actor_organization_id ? 'Пользователь организации' : 'Учебный оператор';
}
