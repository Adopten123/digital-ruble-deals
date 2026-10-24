import { useId, useState } from 'react';
import type { Deal } from './lib/api';
import { currentSignatures, signDocumentCommand } from './lib/agreement';
import { terminalDeal } from './lib/blockers';

export default function DealSignatures({ deal, ownId, locked, onSign }: {
  deal: Deal; ownId: string; locked: boolean; onSign: () => void;
}) {
  const [confirmed, setConfirmed] = useState(false);
  const id = useId();
  const signatures = currentSignatures(deal);
  const ownSignature = signatures.some(item => item.organisation_id === ownId);
  const expired = new Date(deal.deadline_at).getTime() <= Date.now();
  let eligible = false;
  try { signDocumentCommand(deal, ownId); eligible = !expired; } catch { /* Display only actions for the shown context; the server checks it again. */ }
  const document = deal.document;
  return <section className="deal-signatures" aria-label="Тестовые подписи"><h4>Тестовые подписи сторон</h4>
    <p className="field-help">Согласие с условиями и подпись — отдельные действия. Здесь каждая сторона подписывает документ от своей организации после резерва. Это учебная имитация, не квалифицированная электронная подпись.</p>
    <ul className="signature-list">{[deal.customer, deal.supplier].map(party => {
      const signature = signatures.find(item => item.organisation_id === party.id);
      return <li key={party.id}><div><strong>{party.name}</strong><span>{party.id === deal.customer_party_id ? 'Заказчик' : 'Исполнитель'}{party.id === ownId ? ' · Ваша организация' : ''}</span>
        {signature && <small>Подписано {new Date(signature.created_at).toLocaleString('ru-RU')} · Условия {signature.deal_version}, PDF {signature.document_version}</small>}
      </div><span className={`tag${signature ? ' agreement-confirmed' : ''}`}>{signature ? 'Подписано' : 'Ожидает подписи'}</span></li>;
    })}</ul>
    {eligible && document && <div className="signature-confirm"><label className="checkbox-label" htmlFor={`${id}-sign`}><input id={`${id}-sign`} type="checkbox" checked={confirmed} disabled={locked} onChange={event => setConfirmed(event.target.checked)} /><span>Подписываю PDF версии {document.document_version} для условий версии {deal.deal_version} от имени своей организации. Проверил документ и результат работ.</span></label>
      <p className="field-help">Подписываемый файл: {document.file_name}</p><code className="document-hash signature-hash">SHA-256: {document.sha256}</code>
      <button className="button button-primary" type="button" disabled={locked || !confirmed} onClick={onSign}>Подписать документ в тестовом контуре</button>
    </div>}
    {ownSignature && deal.status === 'FUNDED' && <div className="notice notice-info" role="status"><strong>Ваша тестовая подпись сохранена</strong><span>Ожидается подпись второй стороны. Средства остаются в резерве, выплата не выполнена.</span></div>}
    {new Set(signatures.map(item => item.organisation_id)).size === 2 && <div className="notice notice-success" role="status"><strong>Документ подписан обеими сторонами</strong><span>{deal.status === 'READY_TO_SETTLE' ? 'Событие ЭДО обрабатывается. Результат расчёта обновится в этой карточке.' : deal.status === 'SETTLED' ? 'Тестовый расчёт завершён; подписи сохранены.' : 'Подписи сохранены. Возможность расчёта зависит от состояния сделки и блокировок.'}</span></div>}
    {!deal.reserve && !terminalDeal(deal) && <p className="field-help">Подписание откроется после согласования документа и резерва полной суммы заказчиком.</p>}
    {expired && deal.reserve && !terminalDeal(deal) && <p className="field-help">Срок сделки наступил. Новая подпись закрыта; обновите карточку и проверьте ситуацию со второй стороной.</p>}
  </section>;
}
