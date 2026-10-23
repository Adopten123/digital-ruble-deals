import { useCallback, useEffect, useId, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { api, ApiError, describeError } from './lib/api';
import type { Counterparty, Deal, DealFilters, Organisation } from './lib/api';
import { formatRubles } from './lib/money';
import { createDealInput, dateBoundary, localDateTime, pendingStorageKey, plainRubles, readPendingDeal, removePendingDeal } from './lib/deals';
import type { PendingDealAttempt } from './lib/deals';
import SupplierPicker from './SupplierPicker';
import DealAgreement from './DealAgreement';

type ScopeProps = { onUnauthorized: (error: unknown) => void };
const dateFormatter = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'long', year: 'numeric', hour: '2-digit', minute: '2-digit' });
function dateLabel(value: string): string { return dateFormatter.format(new Date(value)); }
function Status({ deal }: { deal: Deal }) {
  return <span className={`deal-status${deal.status === 'REVIEW_REQUIRED' ? ' deal-status-review' : ''}`}>
    <span aria-hidden="true" />{deal.status === 'REVIEW_REQUIRED' ? 'Требует проверки' : deal.status === 'READY_TO_SETTLE' ? 'Ожидает расчёта' : deal.status === 'FUNDED' ? 'Средства зарезервированы' : deal.status === 'AGREED' ? 'Согласовано' : 'Черновик'}
  </span>;
}
function Failure({ error, retry }: { error: unknown; retry?: () => void }) {
  return <div className="notice notice-error" role="alert"><span>{describeError(error)}</span>
    {error instanceof ApiError && error.requestId && <small>Код обращения: {error.requestId}</small>}
    {retry && <button className="text-button" type="button" onClick={retry}>Повторить запрос</button>}
  </div>;
}
function Busy({ children }: { children: string }) { return <div className="loading" role="status"><span className="spinner" aria-hidden="true" />{children}</div>; }

export default function Deals({ organisation, onUnauthorized }: ScopeProps & { organisation: Organisation }) {
  const [view, setView] = useState<'list' | 'create' | 'detail'>('list');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [revision, setRevision] = useState(0);
  const [created, setCreated] = useState(false);
  const [pending, setPending] = useState<PendingDealAttempt | null>(null);
  const [storageError, setStorageError] = useState<unknown>(null);

  useEffect(() => {
    try { setPending(readPendingDeal(sessionStorage.getItem(pendingStorageKey(organisation.id)))); }
    catch (error) { setStorageError(error); }
  }, [organisation.id]);

  function open(id: string) { setSelectedId(id); setCreated(false); setView('detail'); }
  return <section className="deals-section" aria-label="Сделки организации">
    {view === 'list' && <>
      <div className="section-heading deals-heading"><div><h2>Сделки</h2><p className="muted">Условия и сроки ваших договорённостей</p></div>
        <button className="button button-primary" type="button" disabled={storageError != null} onClick={() => setView('create')}><span aria-hidden="true">＋</span>{pending ? 'Продолжить создание' : 'Новая сделка'}</button>
      </div>
      {storageError != null && <Failure error={storageError} />}
      {pending && <div className="notice notice-info"><strong>Один запрос ожидает подтверждения</strong><span>Проверьте результат создания «{pending.input.title}». Повтор сохранит те же условия и не создаст вторую сделку.</span><button className="text-button" type="button" onClick={() => setView('create')}>Проверить результат</button></div>}
      <DealList key={revision} ownId={organisation.id} onOpen={open} onUnauthorized={onUnauthorized} />
    </>}
    {view === 'create' && <CreateDeal organisation={organisation} pending={pending} onPending={setPending} onUnauthorized={onUnauthorized}
      onBack={() => setView('list')} onCreated={(deal) => { setSelectedId(deal.id); setRevision((value) => value + 1); setCreated(true); setView('detail'); }} />}
    {view === 'detail' && selectedId && <DealDetail id={selectedId} ownId={organisation.id} created={created} onUnauthorized={onUnauthorized} onBack={() => { setCreated(false); setView('list'); }} />}
  </section>;
}

