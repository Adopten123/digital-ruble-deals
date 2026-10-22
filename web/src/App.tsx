import { useCallback, useEffect, useId, useRef, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { api, ApiError, describeError } from './lib/api';
import type { Counterparty, OrganisationKind, OwnOrganisation, Session } from './lib/api';
import { formatRubles, validateBalances } from './lib/money';
import Deals from './Deals';

type Readiness = 'checking' | 'ready' | 'unavailable';

function Mark({ small = false }: { small?: boolean }) {
  return <a className={`brand${small ? ' brand-small' : ''}`} href="/" aria-label="ПоАкту — главная">
    <span className="brand-symbol" aria-hidden="true">п<span>а</span></span>
    <span>ПоАкту<span className="brand-caption">условные расчёты для бизнеса</span></span>
  </a>;
}

function ErrorNotice({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  return <div className="notice notice-error" role="alert">
    <span>{describeError(error)}</span>
    {error instanceof ApiError && error.requestId && <small>Код обращения: {error.requestId}</small>}
    {onRetry && <button className="text-button" type="button" onClick={onRetry}>Повторить запрос</button>}
  </div>;
}

function Loading({ children }: { children: ReactNode }) {
  return <div className="loading" role="status"><span className="spinner" aria-hidden="true" />{children}</div>;
}

export default function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [initializing, setInitializing] = useState(true);
  const [bootstrapError, setBootstrapError] = useState<unknown>(null);
  const [notice, setNotice] = useState<unknown>(null);
  const [readiness, setReadiness] = useState<Readiness>('checking');
  const [loggingOut, setLoggingOut] = useState(false);
  const sessionRevision = useRef(0);

  const checkReadiness = useCallback(async () => {
    setReadiness('checking');
    try {
      const response = await fetch('/readyz', { credentials: 'same-origin', cache: 'no-store' });
      setReadiness(response.ok ? 'ready' : 'unavailable');
    } catch { setReadiness('unavailable'); }
  }, []);

  const loadSession = useCallback(async () => {
    const revision = ++sessionRevision.current;
    api.clearCsrf();
    setInitializing(true);
    setBootstrapError(null);
    try {
      const next = await api.me();
      if (revision !== sessionRevision.current) return;
      api.bindIdentity(next.user.id);
      setSession(next);
    }
    catch (error) {
      if (revision !== sessionRevision.current) return;
      if (error instanceof ApiError && error.status === 401) { api.bindIdentity(null); setSession(null); }
      else setBootstrapError(error);
    } finally { if (revision === sessionRevision.current) setInitializing(false); }
  }, []);

  useEffect(() => { void loadSession(); void checkReadiness(); }, [loadSession, checkReadiness]);

  const expireSession = useCallback((error: unknown) => {
    if (error instanceof ApiError && error.status === 401) {
      sessionRevision.current += 1;
      api.clearCsrf();
      api.bindIdentity(null);
      setSession(null);
      setInitializing(false);
      setNotice(error);
    } else if (error instanceof ApiError && ['SESSION_CHANGED', 'CSRF_INVALID'].includes(error.code)) {
      setNotice(error);
      void loadSession();
    }
  }, [loadSession]);

  async function authenticated(nextSession: Session) {
    const revision = ++sessionRevision.current;
    api.clearCsrf();
    api.bindIdentity(nextSession.user.id);
    setNotice(null);
    setBootstrapError(null);
    setInitializing(false);
    setSession(nextSession);
    try { await api.csrf(); }
    catch (error) { if (revision === sessionRevision.current) setNotice(error); }
  }

  async function logout() {
    const revision = ++sessionRevision.current;
    setLoggingOut(true);
    setNotice(null);
    try {
      await api.logout();
      if (revision !== sessionRevision.current) return;
      api.clearCsrf();
      api.bindIdentity(null);
      setSession(null);
    } catch (error) {
      if (revision !== sessionRevision.current) return;
      expireSession(error);
      setNotice(error);
    } finally { setLoggingOut(false); }
  }

  return <>
    <div className="test-banner"><span className="banner-dot" aria-hidden="true" />Тестовый контур. Расчёты и ЭДО эмулируются</div>
    <header className="header">
      <Mark />
      <div className="header-right">
        <button className={`readiness readiness-${readiness}`} type="button" onClick={() => void checkReadiness()}
          aria-label="Обновить проверку готовности стенда" title="Нажмите, чтобы проверить готовность сервиса">
          <span aria-hidden="true" />{readiness === 'ready' ? 'Сервис готов' : readiness === 'checking' ? 'Проверяем сервис' : 'Сервис недоступен'}
        </button>
        {session && <button className="button button-quiet logout" onClick={() => void logout()} disabled={loggingOut}>
          {loggingOut ? 'Выходим…' : 'Выйти'}
        </button>}
      </div>
    </header>
    <main>
      {notice != null && <div className="global-notice"><ErrorNotice error={notice} /></div>}
      {initializing ? <section className="bootstrap"><Loading>Проверяем сессию</Loading></section>
        : bootstrapError != null ? <section className="bootstrap panel">
          <div className="eyebrow">Подключение к стенду</div>
          <h1>Не удалось открыть кабинет</h1>
          <p>Для входа нужны фактические данные сервера.</p>
          <ErrorNotice error={bootstrapError} onRetry={() => { void loadSession(); void checkReadiness(); }} />
        </section>
          : session ? <Workspace key={session.user.id} session={session} onUnauthorized={expireSession} />
            : <AuthScreen onAuthenticated={authenticated} />}
    </main>
    <footer className="footer"><span>ПоАкту · Прототип для МСБ</span><span>Только учебные средства и тестовые подписи</span></footer>
  </>;
}

function AuthScreen({ onAuthenticated }: { onAuthenticated: (session: Session) => Promise<void> }) {
  const [mode, setMode] = useState<'login' | 'register'>('login');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [organisationName, setOrganisationName] = useState('');
  const [kind, setKind] = useState<OrganisationKind>('COMPANY');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  function switchMode(next: 'login' | 'register') {
    setMode(next);
    setPassword('');
    setError(null);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const passwordLength = [...password].length;
      if (passwordLength > 128 || (mode === 'register' && passwordLength < 12)) {
        throw new Error('Пароль должен содержать от 12 до 128 символов.');
      }
      const nameLength = [...organisationName.trim()].length;
      if (mode === 'register' && (nameLength < 2 || nameLength > 200)) {
        throw new Error('Название организации должно содержать от 2 до 200 символов.');
      }
      const session = mode === 'login'
        ? await api.login(email.trim(), password)
        : await api.register({ email: email.trim(), password, organisation: { name: organisationName.trim(), kind } });
      setPassword('');
      await onAuthenticated(session);
    } catch (failure) { setError(failure); }
    finally { setBusy(false); }
  }

  return <div className="auth-layout">
    <section className="intro">
      <div className="eyebrow"><span /> Бизнес договаривается. Условия исполняются.</div>
      <h1>Оплата по факту.<br /><span>По акту.</span></h1>
      <p className="intro-copy">Условный расчёт между компаниями: согласованные условия, видимый резерв и оплата после двух подписей.</p>
      <div className="process" aria-label="Принцип условного расчёта">
        <div><span className="step-number">01</span><strong>Согласовать</strong><p>Стороны, сумма и точная версия документа</p></div>
        <span className="process-line" aria-hidden="true" />
        <div><span className="step-number">02</span><strong>Зарезервировать</strong><p>Полная сумма для одной сделки</p></div>
        <span className="process-line" aria-hidden="true" />
        <div><span className="step-number">03</span><strong>Подписать</strong><p>Две подписи и проверка условий расчёта</p></div>
      </div>
      <p className="stage-note"><span aria-hidden="true">i</span>Создавайте сделки, добавляйте PDF и согласуйте актуальную версию с контрагентом. Резервы, подписи и расчёты появятся в следующих обновлениях.</p>
    </section>
    <section className="auth-card panel" aria-label="Доступ в приложение">
      <div className="auth-tabs" aria-label="Выбор формы">
        <button type="button" aria-pressed={mode === 'login'} className={mode === 'login' ? 'active' : ''} onClick={() => switchMode('login')} disabled={busy}>Вход</button>
        <button type="button" aria-pressed={mode === 'register'} className={mode === 'register' ? 'active' : ''} onClick={() => switchMode('register')} disabled={busy}>Регистрация</button>
      </div>
      <h2>{mode === 'login' ? 'Рады видеть вас' : 'Новая организация'}</h2>
      <p className="muted auth-description">{mode === 'login' ? 'Войдите в учебный кабинет вашей компании.' : 'Создайте отдельную учебную организацию с нулевым счётом.'}</p>
      <form onSubmit={(event) => void submit(event)}>
        <fieldset disabled={busy}>
          <label htmlFor={`${formId}-email`}>Email</label>
          <input id={`${formId}-email`} type="email" autoComplete="username" maxLength={254} required placeholder="name@company.ru" value={email} onChange={(event) => setEmail(event.target.value)} />
          <label htmlFor={`${formId}-password`}>Пароль</label>
          <input id={`${formId}-password`} type="password" autoComplete={mode === 'login' ? 'current-password' : 'new-password'} maxLength={256} required placeholder={mode === 'register' ? 'От 12 до 128 символов' : 'Введите пароль'} value={password} onChange={(event) => setPassword(event.target.value)} />
          {mode === 'register' && <>
            <label htmlFor={`${formId}-name`}>Название организации</label>
            <input id={`${formId}-name`} autoComplete="organization" maxLength={400} required placeholder="ООО «Название»" value={organisationName} onChange={(event) => setOrganisationName(event.target.value)} />
            <label htmlFor={`${formId}-kind`}>Форма организации</label>
            <select id={`${formId}-kind`} value={kind} onChange={(event) => setKind(event.target.value as OrganisationKind)}>
              <option value="COMPANY">Компания</option><option value="IP">Индивидуальный предприниматель</option>
            </select>
            <p className="field-help">Регистрация не подтверждает реальные полномочия и не даёт доступа к существующей организации.</p>
          </>}
          {error != null && <ErrorNotice error={error} />}
          <button className="button button-primary full-width" type="submit">{busy ? 'Подождите…' : mode === 'login' ? 'Войти в кабинет' : 'Создать организацию'}<span aria-hidden="true">↗</span></button>
        </fieldset>
      </form>
      {mode === 'login' && <details className="demo-access">
        <summary>Демоаккаунты для проверки</summary>
        <p>Общий учебный пароль: <code>PoaktuDemo2026!</code></p>
        <div className="demo-options">{[
          ['Заказчик', 'customer@demo.poaktu.local'],
          ['Исполнитель', 'supplier@demo.poaktu.local'],
          ['Оператор', 'operator@demo.poaktu.local'],
        ].map(([title, address]) => <button key={address} type="button" disabled={busy} onClick={() => { setEmail(address); setPassword('PoaktuDemo2026!'); setError(null); }}>
          <strong>{title}</strong><span>{address}</span>
        </button>)}</div>
      </details>}
      <div className="auth-safety">Учебный прототип. Реальные деньги не используются.</div>
    </section>
  </div>;
}

