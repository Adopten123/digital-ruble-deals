export type OrganisationKind = 'COMPANY' | 'IP';

export interface Organisation {
  id: string;
  name: string;
  kind: OrganisationKind;
  role: string;
}

export interface Session {
  user: { id: string; email: string; system_role: string };
  organisation: Organisation | null;
  session_expires_at: string;
}

export interface OwnOrganisation {
  organisation: Organisation;
  account: {
    id: string;
    currency: 'RUB';
    total_kopecks: string;
    reserved_kopecks: string;
    available_kopecks: string;
  };
}

export interface DemoAccount {
  id: string;
  organisation: { id: string; name: string; kind: OrganisationKind };
  currency: 'RUB'; total_kopecks: string; reserved_kopecks: string; available_kopecks: string;
}
export interface DemoMoneyInput { amount_kopecks: string; reason: string }
export interface DemoMoneyResult { transaction_id: string; account: DemoAccount }
export interface DealReserve {
  id: string; deal_id: string; deal_version: number; account_id: string;
  amount_kopecks: string; currency: 'RUB'; status: 'ACTIVE'; created_at: string;
}
export interface ReserveDealInput { deal_version: number }
export interface ReserveResult { reserve: DealReserve; deal: Deal }

export interface Counterparty {
  id: string;
  name: string;
  kind: OrganisationKind;
  is_demo: boolean;
}

export interface Page<T> {
  items: T[];
  next_cursor: string | null;
}

export interface RegisterInput {
  email: string;
  password: string;
  organisation: { name: string; kind: OrganisationKind };
}

export interface CreateDealInput {
  supplier_party_id: string;
  title: string;
  description: string;
  amount_kopecks: string;
  currency: 'RUB';
  deadline_at: string;
}

export interface DealParty { id: string; name: string; kind: OrganisationKind }

export interface DealDocument {
  id: string; deal_id: string; deal_version: number; document_version: number;
  sha256: string; file_name: string; size_bytes: number; content_type: string; created_at: string;
}

export interface DealAgreement {
  id: string; organisation_id: string; user_id: string; deal_version: number;
  document_id: string; document_version: number; document_sha256: string; created_at: string;
}

export interface DealSignature {
  id: string; organisation_id: string; user_id: string; deal_version: number;
  document_id: string; document_version: number; document_sha256: string; created_at: string;
}

export interface SignDocumentInput { deal_version: number; document_version: number; document_sha256: string }
export interface SignDocumentResult { signature: DealSignature; deal: Deal }

export interface UpdateDealInput extends Partial<CreateDealInput> { expected_version: number }
export interface AgreeDealInput { deal_version: number; document_id: string; document_version: number; document_sha256: string }
export interface ReworkDealInput { deal_version: number; reason: string }
export interface GenerateDocumentInput { expected_deal_version: number; template_id: 'printing-acceptance-v1' }
export interface DocumentResult { deal: Deal; document: DealDocument }

export interface Deal extends CreateDealInput {
  id: string;
  deal_version: number;
  status: 'DRAFT' | 'AGREED' | 'FUNDED' | 'READY_TO_SETTLE' | 'REVIEW_REQUIRED';
  customer_party_id: string;
  customer: DealParty;
  supplier: DealParty;
  document: DealDocument | null;
  agreements: DealAgreement[];
  signatures: DealSignature[];
  reserve: DealReserve | null;
  payment_id: null;
  dispute: null;
  cancellation_request: null;
  created_at: string;
  updated_at: string;
}

export interface DealFilters {
  q?: string;
  status?: '' | 'DRAFT' | 'AGREED' | 'FUNDED' | 'READY_TO_SETTLE' | 'REVIEW_REQUIRED';
  role?: '' | 'customer' | 'supplier';
  created_from?: string;
  created_to?: string;
}

