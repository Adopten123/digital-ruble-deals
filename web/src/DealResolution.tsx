import { useEffect, useId, useState } from 'react';
import type { Deal } from './lib/api';
import { canDecideCancellation, canDispute, canRequestCancellation, terminalDeal } from './lib/blockers';

export default function DealResolution({ deal, ownId, locked, onCommand }: {
  deal: Deal; ownId: string; locked: boolean; onCommand: (kind: 'dispute' | 'cancel-request' | 'cancel-accept' | 'cancel-reject', reason?: string) => void;
}) {
  const id = useId();
  const [form, setForm] = useState<'dispute' | 'cancel-request' | 'cancel-reject' | null>(null);
  const [reason, setReason] = useState('');
  const [accept, setAccept] = useState(false);
  const request = deal.cancellation_request;
  useEffect(() => { setAccept(false); setForm(null); setReason(''); }, [request?.id]);
  const decide = canDecideCancellation(deal, ownId);
  const available = form === 'dispute' ? canDispute(deal, ownId) : form === 'cancel-request' ? canRequestCancellation(deal, ownId) : form === 'cancel-reject' ? decide : false;
  const labels = { dispute: ['Причина спора', 'Подтвердить открытие спора'], 'cancel-request': ['Причина отмены', 'Отправить запрос отмены'], 'cancel-reject': ['Причина отказа', 'Подтвердить отказ от отмены'] };
  function show(next: typeof form) { setForm(form === next ? null : next); setReason(''); }
  return <section className="deal-resolution" aria-label="Спор и отмена"><h4>Спор и согласованная отмена</h4>
    {deal.payment_id && <div className="notice notice-success" role="status"><strong>Выплата выполнена один раз</strong><span>Тестовые средства перечислены исполнителю. Поздний спор или отмена не изымают выполненный платёж.</span><code>{deal.payment_id}</code></div>}
    {deal.status === 'CANCELLED' && <div className="notice notice-info" role="status"><strong>Сделка отменена по согласию сторон</strong><span>Резерв не создавался, денежного движения нет.</span></div>}
    {deal.status === 'REFUNDED' && <div className="notice notice-success" role="status"><strong>Резерв освобождён по согласию сторон</strong><span>Сумма снова доступна заказчику. Это освобождение ещё не выплаченных средств, а не возврат после расчёта.</span></div>}
    {deal.dispute && <div className="notice notice-review"><strong>Открыт спор</strong><span>{deal.dispute.organisation_id === ownId ? 'Ваша организация' : 'Другая сторона'} · {new Date(deal.dispute.created_at).toLocaleString('ru-RU')}</span><p className="resolution-reason">{deal.dispute.reason}</p><span>{terminalDeal(deal) ? 'Факт спора сохранён в завершённой сделке.' : 'Автоматическая выплата закрыта. Стороны могут согласовать отмену; автоматического арбитража и возобновления выплаты нет.'}</span></div>}
    {deal.review_reasons.length > 0 && <div className="notice notice-review"><strong>Причины проверки</strong><ul>{deal.review_reasons.map(reason => <li key={reason}>{reason === 'DEADLINE_PASSED' ? 'Срок сделки наступил. Это не подтверждает отсутствие выполненных работ.' : reason === 'CANCELLATION_REJECTED' ? 'Другая сторона отклонила отмену. Выплата автоматически не возобновляется.' : 'Требуется проверка сторонами.'}</li>)}</ul></div>}
    {request && <div className="notice notice-info"><strong>Активный запрос отмены</strong><span>{request.initiator_organisation_id === ownId ? 'Запросила ваша организация. Ожидается решение другой стороны.' : 'Другая сторона просит согласовать отмену.'}</span><p className="resolution-reason">{request.reason}</p><span>Один запрос не освобождает резерв. Спор или наступивший срок не мешают второй стороне согласовать отмену.</span><code>{request.id}</code></div>}
    {!terminalDeal(deal) && <p className="field-help">Спор и запрос отмены блокируют новую выплату. Окончательный исход конкурентных действий подтверждает сервер.</p>}
    <div className="agreement-actions">{canDispute(deal, ownId) && <button className="button button-quiet" disabled={locked} type="button" onClick={() => show('dispute')}>Открыть спор</button>}
      {canRequestCancellation(deal, ownId) && <button className="button button-quiet" disabled={locked} type="button" onClick={() => show('cancel-request')}>Запросить отмену</button>}
      {decide && <button className="button button-quiet" disabled={locked} type="button" onClick={() => show('cancel-reject')}>Отклонить отмену</button>}
    </div>
    {decide && <div className="cancellation-accept"><label className="checkbox-label" htmlFor={`${id}-accept`}><input id={`${id}-accept`} type="checkbox" checked={accept} disabled={locked} onChange={event => setAccept(event.target.checked)} /><span>Согласен отменить сделку. Если средства ещё в резерве, они станут доступны заказчику без выплаты исполнителю.</span></label><button className="button button-primary" type="button" disabled={locked || !accept} onClick={() => onCommand('cancel-accept')}>Принять отмену</button></div>}
    {form && available && <form className="resolution-form" onSubmit={event => { event.preventDefault(); onCommand(form, reason); }}><label htmlFor={`${id}-reason`}>{labels[form][0]}</label><textarea id={`${id}-reason`} rows={3} maxLength={4000} required value={reason} disabled={locked} onChange={event => setReason(event.target.value)} /><p className="field-help">От 1 до 2 000 символов. Причина будет сохранена для обеих сторон.</p><button className="button button-primary" type="submit" disabled={locked}>{labels[form][1]}</button></form>}
  </section>;
}
