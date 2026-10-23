import { test as base, expect, type APIRequestContext, type APIResponse, type PlaywrightWorkerArgs } from '@playwright/test';
import { randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { CreateDealInput, Session } from '../src/lib/api.ts';

function localURL(value: string): string {
  const url = new URL(value);
  if (!['http:', 'https:'].includes(url.protocol) || !['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
    || url.username || url.password || url.search || url.hash || url.pathname !== '/') {
    throw new Error('E2E targets must be explicit loopback origins, without credentials or paths.');
  }
  return url.origin;
}
export const E2E_BASE_URL = localURL(process.env.E2E_BASE_URL ?? 'http://localhost:18080');
export const API_BASE_URL = localURL(process.env.E2E_API_URL ?? E2E_BASE_URL);
if (new URL(E2E_BASE_URL).hostname !== new URL(API_BASE_URL).hostname) {
  throw new Error('UI and API test hostnames must match so cookie state is shared.');
}
export const authDir = join(dirname(fileURLToPath(import.meta.url)), '../test-results/auth');
export const actorsFile = join(authDir, 'actors.json');
export type Role = 'customer' | 'supplier' | 'outsider' | 'operator';
export type Actor = Session & { statePath: string };
export type Actors = Record<Role, Actor>;
export const statePath = (role: Role): string => join(authDir, `${role}.json`);
export function loadActors(): Actors { return JSON.parse(readFileSync(actorsFile, 'utf8')) as Actors; }
export const test = base.extend<{ actors: Actors }>({ actors: async ({}, use) => { await use(loadActors()); } });
export { expect };

export async function apiFor(playwright: PlaywrightWorkerArgs['playwright'], role: Role): Promise<APIRequestContext> {
  return playwright.request.newContext({ baseURL: API_BASE_URL, storageState: statePath(role), maxRedirects: 0 });
}
export async function csrf(context: APIRequestContext): Promise<string> {
  const response = await context.get('/api/v1/auth/csrf');
  expect(response.status(), 'CSRF preflight').toBe(200);
  const body = await response.json();
  expect(body.csrf_token).toEqual(expect.any(String));
  return body.csrf_token;
}
export function draftInput(overrides: Partial<CreateDealInput> = {}): CreateDealInput {
  const supplier = loadActors().supplier.organisation;
  if (!supplier) throw new Error('Synthetic supplier fixture has no organisation');
  return {
    supplier_party_id: supplier.id, title: `E2E ${randomUUID()}`,
    description: 'Синтетические условия проверки. Реальные средства и документы не используются.',
    amount_kopecks: '30000000', currency: 'RUB',
    deadline_at: new Date(Date.now() + 7 * 86_400_000).toISOString(), ...overrides,
  };
}
export async function postDraft(context: APIRequestContext, input: unknown = draftInput(), key: string = randomUUID()): Promise<APIResponse> {
  return context.post('/api/v1/deals', {
    headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(context), 'Idempotency-Key': key }, data: input,
  });
}
