import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError, describeError } from './lib/api';
import type { Deal, OwnOrganisation } from './lib/api';
import { reserveDealInput } from './lib/agreement';
import { reserveBalanceSnapshot } from './lib/demoMoney';
import { formatRubles } from './lib/money';

export default function DealReserve({ deal, ownId, locked, revision, onReserve, onUnauthorized }: {
  deal: Deal; ownId: string; locked: boolean; revision: number; onReserve: () => void; onUnauthorized: (error: unknown) => void;
}) {
  const [account, setAccount] = useState<OwnOrganisation['account'] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const request = useRef(0);
  const customer = ownId === deal.customer_party_id;
  const relevant = customer && (deal.status === 'AGREED' || deal.reserve != null);
  const load = useCallback(async () => {
    const sequence = ++request.current;
    setLoading(true); setAccount(null); setError(null);
    try {
      const actual = await api.ownOrganisation();
      if (actual.organisation.id !== ownId || actual.account.currency !== 'RUB') throw new Error('Счёт не соответствует текущей организации. Обновите кабинет.');
      reserveBalanceSnapshot(actual.account, deal.amount_kopecks);
      if (sequence === request.current) setAccount(actual.account);
    } catch (failure) { if (sequence === request.current) { setError(failure); onUnauthorized(failure); } }
    finally { if (sequence === request.current) setLoading(false); }
  }, [ownId, deal.amount_kopecks, onUnauthorized]);
  useEffect(() => { if (relevant) void load(); return () => { request.current += 1; }; }, [relevant, deal.reserve?.id, deal.reserve?.status, revision, load]);
  const snapshot = account ? reserveBalanceSnapshot(account, deal.amount_kopecks) : null;
  let eligible = false;
  try { reserveDealInput(deal); eligible = customer && new Date(deal.deadline_at).getTime() > Date.now(); } catch { /* Server remains authoritative; no button for an incomplete context. */ }
  const reserve = deal.reserve;
  const validReserve = reserve && reserve.deal_id === deal.id && reserve.deal_version === deal.deal_version
    && reserve.amount_kopecks === deal.amount_kopecks && reserve.currency === 'RUB';
  return <section className="deal-reserve" aria-label="Резерв сделки"><h4>Резерв тестовых средств</h4>
    <p className="field-help">Тестовый контур. Активный резерв закрепляет полную сумму за сделкой. Выплата погашает его, согласованная отмена освобождает.</p>
    <div className="reserve-total"><span>Полная сумма сделки</span><strong>{formatRubles(deal.amount_kopecks)}</strong></div>
    {reserve && (validReserve ? <div className="notice notice-success" role="status"><strong>{reserve.status === 'ACTIVE' ? 'Сумма зарезервирована' : reserve.status === 'CONSUMED' ? 'Резерв использован для выплаты' : 'Резерв больше не удерживается'}</strong><span>{formatRubles(reserve.amount_kopecks)} · версия условий {reserve.deal_version}. {reserve.status === 'ACTIVE' ? 'Средства не выплачены исполнителю.' : reserve.status === 'CONSUMED' ? 'Тестовая выплата исполнителю подтверждена.' : 'Сумма снова доступна заказчику, выплаты исполнителю не было.'}</span><code>{reserve.id}</code></div>
      : <div className="notice notice-error" role="alert">Резерв в ответе не соответствует условиям сделки. Обновите карточку.</div>)}
    {relevant && <><div className="reserve-balance-heading"><h5>Счёт заказчика · данные сервера</h5><button className="text-button" type="button" disabled={loading} onClick={() => void load()}>Обновить остатки</button></div>
      {loading && <p role="status" className="field-help">Получаем актуальные остатки…</p>}
      {error != null && <div className="notice notice-error" role="alert">{describeError(error)}{error instanceof ApiError && error.requestId && <small>Код обращения: {error.requestId}</small>}</div>}
      {account && <dl className="reserve-balances"><div><dt>Общий остаток</dt><dd>{formatRubles(account.total_kopecks)}</dd></div><div><dt>Зарезервировано</dt><dd>{formatRubles(account.reserved_kopecks)}</dd></div><div><dt>Доступно</dt><dd>{formatRubles(account.available_kopecks)}</dd></div></dl>}
      {eligible && snapshot && <p className="field-help">{snapshot.canCover ? `После резерва по этому снимку останется доступно ${formatRubles(snapshot.availableAfterKopecks!)}.` : 'По текущему снимку доступной суммы недостаточно. Остатки могли измениться; окончательный результат определяет сервер.'}</p>}
    </>}
    {eligible && <><button className="button button-primary reserve-submit" type="button" disabled={locked} onClick={onReserve}>Зарезервировать всю сумму</button><p className="field-help">Подтверждаются актуальные условия, PDF и два согласия. Сервер повторно проверит доступные средства в момент операции.</p></>}
    {!reserve && !eligible && deal.status !== 'CANCELLED' && <p className="field-help">{deal.status === 'AGREED' && !customer ? 'Резерв создаёт заказчик. После подтверждения он станет виден обеим сторонам.' : 'Для резерва нужны актуальный PDF и согласие обеих сторон до срока сделки.'}</p>}
  </section>;
}