const errorMessages: Record<string, string> = {
  INVALID_REQUEST: 'Проверьте заполненные поля и повторите запрос.',
  AUTH_REQUIRED: 'Войдите в аккаунт, чтобы продолжить.',
  INVALID_CREDENTIALS: 'Не удалось войти. Проверьте email и пароль.',
  SESSION_EXPIRED: 'Сессия завершилась. Войдите снова.',
  SESSION_CHANGED: 'Аккаунт изменился в другой вкладке. Проверьте организацию перед повторным действием.',
  FORBIDDEN: 'У вашего аккаунта нет прав для этого действия.',
  CSRF_INVALID: 'Защитный токен устарел. Повторите действие, чтобы получить новый.',
  EMAIL_CONFLICT: 'Этот email уже используется. Войдите или укажите другой.',
  RESOURCE_NOT_FOUND: 'Данные недоступны. Обновите страницу.',
  RATE_LIMITED: 'Слишком много попыток. Подождите немного и попробуйте снова.',
  INTERNAL_ERROR: 'Сервер не смог обработать запрос. Попробуйте позже.',
  TEMPORARILY_UNAVAILABLE: 'Сервис временно недоступен. Проверьте готовность стенда.',
  INVALID_AMOUNT: 'Проверьте сумму: нужны положительные целые копейки в допустимом диапазоне.',
  AMOUNT_OVERFLOW: 'Сумма превышает допустимый предел.',
  DEADLINE_PASSED: 'Срок сделки уже наступил. Обновите карточку и проверьте условия.',
  IDEMPOTENCY_KEY_REQUIRED: 'Не удалось подтвердить ключ создания сделки. Обновите страницу.',
  IDEMPOTENCY_CONFLICT: 'Этот запрос уже использован для других условий. Обновите список и проверьте созданную сделку.',
  COMMIT_OUTCOME_UNKNOWN: 'Ответ о сохранении не получен. Повторите тот же запрос для проверки результата.',
  VERSION_CONFLICT: 'Условия или документ уже изменились. Обновите сделку и согласуйте актуальную версию.',
  INVALID_STATE: 'Действие недоступно в текущем состоянии. Обновите сделку.',
  DOCUMENT_REQUIRED: 'Сначала добавьте PDF для актуальной версии условий.',
  INVALID_PDF: 'Файл не прошёл проверку PDF. Выберите корректный документ.',
  INVALID_DOCUMENT: 'Файл не прошёл проверку PDF. Выберите корректный документ без активного содержимого.',
  PAYLOAD_TOO_LARGE: 'Файл слишком большой. Максимальный размер PDF — 5 МиБ.',
  UNSUPPORTED_MEDIA_TYPE: 'Нужен PDF-документ. Выберите файл с расширением .pdf.',
  CONSENTS_MISSING: 'Для следующего действия нужны согласия обеих сторон на актуальный документ.',
  RESERVE_REQUIRED: 'Для подписи нужен действующий резерв полной суммы. Обновите карточку.',
  DOCUMENT_MISMATCH: 'Документ или его версия изменились. Обновите карточку перед следующим действием.',
  INSUFFICIENT_AVAILABLE_FUNDS: 'Недостаточно доступных тестовых средств. Обновите остатки; активные резервы нельзя расходовать.',
};

export class ApiError extends Error {
  status: number;
  code: string;
  requestId?: string;

  constructor(status: number, code: string, requestId?: string) {
    super(errorMessages[code] ?? 'Не удалось выполнить запрос. Повторите попытку позже.');
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.requestId = requestId;
  }
}

export function parseApiError(status: number, body: unknown): ApiError {
  if (body && typeof body === 'object' && 'error' in body) {
    const error = body.error;
    const code = error && typeof error === 'object' && 'code' in error && typeof error.code === 'string'
      ? error.code : 'UNKNOWN_ERROR';
    const requestId = 'request_id' in body && typeof body.request_id === 'string'
      ? body.request_id : undefined;
    return new ApiError(status, code, requestId);
  }
  return new ApiError(status, 'UNKNOWN_ERROR');
}

