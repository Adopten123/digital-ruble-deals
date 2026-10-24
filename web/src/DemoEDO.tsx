import { useEffect, useId, useRef, useState } from 'react';
import { api, ApiError, describeError } from './lib/api';
import type { EDODelivery, EDOEventTemplate } from './lib/api';
import { definitiveEDOFailure, edoId, edoInput, edoOutcome, edoStorageKey, edoTemplates, edoWatchKey, readPendingEDO, removePendingEDO } from './lib/edo';
import type { PendingEDO } from './lib/edo';

export default function DemoEDO({ userId, onUnauthorized }: { userId: string; onUnauthorized: (error: unknown) => void }) {
  const [dealId, setDealId] = useState('');
  const [template, setTemplate] = useState<EDOEventTemplate>('valid');
  const [replayId, setReplayId] = useState('');
  const [lookup, setLookup] = useState('');
  const [eventId, setEventId] = useState('');
  const [revision, setRevision] = useState(0);
  const [delivery, setDelivery] = useState<EDODelivery | null>(null);
  const [pending, setPending] = useState<PendingEDO | null>(null);
  const [storageError, setStorageError] = useState<unknown>(null);
  const [error, setError] = useState<unknown>(null);
  const [readError, setReadError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [reading, setReading] = useState(false);
  const generation = useRef(0);
  const inFlight = useRef(false);
  const id = useId();
  useEffect(() => {
    generation.current += 1;
    try {
      setPending(readPendingEDO(sessionStorage.getItem(edoStorageKey(userId)), userId));
      const saved = sessionStorage.getItem(edoWatchKey(userId));
      if (saved !== null) { setEventId(edoId(saved)); setLookup(saved); }
    } catch (failure) { setStorageError(failure); }
    return () => { generation.current += 1; };
  }, [userId]);
  useEffect(() => {
    if (!eventId) return;
    let current = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    setDelivery(null);
    async function load() {
      setReading(true); setReadError(null);
      try {
        const result = await api.edoEvent(eventId);
        if (!current || api.currentIdentity() !== userId) return;
        if (result.event_id !== eventId) throw new Error('Сервер вернул другое событие. Результат нельзя подтвердить.');
        setDelivery(result);
        if (result.status === 'PENDING') timer = setTimeout(() => void load(), 2000);
      } catch (failure) { if (current && api.currentIdentity() === userId) { setReadError(failure); onUnauthorized(failure); } }
      finally { if (current) setReading(false); }
    }
    void load();
    return () => { current = false; if (timer) clearTimeout(timer); };
  }, [eventId, revision, userId, onUnauthorized]);
  function watch(value: string) {
    const selected = edoId(value);
    sessionStorage.setItem(edoWatchKey(userId), selected);
    setLookup(selected); setEventId(selected); setRevision(value => value + 1);
  }
  async function run() {
    if (inFlight.current || storageError != null || api.currentIdentity() !== userId) return;
    const submitted = generation.current;
    const current = () => generation.current === submitted && api.currentIdentity() === userId;
    inFlight.current = true; setBusy(true); setError(null); setNotice('');
    let attempt = pending;
    try {
      if (!attempt) {
        if (sessionStorage.getItem(edoStorageKey(userId)) !== null) throw new Error('Сначала проверьте результат сохранённой отправки.');
        attempt = { userId, key: crypto.randomUUID(), input: edoInput(dealId, template, replayId) };
        readPendingEDO(JSON.stringify(attempt), userId);
        sessionStorage.setItem(edoStorageKey(userId), JSON.stringify(attempt)); setPending(attempt);
      }
      const queued = await api.queueEDO(attempt.input, attempt.key);
      removePendingEDO(sessionStorage, attempt);
      if (!current()) return;
      setPending(null); setNotice(`Задание принято. Код: ${queued.job_id}. Это ещё не подтверждение выплаты.`);
      watch(queued.event_id);
    } catch (failure) {
      if (attempt && definitiveEDOFailure(failure)) {
        try { removePendingEDO(sessionStorage, attempt); if (current()) setPending(null); }
        catch (storageFailure) { if (current()) setStorageError(storageFailure); }
      }
      if (current()) { setError(failure); onUnauthorized(failure); }
    } finally { inFlight.current = false; if (current()) setBusy(false); }
  }
  const locked = busy || pending != null || storageError != null;
  const outcome = delivery ? edoOutcome(delivery) : null;
  return <section className="demo-edo" aria-label="Учебные события ЭДО"><div className="section-heading"><div><h2>События ЭДО</h2><p className="muted">Проверка условного расчёта на тестовых средствах</p></div><span className="tag">Учебный оператор</span></div>
    <p className="field-help">Две подписи автоматически запускают доставку события. Здесь можно проверить повтор или ошибочные сценарии. Оператор не подписывает документы за стороны и не может обойти условия выплаты.</p>
    {storageError != null && <div className="notice notice-error" role="alert">{describeError(storageError)}</div>}
    {pending && <div className="notice notice-info" role="status"><strong>Отправка ЭДО ожидает подтверждения</strong><span>Сделка {pending.input.deal_id} · {edoTemplates[pending.input.template]}</span>{pending.input.replay_event_id && <span>Исходное событие: {pending.input.replay_event_id}</span>}<span>Повтор проверит тот же запрос с исходным ключом. Новая отправка закрыта.</span><button className="button button-primary" disabled={busy} type="button" onClick={() => void run()}>Проверить результат отправки ЭДО</button></div>}
    {error != null && <div className="notice notice-error" role="alert">{describeError(error)}</div>}
    {notice && <div className="notice notice-success" role="status">{notice}</div>}
    <form className="panel edo-form" onSubmit={event => { event.preventDefault(); void run(); }}><fieldset disabled={locked}><label htmlFor={`${id}-deal`}>ID сделки для события</label><input id={`${id}-deal`} value={dealId} onChange={event => setDealId(event.target.value)} maxLength={256} required />
      <label htmlFor={`${id}-template`}>Учебный сценарий ЭДО</label><select id={`${id}-template`} value={template} onChange={event => setTemplate(event.target.value as EDOEventTemplate)}>{Object.entries(edoTemplates).map(([key, label]) => <option value={key} key={key}>{label}</option>)}</select>
      {(template === 'replay' || template === 'same_id_different_body') && <><label htmlFor={`${id}-replay`}>ID исходного события</label><input id={`${id}-replay`} value={replayId} onChange={event => setReplayId(event.target.value)} maxLength={256} required /></>}
    </fieldset><button type="submit" className="button button-primary" disabled={locked}>Отправить учебное событие</button></form>
    <form className="panel edo-lookup" role="search" aria-label="Проверка события ЭДО" onSubmit={event => { event.preventDefault(); try { setError(null); watch(lookup); } catch (failure) { setError(failure); } }}><label htmlFor={`${id}-event`}>ID события для проверки</label><div className="edo-lookup-row"><input id={`${id}-event`} value={lookup} onChange={event => setLookup(event.target.value)} maxLength={256} required /><button className="button button-quiet" type="submit">Проверить событие</button></div></form>
    {reading && !delivery && <p role="status">Получаем состояние доставки…</p>}
    {readError != null && <div className="notice notice-error" role="alert">{describeError(readError)}<button className="text-button" type="button" onClick={() => setRevision(value => value + 1)}>Повторить проверку события</button></div>}
    {delivery && <section className="panel edo-delivery" aria-label="Результат события ЭДО"><h3>{outcome === 'pending' ? 'Событие ожидает обработки' : outcome === 'settled' ? 'Расчёт подтверждён' : outcome === 'already_settled' ? 'Прежний расчёт подтверждён' : outcome === 'rejected' ? 'Событие доставлено, выплата отклонена' : 'Результат расчёта не подтверждён'}</h3>
      <dl className="document-facts"><div><dt>Событие</dt><dd><code>{delivery.event_id}</code></dd></div><div><dt>Сделка</dt><dd><code>{delivery.deal_id}</code></dd></div><div><dt>Сценарий</dt><dd>{edoTemplates[delivery.template]}</dd></div><div><dt>Доставка</dt><dd>{delivery.status === 'PENDING' ? 'В очереди, проверяем автоматически' : 'Доставлено обработчику'}</dd></div><div><dt>Попытки</dt><dd>{delivery.attempts}</dd></div><div><dt>Ответ</dt><dd>{delivery.http_status ?? 'Ещё нет'}</dd></div></dl>
      {outcome === 'rejected' && <p className="notice notice-review">{describeError(new ApiError(delivery.http_status ?? 422, delivery.error_code ?? 'UNKNOWN_ERROR'))}</p>}
      {(outcome === 'settled' || outcome === 'already_settled') && delivery.result && 'payment_id' in delivery.result && <p>Единственный платёж: <code>{delivery.result.payment_id}</code></p>}
      <button className="button button-quiet" type="button" disabled={reading} onClick={() => setRevision(value => value + 1)}>Обновить состояние события</button>
    </section>}
  </section>;
}
