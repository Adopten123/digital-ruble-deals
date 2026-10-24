import { useCallback, useEffect, useId, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { api, ApiError, describeError } from './lib/api';
import type { AuditEvent, AuditExportFormat, DemoAuditFilters } from './lib/api';
import { auditActor, auditFilters, auditResultLabels, auditTypeLabels } from './lib/audit';
import { statusLabels } from './lib/blockers';
import { formatRubles } from './lib/money';

const localTime = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'medium' });
const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
const historyStatusLabels: Record<string, string> = { ...statusLabels, ALREADY_SETTLED: 'Расчёт ранее исполнен', PENDING: 'Ожидает решения', ACCEPTED: 'Отмена согласована', REJECTED: 'Отказ в отмене' };
type Fields = { type: string; from: string; to: string; dealId: string };
const emptyFields: Fields = { type: '', from: '', to: '', dealId: '' };

function Failure({ error, retry }: { error: unknown; retry?: () => void }) {
  return <div className="notice notice-error" role="alert"><span>{describeError(error)}</span>
    {error instanceof ApiError && error.requestId && <small>Код обращения: {error.requestId}</small>}
    {retry && <button type="button" className="text-button" onClick={retry}>Повторить загрузку истории</button>}
  </div>;
}

function EventCard({ event, operator }: { event: AuditEvent; operator: boolean }) {
  const detail = event.details;
  const references: [string, string | number | undefined | null][] = [
    ['Сделка', operator ? event.deal_id : undefined], ['Версия условий', event.deal_version],
    ['Документ', detail.document_id], ['Версия документа', detail.document_version], ['SHA-256 документа', detail.document_sha256],
    ['Запрос отмены', detail.cancellation_request_id], ['Резерв', detail.reserve_id], ['Платёж', detail.payment_id], ['Событие ЭДО', detail.event_id],
    ['Учебный счёт', detail.account_id], ['Учебная проводка', detail.transaction_id],
  ];
  return <li className="audit-event"><article aria-label={auditTypeLabels[event.type] ?? 'Событие истории'}>
    <div className="audit-event-heading"><div><h4>{auditTypeLabels[event.type] ?? 'Событие истории'}</h4><code>{event.type}</code></div>
      <span className={`tag audit-result audit-result-${event.result.toLowerCase()}`}>{auditResultLabels[event.result] ?? 'Факт сохранён'}</span></div>
    <p className="audit-time"><time dateTime={event.created_at}>{localTime.format(new Date(event.created_at))} · {zone}</time><small>UTC: {event.created_at}</small></p>
    <p className="audit-actor">{auditActor(event)}{event.actor_source && <code>{event.actor_source}</code>}</p>
    {detail.reason && <div className="audit-reason"><strong>Причина</strong><p>{detail.reason}</p></div>}
    {detail.decision && <p className="audit-decision">Решение: {detail.decision === 'ACCEPTED' ? 'отмена согласована' : detail.decision === 'REJECTED' ? 'отказ в отмене' : detail.decision}</p>}
    {detail.amount_kopecks && <p>Сумма: {detail.currency === 'RUB' ? formatRubles(detail.amount_kopecks) : `${detail.amount_kopecks} коп. ${detail.currency ?? ''}`}</p>}
    {event.error_code && <p className="audit-error">{describeError(new ApiError(422, event.error_code))}<code>{event.error_code}</code></p>}
    <details className="audit-event-details"><summary>Подробности события</summary><dl className="audit-facts">
      {references.filter(([, value]) => value != null).map(([label, value]) => <div key={label}><dt>{label}</dt><dd><code>{value}</code></dd></div>)}
      {detail.status && <div><dt>Состояние</dt><dd>{historyStatusLabels[detail.status] ?? detail.status}</dd></div>}
      {event.actor_organization_id && <div><dt>Организация</dt><dd><code>{event.actor_organization_id}</code></dd></div>}
      {event.actor_user_id && <div><dt>Пользователь</dt><dd><code>{event.actor_user_id}</code></dd></div>}
      <div><dt>ID записи</dt><dd><code>{event.id}</code></dd></div>
      <div><dt>Код обращения</dt><dd><code>{event.request_id}</code></dd></div>
      {detail.operation && <div><dt>Операция</dt><dd><code>{detail.operation}</code></dd></div>}
    </dl></details>
  </article></li>;
}

