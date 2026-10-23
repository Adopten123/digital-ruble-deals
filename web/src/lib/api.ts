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

export interface Deal extends CreateDealInput {
  id: string;
  deal_version: number;
  status: 'DRAFT' | 'REVIEW_REQUIRED';
  customer_party_id: string;
  customer: DealParty;
  supplier: DealParty;
  document: null;
  agreements: unknown[];
  signatures: unknown[];
  reserve: null;
  payment_id: null;
  dispute: null;
  cancellation_request: null;
  created_at: string;
  updated_at: string;
}

export interface DealFilters {
  q?: string;
  status?: '' | 'DRAFT' | 'REVIEW_REQUIRED';
  role?: '' | 'customer' | 'supplier';
  created_from?: string;
  created_to?: string;
}

const errorMessages: Record<string, string> = {
  INVALID_REQUEST: 'Проверьте заполненные поля и повторите запрос.',
  AUTH_REQUIRED: 'Войдите в аккаунт, чтобы продолжить.',
  INVALID_CREDENTIALS: 'Не удалось войти. Проверьте email и пароль.',
  SESSION_EXPIRED: 'Сессия завершилась. Войдите снова.',
  FORBIDDEN: 'У вашего аккаунта нет прав для этого действия.',
  CSRF_INVALID: 'Защитный токен устарел. Повторите действие, чтобы получить новый.',
  EMAIL_CONFLICT: 'Этот email уже используется. Войдите или укажите другой.',
  RESOURCE_NOT_FOUND: 'Данные недоступны. Обновите страницу.',
  RATE_LIMITED: 'Слишком много попыток. Подождите немного и попробуйте снова.',
  INTERNAL_ERROR: 'Сервер не смог обработать запрос. Попробуйте позже.',
  TEMPORARILY_UNAVAILABLE: 'Сервис временно недоступен. Проверьте готовность стенда.',
  INVALID_AMOUNT: 'Проверьте сумму: нужны положительные целые копейки в допустимом диапазоне.',
  AMOUNT_OVERFLOW: 'Сумма превышает допустимый предел.',
  DEADLINE_PASSED: 'Срок уже наступил. Укажите время в будущем.',
  IDEMPOTENCY_KEY_REQUIRED: 'Не удалось подтвердить ключ создания сделки. Обновите страницу.',
  IDEMPOTENCY_CONFLICT: 'Этот запрос уже использован для других условий. Обновите список и проверьте созданную сделку.',
  COMMIT_OUTCOME_UNKNOWN: 'Ответ о сохранении не получен. Повторите тот же запрос для проверки результата.',
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
  private fetcher: typeof fetch;

  constructor(fetcher: typeof fetch = (...args) => globalThis.fetch(...args)) {
    this.fetcher = fetcher;
  }

  clearCsrf(): void {
    this.token = null;
    this.tokenRequest = null;
  }

  async csrf(): Promise<string> {
    if (this.token) return this.token;
    if (!this.tokenRequest) {
      this.tokenRequest = this.request<{ csrf_token: string }>('/auth/csrf')
        .then((body) => {
          if (typeof body.csrf_token !== 'string' || !body.csrf_token) {
            throw new Error('Сервер не выдал защитный токен. Повторите запрос.');
          }
          this.token = body.csrf_token;
          return body.csrf_token;
        }).finally(() => { this.tokenRequest = null; });
    }
    return this.tokenRequest;
  }

  private async request<T>(path: string, options: RequestInit = {}): Promise<T> {
    const response = await this.fetcher(`/api/v1${path}`, {
      ...options,
      credentials: 'same-origin',
      headers: { Accept: 'application/json', ...options.headers },
    });
    if (!response.ok) {
      const body: unknown = await response.json().catch(() => null);
      const error = parseApiError(response.status, body);
      if (error.code === 'CSRF_INVALID' || response.status === 401) this.clearCsrf();
      throw error;
    }
    if (response.status === 204) return undefined as T;
    try { return await response.json() as T; }
    catch { throw new Error('Сервер вернул некорректный ответ. Повторите запрос.'); }
  }

  private async mutate<T>(path: string, body?: unknown, idempotencyKey?: string): Promise<T> {
    const token = await this.csrf();
    return this.request<T>(path, {
      method: 'POST',
      headers: {
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
  logout(): Promise<void> { return this.mutate('/auth/logout'); }
  ownOrganisation(): Promise<OwnOrganisation> { return this.request('/organisations/me'); }
  organisations(query: string, cursor?: string): Promise<Page<Counterparty>> {
    const params = new URLSearchParams({ q: query, limit: '20' });
    if (cursor) params.set('cursor', cursor);
    return this.request(`/organisations?${params.toString()}`);
  }
  createDeal(input: CreateDealInput, idempotencyKey: string): Promise<Deal> {
    return this.mutate('/deals', input, idempotencyKey);
  }
  deals(filters: DealFilters = {}, cursor?: string): Promise<Page<Deal>> {
    const params = new URLSearchParams({ limit: '20' });
    for (const [name, value] of Object.entries(filters)) if (value) params.set(name, value);
    if (cursor) params.set('cursor', cursor);
    return this.request(`/deals?${params.toString()}`);
  }
  deal(id: string): Promise<Deal> { return this.request(`/deals/${encodeURIComponent(id)}`); }
}

export const api = new ApiClient();
