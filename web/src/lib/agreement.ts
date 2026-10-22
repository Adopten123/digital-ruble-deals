import { ApiError } from './api.ts';
import type { AgreeDealInput, Deal, GenerateDocumentInput, ReworkDealInput, UpdateDealInput } from './api.ts';
import { createDealInput, localDateTime, readPendingDeal } from './deals.ts';

export const MAX_PDF_BYTES = 5 * 1024 * 1024;
export type AgreementCommand =
  | { kind: 'update'; input: UpdateDealInput }
  | { kind: 'generate'; input: GenerateDocumentInput }
  | { kind: 'agree'; input: AgreeDealInput }
  | { kind: 'rework'; input: ReworkDealInput }
  | { kind: 'upload'; input: { expected_deal_version: number; file_name: string; size_bytes: number; sha256: string } };
export type PendingAgreement = AgreementCommand & { key: string; dealId: string };

export function agreementStorageKey(organisationId: string, dealId: string): string {
  return `poaktu:pending-agreement:${organisationId}:${dealId}`;
}

function version(value: unknown): value is number { return Number.isSafeInteger(value) && Number(value) > 0; }
function exactKeys(value: object, keys: string[]): boolean { return Object.keys(value).length === keys.length && Object.keys(value).every(key => keys.includes(key)); }
function nonempty(value: unknown, limit: number): value is string { return typeof value === 'string' && !!value.trim() && [...value].length <= limit; }

/** Keep expired but intact commands: they may have completed before the reply was lost. */
export function readPendingAgreement(serialized: string | null, dealId: string): PendingAgreement | null {
  if (serialized === null) return null;
  try {
    const attempt: PendingAgreement = JSON.parse(serialized);
    if (!attempt || !exactKeys(attempt, ['key', 'dealId', 'kind', 'input']) || attempt.dealId !== dealId
      || typeof attempt.key !== 'string' || !/^[\x21-\x7e]{16,128}$/.test(attempt.key)
      || !attempt.input || typeof attempt.input !== 'object' || Array.isArray(attempt.input)) throw new Error();
    const input = attempt.input;
    switch (attempt.kind) {
      case 'update': {
        const { expected_version, ...conditions } = attempt.input;
        if (!version(expected_version) || !exactKeys(conditions, ['supplier_party_id', 'title', 'description', 'amount_kopecks', 'currency', 'deadline_at'])) throw new Error();
        // Reuse the strict saved-conditions parser without imposing a new future deadline.
        readPendingDeal(JSON.stringify({ key: attempt.key, input: conditions, supplier: { id: conditions.supplier_party_id, name: 'Исполнитель', kind: 'COMPANY' } }));
        break;
      }
      case 'generate':
        if (!exactKeys(input, ['expected_deal_version', 'template_id']) || !version(attempt.input.expected_deal_version) || attempt.input.template_id !== 'printing-acceptance-v1') throw new Error();
        break;
      case 'agree':
        if (!exactKeys(input, ['deal_version', 'document_id', 'document_version', 'document_sha256'])
          || !version(attempt.input.deal_version) || !version(attempt.input.document_version)
          || !nonempty(attempt.input.document_id, 128) || !/^[a-f0-9]{64}$/.test(attempt.input.document_sha256)) throw new Error();
        break;
      case 'rework':
        if (!exactKeys(input, ['deal_version', 'reason']) || !version(attempt.input.deal_version) || !nonempty(attempt.input.reason, 2000)) throw new Error();
        break;
      case 'upload':
        if (!exactKeys(input, ['expected_deal_version', 'file_name', 'size_bytes', 'sha256'])
          || !version(attempt.input.expected_deal_version) || !nonempty(attempt.input.file_name, 255)
          || !Number.isSafeInteger(attempt.input.size_bytes) || attempt.input.size_bytes < 1 || attempt.input.size_bytes > MAX_PDF_BYTES
          || !/^[a-f0-9]{64}$/.test(attempt.input.sha256)) throw new Error();
        break;
      default: throw new Error();
    }
    if (new TextEncoder().encode(JSON.stringify(input)).length > 16384) throw new Error();
    return attempt;
  } catch { throw new Error('Сохранённую команду нельзя прочитать. Не отправляйте новую: проверьте актуальную сделку и сохранённый запрос.'); }
}