function Workspace({ session, onUnauthorized }: { session: Session; onUnauthorized: (error: unknown) => void }) {
  const [tab, setTab] = useState<'account' | 'deals' | 'catalog'>('account');
  return <div className="workspace">
    <div className="workspace-heading">
      <div><div className="eyebrow">{session.organisation ? 'Кабинет организации' : 'Учебный оператор'}</div>
        <h1>{session.organisation?.name ?? 'Панель оператора'}</h1>
        <p className="muted">{session.user.email}{session.organisation && <span className="inline-badge">{session.organisation.kind === 'IP' ? 'ИП' : 'Компания'}</span>}</p>
      </div>
      <span className="environment-badge"><span aria-hidden="true" />Учебная среда</span>
    </div>
    <nav className="workspace-tabs" aria-label="Разделы кабинета">
      <button className={tab === 'account' ? 'active' : ''} aria-current={tab === 'account' ? 'page' : undefined} onClick={() => setTab('account')}>{session.organisation ? 'Мой счёт' : 'Обзор'}</button>
      {session.organisation && <button className={tab === 'deals' ? 'active' : ''} aria-current={tab === 'deals' ? 'page' : undefined} onClick={() => setTab('deals')}>Сделки</button>}
      <button className={tab === 'catalog' ? 'active' : ''} aria-current={tab === 'catalog' ? 'page' : undefined} onClick={() => setTab('catalog')}>Контрагенты</button>
    </nav>
    {tab === 'deals' && session.organisation ? <Deals key={session.user.id} organisation={session.organisation} onUnauthorized={onUnauthorized} />
      : tab === 'catalog' ? <Catalog key={session.user.id} ownId={session.organisation?.id} onUnauthorized={onUnauthorized} />
      : session.organisation ? <Account key={session.user.id} session={session} onUnauthorized={onUnauthorized} />
        : <section className="panel operator-scope"><div className="section-icon" aria-hidden="true">↳</div><h2>Отдельный доступ оператора</h2>
          <p>У этого аккаунта нет собственной организации и счёта. Оператор не подписывает документы за стороны и не согласует их условия.</p>
          <p className="muted">Управление учебными сценариями будет добавлено вместе с денежным ядром и имитатором ЭДО. Сейчас доступен каталог организаций.</p>
          <button type="button" className="button button-primary" onClick={() => setTab('catalog')}>Открыть каталог<span aria-hidden="true">↗</span></button>
        </section>}
  </div>;
}