export function describeError(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof TypeError) return 'Нет связи с сервером. Проверьте запуск стенда и повторите запрос.';
  return error instanceof Error ? error.message : 'Не удалось загрузить данные. Повторите запрос.';
}

export class ApiClient {
  private token: string | null = null;
  private tokenRequest: Promise<string> | null = null;
  private tokenGeneration = 0;
  private userId: string | null = null;
  private fetcher: typeof fetch;

  constructor(fetcher: typeof fetch = (...args) => globalThis.fetch(...args)) {
    this.fetcher = fetcher;
  }

  clearCsrf(): void {
    this.tokenGeneration += 1;
    this.token = null;
    this.tokenRequest = null;
  }

  bindIdentity(userId: string | null): void {
    if (this.userId !== userId) this.clearCsrf();
    this.userId = userId;
  }

  currentIdentity(): string | null { return this.userId; }

  async csrf(): Promise<string> {
    if (this.token) return this.token;
    if (!this.tokenRequest) {
      const generation = this.tokenGeneration;
      this.tokenRequest = this.request<{ csrf_token: string }>('/auth/csrf')
        .then((body) => {
          if (generation !== this.tokenGeneration) throw new ApiError(409, 'SESSION_CHANGED');
          if (typeof body.csrf_token !== 'string' || !body.csrf_token) {
            throw new Error('Сервер не выдал защитный токен. Повторите запрос.');
          }
          this.token = body.csrf_token;
          return body.csrf_token;
        }).finally(() => { if (generation === this.tokenGeneration) this.tokenRequest = null; });
    }
    return this.tokenRequest;
  }

  private async request<T>(path: string, options: RequestInit = {}): Promise<T> {
    const generation = this.tokenGeneration;
    const response = await this.fetcher(`/api/v1${path}`, {
      ...options,
      credentials: 'same-origin',
      headers: { Accept: 'application/json', ...options.headers },
    });
    if (!response.ok) {
      const body: unknown = await response.json().catch(() => null);
      const error = parseApiError(response.status, body);
      if (generation === this.tokenGeneration && (error.code === 'CSRF_INVALID' || error.code === 'SESSION_CHANGED' || response.status === 401)) this.clearCsrf();
      throw error;
    }
    if (response.status === 204) return undefined as T;
    try { return await response.json() as T; }
    catch { throw new Error('Сервер вернул некорректный ответ. Повторите запрос.'); }
  }

  private identityHeaders(userId = this.userId): Record<string, string> {
    return userId ? { 'X-Expected-User-ID': userId } : {};
  }