export function removePendingAgreement(storage: Pick<Storage, 'getItem' | 'removeItem'>, organisationId: string, dealId: string, key: string): boolean {
  const storageKey = agreementStorageKey(organisationId, dealId);
  const current = storage.getItem(storageKey);
  if (current === null) return false;
  let saved: unknown;
  try { saved = JSON.parse(current); } catch { return false; }
  if (!saved || typeof saved !== 'object' || !('key' in saved) || saved.key !== key) return false;
  storage.removeItem(storageKey);
  return true;
}

export function updateDealInput(deal: Deal, fields: { title: string; description: string; amount: string; deadline: string; supplierId?: string }, now = Date.now()): UpdateDealInput {
  // datetime-local displays minutes; leaving it untouched must not silently truncate server seconds.
  const unchangedDeadline = fields.deadline === localDateTime(deal.deadline_at);
  const input = createDealInput({ supplierId: deal.supplier_party_id, ...fields, deadline: unchangedDeadline ? deal.deadline_at : fields.deadline }, now);
  if (unchangedDeadline) input.deadline_at = deal.deadline_at;
  return { expected_version: deal.deal_version, ...input };
}

export function agreeDealInput(deal: Deal): AgreeDealInput {
  if (!deal.document || deal.document.deal_version !== deal.deal_version) throw new Error('Для текущей версии условий нужен PDF-документ.');
  return { deal_version: deal.deal_version, document_id: deal.document.id, document_version: deal.document.document_version, document_sha256: deal.document.sha256 };
}

export function reworkDealInput(deal: Deal, reason: string): ReworkDealInput {
  reason = reason.trim();
  if (!nonempty(reason, 2000)) throw new Error('Укажите причину доработки: от 1 до 2 000 символов.');
  return { deal_version: deal.deal_version, reason };
}

export async function pdfIdentity(file: File): Promise<{ file_name: string; size_bytes: number; sha256: string }> {
  if (!/\.pdf$/i.test(file.name) || (file.type && file.type !== 'application/pdf')) throw new Error('Выберите PDF-документ с расширением .pdf.');
  if (!file.size || file.size > MAX_PDF_BYTES) throw new Error('PDF должен быть непустым, размером не более 5 МиБ.');
  if ([...file.name].length > 255) throw new Error('Сократите имя PDF до 255 символов.');
  const bytes = await file.arrayBuffer();
  if (new TextDecoder('ascii').decode(bytes.slice(0, 5)) !== '%PDF-') throw new Error('Файл не похож на PDF. Проверка содержимого также выполняется сервером.');
  const digest = await crypto.subtle.digest('SHA-256', bytes);
  return { file_name: file.name, size_bytes: file.size, sha256: Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('') };
}

export async function verifyPendingPdf(attempt: Extract<PendingAgreement, { kind: 'upload' }>, file: File): Promise<void> {
  const identity = await pdfIdentity(file);
  if (identity.size_bytes !== attempt.input.size_bytes || identity.sha256 !== attempt.input.sha256) {
    throw new Error('Выбран другой PDF. Для проверки результата выберите файл с теми же байтами; сохранённая команда не изменена.');
  }
  // The upload caller preserves the original file_name even when a user renamed identical bytes.
}

/** These are definitive rejections, not evidence inferred from a timeout or a changed UI. */
export function definitiveAgreementFailure(error: unknown): boolean {
  return error instanceof ApiError && [
    'INVALID_REQUEST', 'INVALID_AMOUNT', 'AMOUNT_OVERFLOW', 'DEADLINE_PASSED', 'RESOURCE_NOT_FOUND',
    'IDEMPOTENCY_KEY_REQUIRED', 'VERSION_CONFLICT', 'INVALID_STATE', 'DOCUMENT_REQUIRED', 'INVALID_PDF',
    'PAYLOAD_TOO_LARGE', 'UNSUPPORTED_MEDIA_TYPE', 'FORBIDDEN',
  ].includes(error.code);
}