export default function AuditHistory({ dealId, userId, revision, onUnauthorized }: {
  dealId?: string; userId: string; revision?: string; onUnauthorized: (error: unknown) => void;
}) {
  const operator = dealId === undefined;
  const [fields, setFields] = useState<Fields>(emptyFields);
  const [applied, setApplied] = useState<Fields>(emptyFields);
  const [filters, setFilters] = useState<DemoAuditFilters>({});
  const [items, setItems] = useState<AuditEvent[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [filterError, setFilterError] = useState<unknown>(null);
  const [exportError, setExportError] = useState<unknown>(null);
  const [exporting, setExporting] = useState<AuditExportFormat | null>(null);
  const [download, setDownload] = useState<{ url: string; name: string } | null>(null);
  const sequence = useRef(0);
  const exportSequence = useRef(0);
  const formId = useId();
  const dirty = Object.keys(fields).some(key => fields[key as keyof Fields] !== applied[key as keyof Fields]);
  const clearDownload = useCallback(() => {
    exportSequence.current += 1; setDownload(null); setExportError(null); setExporting(null);
  }, []);
  useEffect(() => () => { if (download) URL.revokeObjectURL(download.url); }, [download]);
  useEffect(() => { clearDownload(); }, [fields, clearDownload]);
  const load = useCallback(async (active: DemoAuditFilters, next?: string) => {
    const request = ++sequence.current;
    setLoading(true); setError(null);
    if (!next) { setItems([]); setCursor(null); clearDownload(); }
    try {
      const page = dealId === undefined ? await api.demoAudit(active, next) : await api.dealAudit(dealId, active, next);
      if (request !== sequence.current || api.currentIdentity() !== userId) return;
      setItems(previous => next ? [...previous, ...page.items] : page.items); setCursor(page.next_cursor);
    } catch (failure) {
      if (request === sequence.current && api.currentIdentity() === userId) { setError(failure); onUnauthorized(failure); }
    } finally { if (request === sequence.current && api.currentIdentity() === userId) setLoading(false); }
  }, [dealId, userId, onUnauthorized, clearDownload]);
  useEffect(() => {
    void load(filters);
    return () => { sequence.current += 1; exportSequence.current += 1; };
  }, [filters, load, revision]);
  function search(event: FormEvent) {
    event.preventDefault();
    try { const next = auditFilters(fields); setFilterError(null); setApplied({ ...fields }); setFilters(next); }
    catch (failure) {
      sequence.current += 1; setFilterError(failure); setError(null); setItems([]); setCursor(null); setLoading(false); clearDownload();
    }
  }
  async function exportHistory(format: AuditExportFormat) {
    if (!dealId || dirty || filterError != null || exporting || api.currentIdentity() !== userId) return;
    const request = ++exportSequence.current;
    setDownload(null); setExporting(format); setExportError(null);
    try {
      const blob = await api.exportDealAudit(dealId, format, filters);
      if (request !== exportSequence.current || api.currentIdentity() !== userId) return;
      setDownload({ url: URL.createObjectURL(blob), name: `poaktu-audit-${dealId}.${format}` });
    } catch (failure) {
      if (request === exportSequence.current && api.currentIdentity() === userId) { setExportError(failure); onUnauthorized(failure); }
    } finally { if (request === exportSequence.current && api.currentIdentity() === userId) setExporting(null); }
  }
  return <section className={`panel audit-history${operator ? ' operator-audit' : ''}`} aria-label={operator ? 'Аудит оператора' : 'История сделки'}>
    <div className="agreement-heading"><div><h3>{operator ? 'Аудит' : 'История сделки'}</h3><p className="muted">{operator ? 'Сохранённые действия и результаты учебного контура' : 'Действия сторон, версии документов и результат расчёта'}</p></div>
      <button type="button" className="button button-quiet" disabled={loading || filterError != null} onClick={() => void load(filters)}>Обновить историю</button></div>
    <form className="audit-filters" role="search" aria-label={operator ? 'Фильтры аудита оператора' : 'Фильтры истории сделки'} onSubmit={search}>
      {operator && <div><label htmlFor={`${formId}-deal`}>ID сделки в аудите</label><input id={`${formId}-deal`} value={fields.dealId} maxLength={128} onChange={event => setFields(previous => ({ ...previous, dealId: event.target.value }))} /></div>}
      <div className="audit-filter-grid"><div><label htmlFor={`${formId}-type`}>Тип события</label><select id={`${formId}-type`} value={fields.type} onChange={event => setFields(previous => ({ ...previous, type: event.target.value }))}>
        <option value="">Все события</option>{Object.entries(auditTypeLabels).map(([type, label]) => <option value={type} key={type}>{label}</option>)}
      </select></div><div><label htmlFor={`${formId}-from`}>События с даты</label><input type="date" id={`${formId}-from`} value={fields.from} onChange={event => setFields(previous => ({ ...previous, from: event.target.value }))} /></div>
        <div><label htmlFor={`${formId}-to`}>По дату включительно</label><input type="date" id={`${formId}-to`} value={fields.to} onChange={event => setFields(previous => ({ ...previous, to: event.target.value }))} /></div></div>
      <p className="field-help">Полные календарные дни: {zone}. Время событий дополнительно показано в UTC.</p>
      <div className="audit-actions"><button type="submit" className="button button-primary">Применить фильтры истории</button>
        {(dirty || Object.keys(filters).length > 0 || filterError != null) && <button type="button" className="text-button" onClick={() => { setFields(emptyFields); setApplied(emptyFields); setFilterError(null); setFilters({}); }}>Сбросить фильтры истории</button>}</div>
    </form>
    {filterError != null && <Failure error={filterError} />}
    {dirty && filterError == null && <p className="field-help" role="status">Примените изменённые фильтры, чтобы обновить историю и выгрузку.</p>}
    <p className="audit-caption">Сначала новые · {operator ? 'Доступ учебного оператора' : 'Только доступные вашей организации версии'}. Новые события появляются после обновления.</p>
    {error != null && <Failure error={error} retry={() => void load(filters, items.length ? cursor ?? undefined : undefined)} />}
    <ol className="audit-events">{items.map(event => <EventCard key={event.id} event={event} operator={operator} />)}</ol>
    {loading && <p role="status">Загружаем историю…</p>}
    {!loading && error == null && filterError == null && items.length === 0 && <p className="audit-empty">События не найдены. Измените фильтры или обновите историю.</p>}
    {cursor && !loading && error == null && filterError == null && <button type="button" className="button button-quiet" onClick={() => void load(filters, cursor)}>Показать ещё события</button>}
    {!operator && <div className="audit-export"><h4>Выгрузка истории</h4><p className="field-help">Все доступные события по применённым фильтрам, включая следующие страницы. Предел — 5 000 записей. CSV защищён от запуска формул; JSON сохраняет исходные текстовые значения.</p>
      <div className="audit-actions">{(['csv', 'json'] as const).map(format => <button className="button button-quiet" key={format} type="button" disabled={loading || dirty || filterError != null || exporting != null} onClick={() => void exportHistory(format)}>{exporting === format ? 'Готовим файл…' : `Подготовить ${format.toUpperCase()}`}</button>)}</div>
      {exportError != null && <Failure error={exportError} />}
      {download && <p className="audit-download" role="status">Файл готов: <a href={download.url} download={download.name}>Скачать {download.name.endsWith('.csv') ? 'CSV' : 'JSON'} истории</a></p>}
    </div>}
  </section>;
}
