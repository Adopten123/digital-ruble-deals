import { ApiError, describeError } from './lib/api';
import { auditActor, auditResultLabels, auditTypeLabels } from './lib/audit';
import { notificationFilters } from './lib/notifications';
import type useNotifications from './useNotifications';

const time = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'medium' });
const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
function Timestamp({ value }: { value: string }) {
  return <time dateTime={value}>{time.format(new Date(value))} · {zone}<small>UTC: {value}</small></time>;
}
function Failure({ error }: { error: unknown }) {
  return <div className="notice notice-error" role="alert"><span>{describeError(error)}</span>{error instanceof ApiError && error.requestId && <small>Код обращения: {error.requestId}</small>}</div>;
}
export default function Notifications({ inbox, onOpenDeal }: {
  inbox: ReturnType<typeof useNotifications>; onOpenDeal: (id: string) => void;
}) {
  return <section className="panel notifications-panel" aria-label="Уведомления организации">
    <div className="agreement-heading"><div><h2>Уведомления</h2><p className="muted">Сохранённые события ваших сделок и учебного счёта</p></div>
      <button type="button" className="button button-quiet" disabled={inbox.loading || inbox.reading != null} onClick={inbox.refresh}>Обновить уведомления</button></div>
    <p className="field-help">Доставка внутри приложения. Новые события проверяются каждые 15 секунд и при возвращении в окно. Событие сообщает о сохранённом факте; актуальное состояние открывается в карточке сделки.</p>
    <div className="notification-filters" role="group" aria-label="Фильтры уведомлений">{Object.entries(notificationFilters).map(([filter, label]) => <button key={filter} type="button" className={`button ${inbox.filter === filter ? 'button-primary' : 'button-quiet'}`} aria-pressed={inbox.filter === filter} disabled={inbox.reading != null} onClick={() => inbox.setFilter(filter as keyof typeof notificationFilters)}>{label}</button>)}</div>
    <p className="notifications-summary">Непрочитанных: <strong>{inbox.unreadCount ?? 'проверяем'}</strong> · Сначала новые доставки</p>
    {inbox.error != null && <Failure error={inbox.error} />}
    {inbox.pollError != null && <><Failure error={inbox.pollError} /><p className="field-help">Автообновление не подтверждено. Показаны последние полученные данные.</p></>}
    {inbox.readError != null && <div className="notification-read-error"><Failure error={inbox.readError} /><p>Отметка не подтверждена. Повтор проверит то же уведомление.</p>{inbox.uncertainId && <button type="button" className="button button-quiet" disabled={inbox.reading != null} onClick={() => void inbox.markRead(inbox.uncertainId!)}>Проверить отметку о прочтении</button>}</div>}
    <ol className="notification-list">{inbox.items.map(item => <li className="notification-item" key={item.id} data-notification-id={item.id}>
      <article aria-label={auditTypeLabels[item.event.type] ?? 'Событие уведомления'}><div className="notification-item-heading"><h3>{auditTypeLabels[item.event.type] ?? 'Событие уведомления'}</h3><span className={`tag ${item.read_at ? '' : 'notification-unread'}`}>{item.read_at ? 'Прочитано' : 'Новое уведомление'}</span></div>
        <p className="notification-result">{auditResultLabels[item.event.result] ?? 'Факт сохранён'} · {auditActor(item.event)}</p>
        {item.event.details.reason && <p className="notification-reason">{item.event.details.reason}</p>}
        <dl className="notification-times"><div><dt>Событие сохранено</dt><dd><Timestamp value={item.event.created_at} /></dd></div><div><dt>Доставлено в кабинет</dt><dd><Timestamp value={item.created_at} /></dd></div>{item.read_at && <div><dt>Отмечено прочитанным</dt><dd><Timestamp value={item.read_at} /></dd></div>}</dl>
        <details className="notification-details"><summary>Детали уведомления</summary>{item.event.deal_id && <p className="notification-reference">Сделка <code>{item.event.deal_id}</code>{item.event.deal_version != null && <span> · Версия факта {item.event.deal_version}</span>}</p>}
        {!item.event.deal_id && item.event.details.account_id && <p className="notification-reference">Учебный счёт <code>{item.event.details.account_id}</code></p>}</details>
        <div className="notification-actions">{item.event.deal_id && <button type="button" className="button button-quiet" onClick={() => onOpenDeal(item.event.deal_id!)}>Открыть сделку</button>}
          {!item.read_at && <button type="button" className="button button-quiet" disabled={inbox.reading != null} onClick={() => void inbox.markRead(item.id)}>{inbox.reading === item.id ? 'Сохраняем отметку…' : 'Отметить прочитанным'}</button>}</div>
      </article></li>)}</ol>
    {inbox.loading && <p role="status">Загружаем уведомления…</p>}
    {!inbox.loading && inbox.error == null && inbox.items.length === 0 && <p className="notifications-empty">{inbox.filter === 'unread' ? 'Непрочитанных уведомлений нет.' : inbox.filter === 'read' ? 'Прочитанных уведомлений пока нет.' : 'Уведомлений пока нет. События появятся после сохранения действий в сделках или учебном счёте.'}</p>}
    {inbox.cursor && !inbox.loading && inbox.error == null && <button type="button" className="button button-quiet notification-more" disabled={inbox.reading != null} onClick={inbox.more}>Показать ещё уведомления</button>}
  </section>;
}
