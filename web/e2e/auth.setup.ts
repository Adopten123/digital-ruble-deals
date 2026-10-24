import { test as setup, expect } from '@playwright/test';
import { mkdirSync, writeFileSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { API_BASE_URL, E2E_BASE_URL, actorsFile, authDir, csrf, statePath, type Actors, type Role } from './support.ts';

setup('prepare synthetic authenticated actors on the test contour', async ({ playwright }) => {
  mkdirSync(authDir, { recursive: true });
  const actors = {} as Actors;
  const probe = await playwright.request.newContext({ baseURL: API_BASE_URL, maxRedirects: 0 });
  const ready = await probe.get('/readyz');
  expect(ready.status()).toBe(200);
  expect(await ready.json()).toMatchObject({ contour: 'test', stage: 'edo_settlement' });
  await probe.dispose();
  for (const role of ['customer', 'supplier', 'operator', 'outsider'] as Role[]) {
    const context = await playwright.request.newContext({ baseURL: API_BASE_URL, maxRedirects: 0 });
    try {
      const outsider = role === 'outsider';
      const body = outsider
        ? { email: `e2e-outsider-${randomUUID()}@demo.poaktu.local`, password: 'E2eOnlyPassword2026!', organisation: { name: `E2E outsider ${randomUUID()}`, kind: 'IP' } }
        : { email: `${role}@demo.poaktu.local`, password: 'PoaktuDemo2026!' };
      const response = await context.post(`/api/v1/auth/${outsider ? 'register' : 'login'}`, {
        data: body, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(context) },
      });
      expect(response.status(), `authenticate ${role}`).toBe(outsider ? 201 : 200);
      actors[role] = { ...await response.json(), statePath: statePath(role) };
      await context.storageState({ path: statePath(role) });
    } finally { await context.dispose(); }
  }
  writeFileSync(actorsFile, JSON.stringify(actors, null, 2), { mode: 0o600 });
});
