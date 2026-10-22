import { useEffect, useId, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { api, ApiError, describeError } from './lib/api';
import type { Counterparty, Deal } from './lib/api';
import { agreementStorageKey, agreeDealInput, definitiveAgreementFailure, pdfIdentity, readPendingAgreement, removePendingAgreement, reworkDealInput, updateDealInput, verifyPendingPdf } from './lib/agreement';
import type { AgreementCommand, PendingAgreement } from './lib/agreement';
import { localDateTime, plainRubles } from './lib/deals';
import SupplierPicker from './SupplierPicker';

interface Props { deal: Deal; ownId: string; onDeal: (deal: Deal) => void; onUnauthorized: (error: unknown) => void }
const commandLabels: Record<AgreementCommand['kind'], string> = {
  update: 'изменение условий', upload: 'загрузка PDF', generate: 'формирование акта', agree: 'согласование документа', rework: 'возврат на доработку',
};

export default function DealAgreement({ deal, ownId, onDeal, onUnauthorized }: Props) {
  const [pending, setPending] = useState<PendingAgreement | null>(null);
  const [storageError, setStorageError] = useState<unknown>(null);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const [refreshRequired, setRefreshRequired] = useState(false);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState(false);
  const [reworking, setReworking] = useState(false);
  const [reason, setReason] = useState('');
  const [consent, setConsent] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [pdfUrl, setPdfUrl] = useState('');
  const [pdfBusy, setPdfBusy] = useState(false);
  const [downloadUrl, setDownloadUrl] = useState('');
  const generation = useRef(0);
  const fileRequest = useRef(0);
  const formId = useId();
  useEffect(() => {
    generation.current += 1;
    try { setPending(readPendingAgreement(sessionStorage.getItem(agreementStorageKey(ownId, deal.id)), deal.id)); }
    catch (failure) { setStorageError(failure); }
    return () => { generation.current += 1; fileRequest.current += 1; };
  }, [ownId, deal.id]);
  useEffect(() => { setConsent(false); setFile(null); setPdfUrl(''); setDownloadUrl(''); fileRequest.current += 1; setPdfBusy(false); }, [deal.document?.id, deal.document?.document_version, deal.deal_version]);
  useEffect(() => () => { if (pdfUrl) URL.revokeObjectURL(pdfUrl); }, [pdfUrl]);
  useEffect(() => () => { if (downloadUrl) URL.revokeObjectURL(downloadUrl); }, [downloadUrl]);

  const customer = deal.customer_party_id === ownId;
  const locked = busy || pending != null || storageError != null || refreshRequired;
  const active = deal.status === 'DRAFT' || deal.status === 'AGREED';
  const expired = new Date(deal.deadline_at).getTime() <= Date.now();
  const document = deal.document;
  const currentAgreements = document ? deal.agreements.filter(item => item.deal_version === deal.deal_version
    && item.document_id === document.id && item.document_version === document.document_version && item.document_sha256 === document.sha256) : [];
  const hasAgreed = currentAgreements.some(item => item.organisation_id === ownId);
  const pendingVersion = pending ? pending.kind === 'update' ? pending.input.expected_version
    : pending.kind === 'agree' || pending.kind === 'rework' ? pending.input.deal_version : pending.input.expected_deal_version : null;

  async function run(command?: AgreementCommand) {
    if (busy || storageError != null) return;
    const submittedGeneration = generation.current;
    const submittedIdentity = api.currentIdentity();
    const isCurrent = () => generation.current === submittedGeneration && api.currentIdentity() === submittedIdentity;
    setBusy(true); setError(null); setNotice('');
    let attempt = pending;
    try {
      if (!attempt) {
        if (!command) throw new Error('Выберите действие.');
        attempt = { ...command, key: crypto.randomUUID(), dealId: deal.id };
        // Verify every persisted condition before transmitting; never overwrite another pending command.
        readPendingAgreement(JSON.stringify(attempt), deal.id);
        const existing = sessionStorage.getItem(agreementStorageKey(ownId, deal.id));
        if (existing !== null) throw new Error('Для этой сделки уже сохранена команда. Откройте карточку заново и проверьте её результат.');
        if (!isCurrent()) return;
        sessionStorage.setItem(agreementStorageKey(ownId, deal.id), JSON.stringify(attempt));
        setPending(attempt);
      }
      if (attempt.kind === 'upload') {
        if (!file) throw new Error('Выберите исходный PDF, чтобы повторить сохранённую загрузку.');
        await verifyPendingPdf(attempt, file);
        if (!isCurrent()) return;
      }
      if (!isCurrent()) return;
      switch (attempt.kind) {
        case 'update': await api.updateDeal(attempt.dealId, attempt.input, attempt.key); break;
        case 'generate': await api.generateDocument(attempt.dealId, attempt.input, attempt.key); break;
        case 'agree': await api.agreeDeal(attempt.dealId, attempt.input, attempt.key); break;
        case 'rework': await api.reworkDeal(attempt.dealId, attempt.input, attempt.key); break;
        case 'upload': await api.uploadDocument(attempt.dealId, attempt.input.expected_deal_version, file!, attempt.input.file_name, attempt.key); break;
      }
      // A cached idempotent reply can describe an earlier version. Always read current server state.
      removePendingAgreement(sessionStorage, ownId, attempt.dealId, attempt.key);
      if (!isCurrent()) return;
      setPending(null); setEditing(false); setReworking(false); setReason(''); setConsent(false);
      setNotice('Команда подтверждена сервером.');
      const actual = await api.deal(attempt.dealId);
      if (isCurrent()) onDeal(actual);
    } catch (failure) {
      if (attempt && definitiveAgreementFailure(failure)) {
        try { removePendingAgreement(sessionStorage, ownId, attempt.dealId, attempt.key); if (isCurrent()) setPending(null); }
        catch (storageFailure) { if (isCurrent()) setStorageError(storageFailure); }
      }
      if (isCurrent()) {
        if (failure instanceof ApiError && ['VERSION_CONFLICT', 'INVALID_STATE', 'DEADLINE_PASSED'].includes(failure.code)) setRefreshRequired(true);
        setError(failure); onUnauthorized(failure);
      }
    } finally { if (isCurrent()) setBusy(false); }
  }

  async function upload() {
    if (!file || locked) return;
    const submittedGeneration = generation.current;
    const submittedIdentity = api.currentIdentity();
    const isCurrent = () => generation.current === submittedGeneration && api.currentIdentity() === submittedIdentity;
    setBusy(true); setError(null);
    try {
      const identity = await pdfIdentity(file);
      if (!isCurrent()) return;
      setBusy(false);
      await run({ kind: 'upload', input: { expected_deal_version: deal.deal_version, ...identity } });
    } catch (failure) { if (isCurrent()) { setBusy(false); setError(failure); } }
  }

  async function loadPdf(download: boolean) {
    if (!document || pdfBusy) return;
    const request = ++fileRequest.current;
    const submittedIdentity = api.currentIdentity();
    const isCurrent = () => request === fileRequest.current && api.currentIdentity() === submittedIdentity;
    setPdfBusy(true); setError(null);
    try {
      const blob = await api.documentFile(document.id, document.document_version, download);
      if (!isCurrent()) return;
      const url = URL.createObjectURL(blob);
      if (download) setDownloadUrl(url); else setPdfUrl(url);
    } catch (failure) { if (isCurrent()) { setError(failure); onUnauthorized(failure); } }
    finally { if (isCurrent()) setPdfBusy(false); }
  }

  function validateAndRun(build: () => AgreementCommand) {
    try { void run(build()); } catch (failure) { setError(failure); }
  }

  return <section className="panel agreement-panel" aria-label="Документ и согласование">
    <div className="agreement-heading"><div><h3>Документ и согласование</h3><p className="muted">Сначала согласуйте одну версию условий и PDF. Средства пока свободны.</p></div>
      {customer && deal.status === 'DRAFT' && <button className="button button-quiet" type="button" disabled={locked || expired} onClick={() => setEditing(value => !value)}>Редактировать условия</button>}
    </div>
    {storageError != null && <div className="notice notice-error" role="alert">{describeError(storageError)}</div>}
    {pending && <div className="notice notice-info" role="status"><strong>Команда ожидает подтверждения: {commandLabels[pending.kind]}</strong><span>Запрос относится к версии условий {pendingVersion}. Условия и ключ запроса сохранены. Повтор проверяет прежнюю команду; новая команда до подтверждения закрыта.</span>
      {pending.kind === 'update' && <span>Сохранённые условия: «{pending.input.title}», сумма {plainRubles(pending.input.amount_kopecks!)} ₽, исполнитель {pending.input.supplier_party_id}.</span>}
      {pending.kind === 'agree' && <><span>Согласование PDF версии {pending.input.document_version}.</span><code className="document-hash">SHA-256: {pending.input.document_sha256}</code></>}
      {pending.kind === 'rework' && <span>Причина: {pending.input.reason}</span>}
      {pending.kind === 'upload' && <><span>Повторно выберите исходный PDF с теми же байтами. Имя отправки останется «{pending.input.file_name}», размер {pending.input.size_bytes.toLocaleString('ru-RU')} байт.</span><code className="document-hash">SHA-256: {pending.input.sha256}</code></>}
      <button className="button button-primary" type="button" disabled={busy || (pending.kind === 'upload' && !file)} onClick={() => void run()}>{busy ? 'Проверяем…' : 'Проверить результат команды'}</button>
    </div>}
    {notice && <div className="notice notice-success" role="status">{notice}</div>}
    {refreshRequired && <div className="notice notice-info" role="status">Используйте «Обновить» вверху карточки перед следующим действием: состояние или версия сделки изменились.</div>}
    {error != null && <div className="notice notice-error" role="alert"><span>{describeError(error)}</span>{error instanceof ApiError && error.requestId && <small>Код обращения: {error.requestId}</small>}
      {error instanceof ApiError && error.code === 'VERSION_CONFLICT' && <span>Используйте «Обновить» вверху карточки. Повторное согласие должно относиться к актуальному документу.</span>}
    </div>}
    {editing && <EditConditions key={deal.deal_version} deal={deal} locked={locked || expired} onCancel={() => setEditing(false)} onSave={(input) => void run({ kind: 'update', input })} onError={setError} onUnauthorized={onUnauthorized} />}
    <div className="agreement-document">
      <h4>PDF для текущих условий</h4>
      {document ? <><dl className="document-facts"><div><dt>Документ</dt><dd>{document.file_name}</dd></div><div><dt>Версии</dt><dd>Условия {document.deal_version} · PDF {document.document_version}</dd></div><div><dt>Размер</dt><dd>{document.size_bytes.toLocaleString('ru-RU')} байт</dd></div><div><dt>SHA-256</dt><dd><code className="document-hash">{document.sha256}</code></dd></div></dl>
        <div className="agreement-actions"><button className="button button-quiet" type="button" disabled={pdfBusy} onClick={() => void loadPdf(false)}>{pdfBusy ? 'Загружаем PDF…' : 'Открыть PDF'}</button><button className="button button-quiet" type="button" disabled={pdfBusy} onClick={() => void loadPdf(true)}>Скачать PDF</button>
          {pdfUrl && <a className="button button-quiet" href={pdfUrl} target="_blank" rel="noopener noreferrer">Открыть загруженный PDF ↗</a>}
          {downloadUrl && <a className="button button-quiet" href={downloadUrl} download={document.file_name}>Сохранить загруженный PDF</a>}
        </div><p className="field-help">Файл загружается после проверки доступа. Ссылка открывает только полученную версию PDF.</p>
      </> : <p className="field-help">К текущей версии условий PDF ещё не добавлен. Заказчик может загрузить документ или сформировать учебный акт.</p>}
      {((customer && active && !expired) || pending?.kind === 'upload') && <div className="document-upload"><label htmlFor={`${formId}-pdf`}>{pending?.kind === 'upload' ? 'Исходный PDF для проверки результата' : 'PDF-документ'}</label><input id={`${formId}-pdf`} type="file" accept="application/pdf,.pdf" disabled={busy || storageError != null || (pending != null && pending.kind !== 'upload')} onChange={(event) => setFile(event.target.files?.[0] ?? null)} />
        <p className="field-help">До 5 МиБ. Замена PDF создаёт новую версию условий и требует двух новых согласий.</p>
        {!pending && <div className="agreement-actions"><button className="button button-primary" type="button" disabled={locked || !file} onClick={() => void upload()}>Загрузить PDF</button><button className="button button-quiet" type="button" disabled={locked} onClick={() => void run({ kind: 'generate', input: { expected_deal_version: deal.deal_version, template_id: 'printing-acceptance-v1' } })}>Сформировать акт</button></div>}
      </div>}
    </div>
    <div className="agreement-consents"><h4>Согласия сторон на текущую версию</h4>
      <ul>{[deal.customer, deal.supplier].map(party => { const agreement = currentAgreements.find(item => item.organisation_id === party.id); return <li key={party.id}><div><strong>{party.name}</strong><span>{party.id === deal.customer_party_id ? 'Заказчик' : 'Исполнитель'}{party.id === ownId ? ' · Ваша организация' : ''}</span></div><span className={`tag${agreement ? ' agreement-confirmed' : ''}`}>{agreement ? 'Согласовано' : 'Ожидает согласия'}</span></li>; })}</ul>
      {active && document && !hasAgreed && !expired && <div className="agreement-confirm"><label className="checkbox-label" htmlFor={`${formId}-consent`}><input id={`${formId}-consent`} type="checkbox" checked={consent} disabled={locked} onChange={(event) => setConsent(event.target.checked)} /><span>Согласен с условиями версии {deal.deal_version} и PDF версии {document.document_version}. Проверил стороны, сумму, срок и содержание документа.</span></label><button className="button button-primary" type="button" disabled={locked || !consent} onClick={() => validateAndRun(() => ({ kind: 'agree', input: agreeDealInput(deal) }))}>Согласовать документ</button></div>}
      {deal.status === 'AGREED' && <div className="notice notice-success"><strong>Обе стороны согласовали документ</strong><span>Это согласование условий, а не электронная подпись или выплата. Резервирование появится на следующем этапе.</span></div>}
      {deal.status === 'AGREED' && !expired && <><button className="text-button" type="button" disabled={locked} onClick={() => setReworking(value => !value)}>Вернуть на доработку</button>
        {reworking && <form className="rework-form" onSubmit={(event) => { event.preventDefault(); validateAndRun(() => ({ kind: 'rework', input: reworkDealInput(deal, reason) })); }}><label htmlFor={`${formId}-reason`}>Причина доработки</label><textarea id={`${formId}-reason`} value={reason} disabled={locked} onChange={(event) => setReason(event.target.value)} maxLength={4000} required rows={3} /><p className="field-help">До 2 000 символов. Активный PDF и согласия будут сняты; история сохранится. Нужны новый документ и два новых согласия.</p><button className="button button-primary" type="submit" disabled={locked}>Подтвердить возврат на доработку</button></form>}
      </>}
      {!active && <p className="field-help">Изменение условий и согласование закрыты. Ситуация требует проверки сторонами.</p>}
      {expired && active && <p className="field-help">Срок наступил. Обновите карточку: автоматическое продолжение закрыто, выполнение работ не подтверждено.</p>}
    </div>
  </section>;
}

function EditConditions({ deal, locked, onSave, onCancel, onError, onUnauthorized }: { deal: Deal; locked: boolean; onSave: (input: ReturnType<typeof updateDealInput>) => void; onCancel: () => void; onError: (error: unknown) => void; onUnauthorized: (error: unknown) => void }) {
  const [supplier, setSupplier] = useState<Counterparty | null>({ ...deal.supplier, is_demo: false });
  const [title, setTitle] = useState(deal.title);
  const [description, setDescription] = useState(deal.description);
  const [amount, setAmount] = useState(plainRubles(deal.amount_kopecks));
  const [deadline, setDeadline] = useState(localDateTime(deal.deadline_at));
  const id = useId();
  function save(event: FormEvent) { event.preventDefault(); try { if (!supplier) throw new Error('Выберите исполнителя из каталога.'); onSave(updateDealInput(deal, { title, description, amount, deadline, supplierId: supplier.id })); } catch (failure) { onError(failure); } }
  return <form className="deal-form agreement-edit" onSubmit={save} aria-label="Редактирование условий"><fieldset disabled={locked}>
    <h4>Новая версия условий</h4><p className="field-help">Изменение снимает активный PDF и согласия. При смене исполнителя прежняя организация потеряет доступ к сделке.</p>
    <SupplierPicker ownId={deal.customer_party_id} value={supplier} onChange={setSupplier} disabled={locked} onUnauthorized={onUnauthorized} />
    <label htmlFor={`${id}-title`}>Название сделки</label><input id={`${id}-title`} value={title} onChange={(event) => setTitle(event.target.value)} required maxLength={400} />
    <label htmlFor={`${id}-description`}>Условия и результат работы</label><textarea id={`${id}-description`} value={description} onChange={(event) => setDescription(event.target.value)} required maxLength={10000} rows={5} />
    <div className="deal-form-pair"><div><label htmlFor={`${id}-amount`}>Полная сумма, ₽</label><input id={`${id}-amount`} value={amount} onChange={(event) => setAmount(event.target.value)} inputMode="decimal" maxLength={24} required /></div><div><label htmlFor={`${id}-deadline`}>Срок сделки</label><input id={`${id}-deadline`} type="datetime-local" value={deadline} onChange={(event) => setDeadline(event.target.value)} required /><p className="field-help">Ваше время: {Intl.DateTimeFormat().resolvedOptions().timeZone}</p></div></div>
  </fieldset><div className="agreement-actions"><button className="button button-primary" type="submit" disabled={locked}>Сохранить новую версию</button><button className="button button-quiet" type="button" disabled={locked} onClick={onCancel}>Отменить редактирование</button></div></form>;
}