  private async mutate<T>(path: string, body?: unknown, idempotencyKey?: string, protectedRequest = false, method = 'POST'): Promise<T> {
    // Capture before awaiting CSRF: a later UI session change must not change this command's actor.
    const identityHeaders = protectedRequest ? this.identityHeaders() : {};
    const token = await this.csrf();
    return this.request<T>(path, {
      method,
      headers: {
        ...identityHeaders,
        'X-CSRF-Token': token,
        ...(idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : {}),
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
  }

  me(): Promise<Session> { return this.request('/auth/me'); }
  login(email: string, password: string): Promise<Session> {
    return this.mutate('/auth/login', { email, password });
  }
  register(input: RegisterInput): Promise<Session> { return this.mutate('/auth/register', input); }
  logout(): Promise<void> { return this.mutate('/auth/logout', undefined, undefined, true); }
  ownOrganisation(): Promise<OwnOrganisation> { return this.request('/organisations/me', { headers: this.identityHeaders() }); }
  organisations(query: string, cursor?: string): Promise<Page<Counterparty>> {
    const params = new URLSearchParams({ q: query, limit: '20' });
    if (cursor) params.set('cursor', cursor);
    return this.request(`/organisations?${params.toString()}`, { headers: this.identityHeaders() });
  }
  createDeal(input: CreateDealInput, idempotencyKey: string): Promise<Deal> {
    return this.mutate('/deals', input, idempotencyKey, true);
  }
  deals(filters: DealFilters = {}, cursor?: string): Promise<Page<Deal>> {
    const params = new URLSearchParams({ limit: '20' });
    for (const [name, value] of Object.entries(filters)) if (value) params.set(name, value);
    if (cursor) params.set('cursor', cursor);
    return this.request(`/deals?${params.toString()}`, { headers: this.identityHeaders() });
  }
  deal(id: string): Promise<Deal> { return this.request(`/deals/${encodeURIComponent(id)}`, { headers: this.identityHeaders() }); }
  updateDeal(id: string, input: UpdateDealInput, key: string): Promise<Deal> {
    return this.mutate(`/deals/${encodeURIComponent(id)}`, input, key, true, 'PATCH');
  }
  generateDocument(id: string, input: GenerateDocumentInput, key: string): Promise<DocumentResult> {
    return this.mutate(`/deals/${encodeURIComponent(id)}/documents/generate`, input, key, true);
  }
  async uploadDocument(id: string, expectedVersion: number, file: Blob, fileName: string, key: string): Promise<DocumentResult> {
    const identityHeaders = this.identityHeaders();
    const token = await this.csrf();
    const body = new FormData();
    body.append('expected_deal_version', String(expectedVersion));
    body.append('file', file, fileName);
    return this.request(`/deals/${encodeURIComponent(id)}/documents`, {
      method: 'POST', body,
      headers: { ...identityHeaders, 'X-CSRF-Token': token, 'Idempotency-Key': key },
    });
  }
  agreeDeal(id: string, input: AgreeDealInput, key: string): Promise<Deal> {
    return this.mutate(`/deals/${encodeURIComponent(id)}/agree`, input, key, true);
  }
  reworkDeal(id: string, input: ReworkDealInput, key: string): Promise<Deal> {
    return this.mutate(`/deals/${encodeURIComponent(id)}/rework`, input, key, true);
  }
  reserveDeal(id: string, input: ReserveDealInput, key: string): Promise<ReserveResult> {
    return this.mutate(`/deals/${encodeURIComponent(id)}/reserve`, input, key, true);
  }
  signDocument(id: string, input: SignDocumentInput, key: string): Promise<SignDocumentResult> {
    return this.mutate(`/documents/${encodeURIComponent(id)}/sign`, input, key, true);
  }
  demoAccounts(query: string, cursor?: string): Promise<Page<DemoAccount>> {
    const params = new URLSearchParams({ q: query, limit: '20' });
    if (cursor) params.set('cursor', cursor);
    return this.request(`/demo/accounts?${params}`, { headers: this.identityHeaders() });
  }
  demoMoney(accountId: string, operation: 'credits' | 'debits', input: DemoMoneyInput, key: string): Promise<DemoMoneyResult> {
    return this.mutate(`/demo/accounts/${encodeURIComponent(accountId)}/${operation}`, input, key, true);
  }
  document(id: string, version?: number): Promise<DealDocument> {
    return this.request(`/documents/${encodeURIComponent(id)}${version === undefined ? '' : `?version=${version}`}`, { headers: this.identityHeaders() });
  }
  async documentFile(id: string, version: number, download = false): Promise<Blob> {
    const generation = this.tokenGeneration;
    const response = await this.fetcher(`/api/v1/documents/${encodeURIComponent(id)}/${download ? 'download' : 'view'}?version=${version}`, {
      credentials: 'same-origin', headers: { ...this.identityHeaders(), Accept: 'application/pdf' },
    });
    if (!response.ok) {
      const error = parseApiError(response.status, await response.json().catch(() => null));
      if (generation === this.tokenGeneration && (error.code === 'CSRF_INVALID' || error.code === 'SESSION_CHANGED' || response.status === 401)) this.clearCsrf();
      throw error;
    }
    if (!response.headers.get('Content-Type')?.toLowerCase().startsWith('application/pdf')) throw new Error('Сервер вернул файл неизвестного формата. PDF не открыт.');
    return response.blob();
  }
}

export const api = new ApiClient();