function Account({ session, onUnauthorized }: { session: Session; onUnauthorized: (error: unknown) => void }) {
  const [data, setData] = useState<OwnOrganisation | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [loadedAt, setLoadedAt] = useState<Date | null>(null);
  const requestSequence = useRef(0);

  const load = useCallback(async () => {
    const request = ++requestSequence.current;
    setLoading(true);
    setError(null);
    setData(null);
    try {
      const result = await api.ownOrganisation();
      if (result.account.currency !== 'RUB') throw new Error('Сервер вернул неподдерживаемую валюту счёта.');
      validateBalances(result.account.total_kopecks, result.account.reserved_kopecks, result.account.available_kopecks);
      if (request !== requestSequence.current) return;
      setData(result);
      setLoadedAt(new Date());
    } catch (failure) { if (request === requestSequence.current) { setError(failure); onUnauthorized(failure); } }
    finally { if (request === requestSequence.current) setLoading(false); }
  }, [onUnauthorized]);

  useEffect(() => { void load(); return () => { requestSequence.current += 1; }; }, [load]);

  return <>
    <section className="account-section" aria-labelledby="account-title">
      <div className="section-heading"><div><h2 id="account-title">Учебный счёт</h2><p className="muted">Три остатка — полная картина средств</p></div>
        <button className="button button-quiet" onClick={() => void load()} disabled={loading}><span aria-hidden="true">↻</span>{loading ? 'Загрузка…' : 'Обновить'}</button>
      </div>
      {loading ? <div className="panel"><Loading>Получаем остатки с сервера</Loading></div>
        : error != null ? <ErrorNotice error={error} onRetry={() => void load()} />
          : data && <>
            <div className="balance-grid">
              <BalanceCard type="total" label="Общий остаток" value={data.account.total_kopecks} help="Все тестовые средства на счёте" />
              <BalanceCard type="reserved" label="Зарезервировано" value={data.account.reserved_kopecks} help="Средства, закреплённые за сделками" />
              <BalanceCard type="available" label="Доступно" value={data.account.available_kopecks} help="Общий остаток минус активные резервы" />
            </div>
            <div className="account-meta"><span>Счёт <code>{data.account.id}</code></span><span>Данные сервера на {loadedAt?.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</span></div>
          </>}
    </section>
    <div className="overview-grid">
      <section className="panel next-stage"><div className="section-icon" aria-hidden="true">↗</div><span className="tag">Следующий этап</span><h2>От условий к расчёту</h2>
        <p>В разделе «Сделки» доступны условия, PDF и согласование обеими сторонами. Следующий этап — резервирование, две тестовые подписи и расчёт по проверенному событию.</p>
        <div className="future-flow"><span>Согласование</span><span aria-hidden="true">→</span><span>Резерв</span><span aria-hidden="true">→</span><span>Две подписи</span><span aria-hidden="true">→</span><span>Расчёт</span></div>
      </section>
      <section className="panel organisation-info"><h2>Ваша организация</h2><dl><div><dt>Название</dt><dd>{session.organisation?.name}</dd></div><div><dt>Идентификатор</dt><dd><code>{session.organisation?.id}</code></dd></div><div><dt>Доступ</dt><dd>Владелец учебной организации</dd></div></dl><p className="field-help">Права проверяются сервером для каждого запроса.</p></section>
    </div>
  </>;
}

function BalanceCard({ type, label, value, help }: { type: string; label: string; value: string; help: string }) {
  return <article className={`balance-card balance-${type}`}><div className="balance-label">{label}<span aria-hidden="true">{type === 'total' ? '≡' : type === 'reserved' ? '◇' : '↗'}</span></div><strong>{formatRubles(value)}</strong><p>{help}</p></article>;
}

function Catalog({ ownId, onUnauthorized }: { ownId?: string; onUnauthorized: (error: unknown) => void }) {
  const [query, setQuery] = useState('');
  const [activeQuery, setActiveQuery] = useState('');
  const [items, setItems] = useState<Counterparty[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const requestSequence = useRef(0);

  const load = useCallback(async (search: string, nextCursor?: string) => {
    const request = ++requestSequence.current;
    setLoading(true);
    setError(null);
    if (!nextCursor) { setItems([]); setCursor(null); }
    try {
      const result = await api.organisations(search, nextCursor);
      if (request !== requestSequence.current) return;
      setItems((previous) => nextCursor ? [...previous, ...result.items] : result.items);
      setCursor(result.next_cursor);
    } catch (failure) {
      if (request !== requestSequence.current) return;
      setError(failure);
      onUnauthorized(failure);
    } finally { if (request === requestSequence.current) setLoading(false); }
  }, [onUnauthorized]);

  useEffect(() => {
    void load(activeQuery);
    return () => { requestSequence.current += 1; };
  }, [activeQuery, load]);

  function search(event: FormEvent) {
    event.preventDefault();
    const normalized = query.trim();
    if (normalized === activeQuery) void load(normalized);
    else setActiveQuery(normalized);
  }

  return <section className="catalog" aria-labelledby="catalog-title">
    <div className="section-heading"><div><h2 id="catalog-title">Каталог контрагентов</h2><p className="muted">Найдите организацию по названию</p></div><span className="tag">Компании и ИП</span></div>
    <form className="search-form" onSubmit={search} role="search"><label className="visually-hidden" htmlFor="counterparty-search">Название организации</label><div className="search-field"><span aria-hidden="true">⌕</span><input id="counterparty-search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Название организации" maxLength={400} /></div><button className="button button-primary" type="submit">Найти</button></form>
    <p className="catalog-privacy">В каталоге доступны название, форма и ID. Остатки, документы и данные участников других организаций закрыты.</p>
    {error != null && <ErrorNotice error={error} onRetry={() => void load(activeQuery, items.length ? cursor ?? undefined : undefined)} />}
    <div className="panel catalog-list">
      {items.length > 0 && <div className="catalog-list-heading"><span>Организация</span><span>Идентификатор</span></div>}
      {items.map((item) => <article className="counterparty" key={item.id}><div className="counterparty-icon" aria-hidden="true">{item.kind === 'IP' ? 'ИП' : 'К'}</div><div className="counterparty-name"><h3>{item.name}</h3><div><span>{item.kind === 'IP' ? 'Индивидуальный предприниматель' : 'Компания'}</span>{item.is_demo && <span className="mini-tag">Демо</span>}{item.id === ownId && <span className="mini-tag">Ваша</span>}</div></div><code>{item.id}</code></article>)}
      {loading && <Loading>{items.length ? 'Загружаем следующие организации' : 'Загружаем каталог'}</Loading>}
      {!loading && error == null && items.length === 0 && <div className="empty-state"><span aria-hidden="true">⌕</span><h3>Организации не найдены</h3><p>{activeQuery ? 'Попробуйте другое название или очистите поиск.' : 'В каталоге пока нет организаций.'}</p></div>}
      {!loading && error != null && items.length === 0 && <p className="catalog-unavailable">Каталог не загружен. Повторите запрос после восстановления связи.</p>}
    </div>
    {cursor && !loading && error == null && <button type="button" className="button button-quiet load-more" onClick={() => void load(activeQuery, cursor)}>Показать ещё</button>}
    <p className="field-help">Названия могут совпадать. Идентификатор однозначно определяет организацию.</p>
  </section>;
}
