import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { api, ApiError, describeError } from './lib/api';
import type { Counterparty } from './lib/api';
type ScopeProps = { onUnauthorized: (error: unknown) => void };
function Failure({ error, retry }: { error: unknown; retry?: () => void }) {
  return <div className="notice notice-error" role="alert"><span>{describeError(error)}</span>{error instanceof ApiError && error.requestId && <small>Код обращения: {error.requestId}</small>}{retry && <button className="text-button" type="button" onClick={retry}>Повторить запрос</button>}</div>;
}
function Busy({ children }: { children: string }) { return <div className="loading" role="status"><span className="spinner" aria-hidden="true" />{children}</div>; }

export default function SupplierPicker({ ownId, value, onChange, disabled, onUnauthorized }: ScopeProps & {
  ownId: string; value: Counterparty | null; onChange: (value: Counterparty | null) => void; disabled: boolean;
}) {
  const [query, setQuery] = useState('');
  const [search, setSearch] = useState('');
  const [items, setItems] = useState<Counterparty[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const sequence = useRef(0);
  const id = useId();
  const load = useCallback(async (active: string, next?: string) => {
    const request = ++sequence.current;
    setLoading(true); setError(null);
    if (!next) { setItems([]); setCursor(null); }
    try {
      const result = await api.organisations(active, next);
      if (request !== sequence.current) return;
      setItems((previous) => next ? [...previous, ...result.items.filter((item) => item.id !== ownId)] : result.items.filter((item) => item.id !== ownId));
      setCursor(result.next_cursor);
    } catch (failure) { if (request === sequence.current) { setError(failure); onUnauthorized(failure); } }
    finally { if (request === sequence.current) setLoading(false); }
  }, [ownId, onUnauthorized]);
  useEffect(() => { if (!value) void load(search); return () => { sequence.current += 1; }; }, [search, load, value]);
  function searchSuppliers() {
    if (disabled || loading) return;
    const next = query.trim();
    if (next === search) void load(next);
    else setSearch(next);
  }
  if (value) return <div className="selected-supplier"><div className="counterparty-icon" aria-hidden="true">{value.kind === 'IP' ? 'ИП' : 'К'}</div><div><strong>{value.name}</strong><span>{value.kind === 'IP' ? 'Индивидуальный предприниматель' : 'Компания'}</span><code>{value.id}</code></div><button type="button" className="text-button" disabled={disabled} onClick={() => onChange(null)}>Изменить</button></div>;
  return <div className="supplier-picker">
    <label htmlFor={id}>Исполнитель</label><div className="deal-search-row"><input id={id} value={query} onChange={(event) => setQuery(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && !event.nativeEvent.isComposing) { event.preventDefault(); searchSuppliers(); } }} maxLength={400} placeholder="Найти по названию" /><button className="button button-quiet" type="button" disabled={disabled || loading} onClick={searchSuppliers}>Найти</button></div>
    {error != null && <Failure error={error} retry={() => void load(search, items.length ? cursor ?? undefined : undefined)} />}
    <div className="supplier-results" aria-label="Выбор исполнителя">{items.map((item) => <button key={item.id} type="button" className="supplier-option" disabled={disabled} onClick={() => onChange(item)}><span><strong>{item.name}</strong><small>{item.kind === 'IP' ? 'ИП' : 'Компания'} · <code>{item.id}</code></small></span><span aria-hidden="true">＋</span></button>)}
      {loading && <Busy>Загружаем организации</Busy>}{!loading && !items.length && error == null && <p className="field-help supplier-empty">Другие организации не найдены. Измените название или зарегистрируйте отдельного исполнителя.</p>}
    </div>
    {cursor && !loading && <button className="text-button supplier-more" type="button" disabled={disabled} onClick={() => void load(search, cursor)}>Показать ещё организации</button>}
    <p className="field-help">Своя организация исключена. Сверяйте ID: названия могут совпадать.</p>
  </div>;
}
