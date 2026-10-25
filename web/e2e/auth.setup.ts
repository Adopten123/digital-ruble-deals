import { test as setup, expect } from '@playwright/test';
import { mkdirSync, writeFileSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import { API_BASE_URL, E2E_BASE_URL, actorsFile, authDir, csrf, statePath, type Actors, type Role } from './support.ts';

setup('prepare synthetic authenticated actors on the test contour', async ({ playwright }) => {
  mkdirSync(authDir, { recursive: true });
  const actors = {} as Actors;
  const probe = await playwright.request.newContext({ baseURL: API_BASE_URL, maxRedirects: 0 });
  const ready = await probe.get('/readyz');
  expect(ready.status()).toBe(200);
  expect(await ready.json()).toMatchObject({ contour: 'test', stage: 'audit_integrity' });
  await probe.dispose();
  for (const role of ['customer', 'supplier', 'operator', 'outsider'] as Role[]) {
    const context = await playwright.request.newContext({ baseURL: API_BASE_URL, maxRedirects: 0 });
    try {
      const outsider = role === 'outsider';
      const body = outsider
        ? { email: `e2e-outsider-${randomUUID()}@demo.poaktu.local`, password: 'E2eOnlyPassword2026!', organisation: { name: `E2E outsider ${randomUUID()}`, kind: 'IP' } }
        : { email: `${role}@demo.poaktu.local`, password: 'PoaktuDemo2026!' };
      let response = await context.post(`/api/v1/auth/${outsider ? 'register' : 'login'}`, {
        data: body, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(context) },
      });
      if (response.status() === 429) {
        expect((await response.json()).error.code).toBe('RATE_LIMITED');
        const seconds = Number(response.headers()['retry-after']);
        expect(Number.isInteger(seconds) && seconds >= 1 && seconds <= 60).toBe(true);
        setup.setTimeout(setup.info().timeout + seconds * 1000);
        await setup.step('respect explicit auth Retry-After before one setup retry', () => delay(seconds * 1000));
        response = await context.post(`/api/v1/auth/${outsider ? 'register' : 'login'}`, {
          data: body, headers: { Origin: E2E_BASE_URL, 'X-CSRF-Token': await csrf(context) },
        });
      }
      expect(response.status(), `authenticate ${role}`).toBe(outsider ? 201 : 200);
      actors[role] = { ...await response.json(), statePath: statePath(role) };
      await context.storageState({ path: statePath(role) });
    } finally { await context.dispose(); }
  }
  writeFileSync(actorsFile, JSON.stringify(actors, null, 2), { mode: 0o600 });
});
