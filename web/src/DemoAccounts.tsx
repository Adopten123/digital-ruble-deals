import { useCallback, useEffect, useId, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { api, ApiError, describeError } from './lib/api';
import type { DemoAccount } from './lib/api';
import { definitiveDemoMoneyFailure, demoMoneyInput, demoMoneyStorageKey, readPendingDemoCommands, readPendingDemoMoney, removePendingDemoMoney, validateDemoAccount } from './lib/demoMoney';
import type { DemoMoneyOperation, PendingDemoMoney } from './lib/demoMoney';
import { formatRubles } from './lib/money';

export default function DemoAccounts({ userId, onUnauthorized }: { userId: string; onUnauthorized: (error: unknown) => void }) {
  const [query, setQuery] = useState('');
  const [activeQuery, setActiveQuery] = useState('');
  const [items, setItems] = useState<DemoAccount[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState<DemoAccount | null>(null);
  const [operation, setOperation] = useState<DemoMoneyOperation>('credits');
  const [amount, setAmount] = useState('');
  const [reason, setReason] = useState('');
  const [pending, setPending] = useState<PendingDemoMoney[]>([]);
  const [storageError, setStorageError] = useState<unknown>(null);
  const [error, setError] = useState<unknown>(null);
  const [commandError, setCommandError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const listSequence = useRef(0);
  const generation = useRef(0);
  const inFlight = useRef(false);
  const id = useId();
  useEffect(() => {
    generation.current += 1;
    try { setPending(readPendingDemoCommands(sessionStorage, userId)); }
    catch (failure) { setStorageError(failure); }
    return () => { generation.current += 1; listSequence.current += 1; };
  }, [userId]);
  const load = useCallback(async (q: string, next?: string) => {
    const sequence = ++listSequence.current;
    setLoading(true); setError(null);
    if (!next) { setItems([]); setCursor(null); }
    try {
      const page = await api.demoAccounts(q, next);
      page.items.forEach(validateDemoAccount);
      if (sequence !== listSequence.current) return;
      setItems(previous => next ? [...previous, ...page.items] : page.items); setCursor(page.next_cursor);
    } catch (failure) { if (sequence === listSequence.current) { setError(failure); onUnauthorized(failure); } }
    finally { if (sequence === listSequence.current) setLoading(false); }
  }, [onUnauthorized]);
  useEffect(() => { void load(activeQuery); return () => { listSequence.current += 1; }; }, [activeQuery, load]);

  async function currentAccount(accountId: string) {
    const page = await api.demoAccounts(accountId);
    const account = page.items.find(item => item.id === accountId);
    if (!account) throw new ApiError(404, 'RESOURCE_NOT_FOUND');
    validateDemoAccount(account);
    return account;
  }
  async function refreshSelected() {
    if (!selected || busy) return;
    const submittedGeneration = generation.current;
    const submittedId = selected.id;
    setCommandError(null); setBusy(true);
    try {
      const actual = await currentAccount(submittedId);
      if (generation.current !== submittedGeneration || api.currentIdentity() !== userId) return;
      setSelected(actual); setItems(previous => previous.map(item => item.id === actual.id ? actual : item));
    } catch (failure) { if (generation.current === submittedGeneration && api.currentIdentity() === userId) { setCommandError(failure); onUnauthorized(failure); } }
    finally { if (generation.current === submittedGeneration) setBusy(false); }
  }

  async function run(restored?: PendingDemoMoney) {
    if (inFlight.current || storageError != null || api.currentIdentity() !== userId) return;
    const submittedGeneration = generation.current;
    const isCurrent = () => generation.current === submittedGeneration && api.currentIdentity() === userId;
    inFlight.current = true; setBusy(true); setCommandError(null); setNotice('');
    let attempt = restored;
    try {
      if (!attempt) {
        if (!selected) throw new Error('Выберите учебный счёт.');
        // Re-read storage, not only React state: an existing unknown outcome must not get a new key.
        if (readPendingDemoCommands(sessionStorage, userId).length) throw new Error('Сначала проверьте результат сохранённой операции.');
        attempt = { key: crypto.randomUUID(), userId, accountId: selected.id, accountName: selected.organisation.name, operation, input: demoMoneyInput(amount, reason) };
        readPendingDemoMoney(JSON.stringify(attempt), userId);
        sessionStorage.setItem(demoMoneyStorageKey(userId, attempt.accountId, operation), JSON.stringify(attempt));
        setPending(readPendingDemoCommands(sessionStorage, userId));
      }
      if (!isCurrent()) return;
      const result = await api.demoMoney(attempt.accountId, attempt.operation, attempt.input, attempt.key);
      removePendingDemoMoney(sessionStorage, attempt);
      if (!isCurrent()) return;
      setPending(readPendingDemoCommands(sessionStorage, userId)); setAmount(''); setReason('');
      setNotice(`Учебная операция подтверждена. Код: ${result.transaction_id}.`);
      // Ignore the historic cached account in a replay response; show an independent current read.
      const actual = await currentAccount(attempt.accountId);
      if (!isCurrent()) return;
      setSelected(actual); setItems(previous => previous.map(item => item.id === actual.id ? actual : item));
    } catch (failure) {
      if (attempt && definitiveDemoMoneyFailure(failure)) {
        try { removePendingDemoMoney(sessionStorage, attempt); if (isCurrent()) setPending(readPendingDemoCommands(sessionStorage, userId)); }
        catch (storageFailure) { if (isCurrent()) setStorageError(storageFailure); }
      }
      if (attempt && failure instanceof ApiError && failure.code === 'INSUFFICIENT_AVAILABLE_FUNDS' && isCurrent()) {
        try { const actual = await currentAccount(attempt.accountId); if (isCurrent()) { setSelected(actual); setItems(previous => previous.map(item => item.id === actual.id ? actual : item)); } }
        catch (readFailure) { if (isCurrent()) onUnauthorized(readFailure); }
      }
      if (isCurrent()) { setCommandError(failure); onUnauthorized(failure); }
    } finally { inFlight.current = false; if (isCurrent()) setBusy(false); }
  }

  function search(event: FormEvent) { event.preventDefault(); const next = query.trim(); if (next === activeQuery) void load(next); else setActiveQuery(next); }
  const locked = busy || pending.length > 0 || storageError != null;
  return <section className="demo-accounts" aria-label="Учебные счета оператора">
    <div className="section-heading"><div><h2>Учебные счета</h2><p className="muted">Только тестовые средства. Операции оператора не являются оплатой сделки.</p></div></div>
    <div className="notice notice-info"><strong>Учебный оператор</strong><span>Зачисление и служебное списание отражаются проводками. Активный резерв недоступен для списания; подписи сторон оператор не создаёт.</span></div>
    {storageError != null && <Failure error={storageError} />}
    {pending.map(attempt => <div className="notice notice-info demo-pending" role="status" key={attempt.key}><strong>Операция ожидает подтверждения</strong><span>{attempt.operation === 'credits' ? 'Зачисление' : 'Служебное списание'} {formatRubles(attempt.input.amount_kopecks)} · {attempt.accountName}</span><code>{attempt.accountId}</code><span>Основание: {attempt.input.reason}</span><span>Повтор использует прежние сумму, основание и ключ. Новые операции закрыты до подтверждения.</span><button className="button button-primary" type="button" disabled={busy} onClick={() => void run(attempt)}>Проверить результат операции</button></div>)}
    {notice && <div className="notice notice-success" role="status">{notice}</div>}
    {commandError != null && <Failure error={commandError} />}
    <form className="search-form" role="search" aria-label="Поиск учебных счетов" onSubmit={search}><div className="search-field"><label className="visually-hidden" htmlFor={`${id}-query`}>Счёт или организация</label><input id={`${id}-query`} value={query} onChange={event => setQuery(event.target.value)} maxLength={400} placeholder="ID счёта, ID или название организации" /></div><button className="button button-primary" type="submit">Найти</button></form>
    <div className="deal-list-caption"><span>Сначала новые · Остатки из ответа сервера</span><button className="text-button" type="button" disabled={loading} onClick={() => void load(activeQuery)}>Обновить счета</button></div>
    {error != null && <Failure error={error} retry={() => void load(activeQuery)} />}
    <div className="demo-account-list">{items.map(account => <button className={`panel demo-account-row${selected?.id === account.id ? ' selected' : ''}`} type="button" key={account.id} disabled={busy} onClick={() => { setSelected(account); setAmount(''); setReason(''); setCommandError(null); }}><div><strong>{account.organisation.name}</strong><code>{account.id}</code><small>{account.organisation.id}</small></div><div><span>Доступно</span><strong>{formatRubles(account.available_kopecks)}</strong><span>Резерв {formatRubles(account.reserved_kopecks)}</span></div></button>)}</div>
    {loading && <p className="field-help" role="status">Загружаем учебные счета…</p>}
    {!loading && !items.length && error == null && <p className="panel">Учебные счета не найдены. Измените запрос.</p>}
    {cursor && !loading && <button className="button button-quiet load-more" type="button" onClick={() => void load(activeQuery, cursor)}>Показать ещё счета</button>}
    {selected && <section className="panel demo-money-panel" aria-label="Учебная денежная операция"><div className="agreement-heading"><div><h3>{selected.organisation.name}</h3><code>{selected.id}</code></div><button className="text-button" type="button" disabled={busy} onClick={() => void refreshSelected()}>Обновить выбранный счёт</button></div>
      <dl className="reserve-balances"><div><dt>Общий остаток</dt><dd>{formatRubles(selected.total_kopecks)}</dd></div><div><dt>Зарезервировано</dt><dd>{formatRubles(selected.reserved_kopecks)}</dd></div><div><dt>Доступно</dt><dd>{formatRubles(selected.available_kopecks)}</dd></div></dl>
      <form onSubmit={event => { event.preventDefault(); void run(); }}><fieldset disabled={locked}><label htmlFor={`${id}-operation`}>Операция с тестовыми средствами</label><select id={`${id}-operation`} value={operation} onChange={event => setOperation(event.target.value as DemoMoneyOperation)}><option value="credits">Учебное зачисление</option><option value="debits">Служебное списание</option></select><label htmlFor={`${id}-amount`}>Сумма операции, ₽</label><input id={`${id}-amount`} inputMode="decimal" autoComplete="off" value={amount} onChange={event => setAmount(event.target.value)} maxLength={24} required /><label htmlFor={`${id}-reason`}>Основание операции</label><textarea id={`${id}-reason`} rows={3} value={reason} onChange={event => setReason(event.target.value)} maxLength={4000} required /><p className="field-help">До 2 000 символов. Служебное списание переводит средства на системный учебный счёт, без оплаты исполнителю. Разрешение и доступный остаток проверяет сервер.</p></fieldset><button className="button button-primary" type="submit" disabled={locked}>{busy ? 'Подтверждаем…' : operation === 'credits' ? 'Зачислить тестовые средства' : 'Списать доступные средства'}</button></form>
    </section>}
  </section>;
}

function Failure({ error, retry }: { error: unknown; retry?: () => void }) {
  return <div className="notice notice-error" role="alert"><span>{describeError(error)}</span>{error instanceof ApiError && error.requestId && <small>Код обращения: {error.requestId}</small>}{retry && <button className="text-button" type="button" onClick={retry}>Повторить запрос</button>}</div>;
}