function DealList({ ownId, onOpen, onUnauthorized }: ScopeProps & { ownId: string; onOpen: (id: string) => void }) {
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<DealFilters['status']>('');
  const [role, setRole] = useState<DealFilters['role']>('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [filters, setFilters] = useState<DealFilters>({});
  const [items, setItems] = useState<Deal[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [filterError, setFilterError] = useState<unknown>(null);
  const sequence = useRef(0);
  const formId = useId();
  const load = useCallback(async (active: DealFilters, nextCursor?: string) => {
    const request = ++sequence.current;
    setLoading(true); setError(null);
    if (!nextCursor) { setItems([]); setCursor(null); }
    try {
      const page = await api.deals(active, nextCursor);
      if (request !== sequence.current) return;
      setItems((previous) => nextCursor ? [...previous, ...page.items] : page.items);
      setCursor(page.next_cursor);
    } catch (failure) { if (request === sequence.current) { setError(failure); onUnauthorized(failure); } }
    finally { if (request === sequence.current) setLoading(false); }
  }, [onUnauthorized]);
  useEffect(() => { void load(filters); return () => { sequence.current += 1; }; }, [filters, load]);
  function search(event: FormEvent) {
    event.preventDefault();
    try {
      if (from && to && from > to) throw new Error('Начало периода не может быть позже его окончания.');
      const next = { q: query.trim(), status, role, created_from: dateBoundary(from, false), created_to: dateBoundary(to, true) };
      setFilterError(null);
      setFilters(next);
    } catch (failure) {
      sequence.current += 1;
      setFilterError(failure); setError(null); setItems([]); setCursor(null); setLoading(false);
    }
  }
  const filtered = Object.values(filters).some(Boolean);
  const canReset = filtered || filterError != null || Boolean(query || status || role || from || to);
  return <>
    <form className="deal-filters panel" onSubmit={search} role="search" aria-label="Поиск сделок">
      <div className="deal-search-row"><label className="visually-hidden" htmlFor={`${formId}-query`}>Поиск по названию, ID или контрагенту</label><div className="search-field"><span aria-hidden="true">⌕</span><input id={`${formId}-query`} value={query} onChange={(event) => setQuery(event.target.value)} maxLength={400} placeholder="Название, ID или контрагент" /></div><button className="button button-primary" type="submit" disabled={loading}>Найти</button></div>
      <div className="deal-filter-fields">
        <div><label htmlFor={`${formId}-status`}>Состояние</label><select id={`${formId}-status`} value={status} onChange={(event) => setStatus(event.target.value as DealFilters['status'])}><option value="">Все состояния</option><option value="DRAFT">Черновик</option><option value="AGREED">Согласовано</option><option value="FUNDED">Средства зарезервированы</option><option value="READY_TO_SETTLE">Ожидает расчёта</option><option value="REVIEW_REQUIRED">Требует проверки</option></select></div>
        <div><label htmlFor={`${formId}-role`}>Моя роль</label><select id={`${formId}-role`} value={role} onChange={(event) => setRole(event.target.value as DealFilters['role'])}><option value="">Любая роль</option><option value="customer">Заказчик</option><option value="supplier">Исполнитель</option></select></div>
        <div><label htmlFor={`${formId}-from`}>Создана с</label><input type="date" id={`${formId}-from`} value={from} onChange={(event) => setFrom(event.target.value)} /></div>
        <div><label htmlFor={`${formId}-to`}>По дату включительно</label><input type="date" id={`${formId}-to`} value={to} onChange={(event) => setTo(event.target.value)} /></div>
      </div>
      {canReset && <button className="text-button clear-filters" type="button" onClick={() => { setQuery(''); setStatus(''); setRole(''); setFrom(''); setTo(''); setFilterError(null); setFilters({}); }}>Сбросить фильтры</button>}
    </form>
    <div className="deal-list-caption"><span>Сначала новые · Только сделки вашей организации</span><button className="text-button" type="button" disabled={loading || filterError != null} onClick={() => void load(filters)}>Обновить</button></div>
    {filterError != null && <Failure error={filterError} />}
    {error != null && <Failure error={error} retry={() => void load(filters, items.length ? cursor ?? undefined : undefined)} />}
    <div className="deal-list">
      {items.map((deal) => { const customer = deal.customer_party_id === ownId; return <button className="panel deal-row" key={deal.id} type="button" onClick={() => onOpen(deal.id)}>
        <div className="deal-row-main"><div className="deal-row-meta"><Status deal={deal} /><span>Вы — {customer ? 'заказчик' : 'исполнитель'}</span></div><h3>{deal.title}</h3><p>{customer ? deal.supplier.name : deal.customer.name}</p><code>{deal.id}</code></div>
        <div className="deal-row-amount"><strong>{formatRubles(deal.amount_kopecks)}</strong><span>Срок: {dateLabel(deal.deadline_at)}</span><span className="deal-open">Открыть <span aria-hidden="true">↗</span></span></div>
      </button>; })}
      {loading && <div className="panel"><Busy>{items.length ? 'Загружаем следующие сделки' : 'Загружаем сделки'}</Busy></div>}
      {!loading && error == null && filterError == null && !items.length && <div className="panel empty-state deal-empty"><span aria-hidden="true">▤</span><h3>{filtered ? 'Сделки не найдены' : 'Здесь начнутся ваши сделки'}</h3><p>{filtered ? 'Измените запрос или сбросьте фильтры.' : 'Создайте первую сделку: выберите исполнителя, укажите условия, сумму и срок.'}</p></div>}
    </div>
    {cursor && !loading && error == null && filterError == null && <button className="button button-quiet load-more" type="button" onClick={() => void load(filters, cursor)}>Показать ещё</button>}
  </>;
}

function CreateDeal({ organisation, pending, onPending, onCreated, onBack, onUnauthorized }: ScopeProps & {
  organisation: Organisation; pending: PendingDealAttempt | null; onPending: (attempt: PendingDealAttempt | null) => void;
  onCreated: (deal: Deal) => void; onBack: () => void;
}) {
  const [supplier, setSupplier] = useState<Counterparty | null>(pending?.supplier ?? null);
  const [title, setTitle] = useState(pending?.input.title ?? '');
  const [description, setDescription] = useState(pending?.input.description ?? '');
  const [amount, setAmount] = useState(pending ? plainRubles(pending.input.amount_kopecks) : '');
  const [deadline, setDeadline] = useState(pending ? localDateTime(pending.input.deadline_at) : '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const formId = useId();
  const locked = busy || pending != null;
  const generation = useRef(0);
  useEffect(() => { generation.current += 1; return () => { generation.current += 1; }; }, []);
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    setError(null);
    let attempt = pending;
    try {
      if (!attempt) {
        if (!supplier) throw new Error('Выберите исполнителя из каталога.');
        const input = createDealInput({ supplierId: supplier.id, title, description, amount, deadline });
        attempt = { key: crypto.randomUUID(), input, supplier };
        // Save before sending: a lost response, reload, or navigation must retain the exact command.
        sessionStorage.setItem(pendingStorageKey(organisation.id), JSON.stringify(attempt));
        onPending(attempt);
      }
    } catch (failure) { setError(failure); return; }
    setBusy(true);
    const submittedGeneration = generation.current;
    const isCurrent = () => generation.current === submittedGeneration;
    try {
      const deal = await api.createDeal(attempt.input, attempt.key);
      removePendingDeal(sessionStorage, organisation.id, attempt.key);
      if (isCurrent()) { onPending(null); onCreated(deal); }
    } catch (failure) {
      if (failure instanceof ApiError && ['INVALID_REQUEST', 'INVALID_AMOUNT', 'AMOUNT_OVERFLOW', 'DEADLINE_PASSED', 'RESOURCE_NOT_FOUND', 'IDEMPOTENCY_KEY_REQUIRED'].includes(failure.code)) {
        removePendingDeal(sessionStorage, organisation.id, attempt.key);
        if (isCurrent()) onPending(null);
      }
      if (isCurrent()) { setError(failure); onUnauthorized(failure); }
    } finally { if (isCurrent()) setBusy(false); }
  }
  return <>
    <button className="text-button back-link" type="button" disabled={busy} onClick={onBack}>← К списку сделок</button>
    <div className="section-heading"><div><h2>{pending ? 'Подтверждение создания' : 'Новая сделка'}</h2><p className="muted">Вы выступаете заказчиком от имени {organisation.name}</p></div><span className="tag">Шаг 1 · Условия</span></div>
    <div className="deal-create-layout">
      <form className="panel deal-form" onSubmit={(event) => void submit(event)}>
        {pending && <div className="notice notice-info"><strong>Условия запроса сохранены</strong><span>Ответ сервера ещё нужно подтвердить. Повтор отправит тот же запрос: существующая сделка вернётся без создания второй. До подтверждения условия закреплены.</span></div>}
        <fieldset disabled={locked}>
          <h3>Кто выполнит работу</h3>
          <SupplierPicker ownId={organisation.id} value={supplier} onChange={setSupplier} disabled={locked} onUnauthorized={onUnauthorized} />
          <div className="form-divider" />
          <h3>О чём договорились</h3>
          <label htmlFor={`${formId}-title`}>Название сделки</label><input id={`${formId}-title`} required maxLength={400} value={title} onChange={(event) => setTitle(event.target.value)} placeholder="Например, печать на партии упаковки" aria-describedby={`${formId}-title-help`} /><p className="field-help" id={`${formId}-title-help`}>До 200 символов</p>
          <label htmlFor={`${formId}-description`}>Условия и результат работы</label><textarea id={`${formId}-description`} required rows={6} maxLength={10000} value={description} onChange={(event) => setDescription(event.target.value)} placeholder="Что нужно выполнить, объём работ и какой результат стороны принимают. Для печати — описание согласованного макета." aria-describedby={`${formId}-description-help`} /><p className="field-help" id={`${formId}-description-help`}>До 5 000 символов · {[...description].length.toLocaleString('ru-RU')} / 5 000</p>
          <div className="deal-form-pair"><div><label htmlFor={`${formId}-amount`}>Полная сумма, ₽</label><input id={`${formId}-amount`} inputMode="decimal" autoComplete="off" required maxLength={24} value={amount} onChange={(event) => setAmount(event.target.value)} placeholder="300000,00" /><p className="field-help">Один полный расчёт по сделке</p></div>
            <div><label htmlFor={`${formId}-deadline`}>Срок сделки</label><input id={`${formId}-deadline`} type="datetime-local" required value={deadline} onChange={(event) => setDeadline(event.target.value)} /><p className="field-help">Ваше время: {zone}. На сервер передаётся UTC.</p></div></div>
        </fieldset>
        {error != null && <Failure error={error} />}
        <div className="deal-form-footer"><p>Создание черновика не резервирует и не списывает средства.</p><button className="button button-primary" type="submit" disabled={busy}>{busy ? 'Подтверждаем…' : pending ? 'Проверить результат создания' : 'Создать черновик'}<span aria-hidden="true">↗</span></button></div>
      </form>
      <aside className="panel deal-guide"><div className="section-icon" aria-hidden="true">▤</div><h3>Сначала — ясные условия</h3><p>Исполнитель увидит сделку в своём кабинете. Выбор организации ещё не означает её согласие.</p><ol><li><strong>Условия</strong><span>Стороны, сумма, описание и срок</span></li><li><strong>Документ и согласование</strong><span>PDF и два явных согласия</span></li><li><strong>Резерв и подписи</strong><span>Полная сумма и две тестовые подписи</span></li></ol><p className="field-help">После создания добавьте PDF и получите согласие обеих сторон. Выплата по событию ЭДО будет доступна следующим этапом.</p></aside>
    </div>
  </>;
}

function DealDetail({ id, ownId, created, onBack, onUnauthorized }: ScopeProps & { id: string; ownId: string; created: boolean; onBack: () => void }) {
  const [deal, setDeal] = useState<Deal | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const sequence = useRef(0);
  const load = useCallback(async () => {
    const request = ++sequence.current;
    setLoading(true); setError(null); setDeal(null);
    try { const result = await api.deal(id); if (request === sequence.current) setDeal(result); }
    catch (failure) { if (request === sequence.current) { setError(failure); onUnauthorized(failure); } }
    finally { if (request === sequence.current) setLoading(false); }
  }, [id, onUnauthorized]);
  useEffect(() => { void load(); return () => { sequence.current += 1; }; }, [load]);
  return <>
    <button className="text-button back-link" type="button" onClick={onBack}>← К списку сделок</button>
    {created && <div className="notice notice-success" role="status"><strong>Черновик создан</strong><span>Обе стороны могут открыть условия в своём кабинете. Средства не резервировались.</span></div>}
    {loading ? <div className="panel"><Busy>Открываем сделку</Busy></div> : error != null ? <Failure error={error} retry={() => void load()} /> : deal && <>
      <div className="deal-detail-heading"><div><div className="deal-row-meta"><Status deal={deal} /><span>Версия {deal.deal_version} · Вы — {deal.customer_party_id === ownId ? 'заказчик' : 'исполнитель'}</span></div><h2>{deal.title}</h2><code>{deal.id}</code></div><button className="button button-quiet" type="button" onClick={() => void load()}><span aria-hidden="true">↻</span>Обновить</button></div>
      {deal.status === 'REVIEW_REQUIRED' && <div className="notice notice-review"><strong>Срок сделки наступил</strong><span>Ситуация требует проверки сторонами. Истечение срока не означает подтверждение работ или автоматический расчёт.</span></div>}
      <div className="deal-detail-grid"><section className="panel deal-conditions"><h3>Условия сделки</h3><div className="deal-condition-amount"><span>Полная сумма</span><strong>{formatRubles(deal.amount_kopecks)}</strong></div><dl className="deal-facts"><div><dt>Валюта</dt><dd>Российский рубль · RUB</dd></div><div><dt>Срок сделки</dt><dd>{dateLabel(deal.deadline_at)}<small>{Intl.DateTimeFormat().resolvedOptions().timeZone}</small></dd></div><div><dt>Создана</dt><dd>{dateLabel(deal.created_at)}</dd></div></dl><h3 className="deal-description-heading">Условия и результат работы</h3><p className="deal-description">{deal.description}</p></section>
        <section className="panel deal-parties"><h3>Участники</h3>{([{ label: 'Заказчик', party: deal.customer }, { label: 'Исполнитель', party: deal.supplier }]).map(({ label, party }) => <div className="deal-party" key={label}><span>{label}{party.id === ownId && <span className="mini-tag">Ваша организация</span>}</span><strong>{party.name}</strong><small>{party.kind === 'IP' ? 'Индивидуальный предприниматель' : 'Компания'}</small><code>{party.id}</code></div>)}<p className="field-help">Роль определяется для каждой сделки отдельно.</p></section></div>
      <DealAgreement deal={deal} ownId={ownId} onDeal={setDeal} onUnauthorized={onUnauthorized} />
    </>}
  </>;
}
