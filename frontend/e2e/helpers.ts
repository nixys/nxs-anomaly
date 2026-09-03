import {
  expect,
  request as playwrightRequest,
  type APIRequestContext,
  type Locator,
  type Page,
} from '@playwright/test';

// Shared helpers for the browser e2e suite. The critical responder flow lives in
// responder-flow.spec.ts; the specs here extend browser coverage to first-time
// setup, schedules, delivery failure and RBAC/cross-team isolation.

export const API_BASE = `http://localhost:${process.env.E2E_API_PORT ?? '8080'}`;
export const API_KEY = process.env.E2E_API_KEY ?? 'e2e-key';
export const ADMIN_USER = process.env.E2E_ADMIN_USER ?? 'e2e-admin';
export const ADMIN_PASSWORD = process.env.E2E_ADMIN_PASSWORD ?? 'e2e-password-1234';

/**
 * A form field by its label, exactly — tolerating Mantine's required marker.
 *
 * Mantine renders a required field's label as `Name<span aria-hidden> *</span>`,
 * so the label's text content is "Name *". Playwright's getByLabel matches the
 * label's text, not the accessible name (the accessible name *is* "Name", since
 * the marker is aria-hidden), so `getByLabel('Name', { exact: true })` matches
 * nothing on every required field — while dropping `exact` would make 'Name'
 * also match 'Username'. Anchoring with a regex keeps both properties.
 */
export function field(scope: Page | Locator, label: string): Locator {
  const escaped = label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return scope.getByLabel(new RegExp(`^${escaped}\\s*\\*?$`));
}

// A unique suffix so repeated runs against a persistent DB do not collide.
export function uniqueSuffix(): string {
  return `${Date.now().toString(36)}${Math.floor(Math.random() * 1e4).toString(36)}`;
}

// An admin API context (X-API-Key) for the setup a spec does out of band.
export async function adminApi(): Promise<APIRequestContext> {
  return playwrightRequest.newContext({
    baseURL: API_BASE,
    extraHTTPHeaders: { 'X-API-Key': API_KEY },
  });
}

// Sign in through the real login form. Defaults to the bootstrap admin; pass a
// username/password to sign in as another principal (RBAC specs).
export async function signIn(
  page: Page,
  username: string = ADMIN_USER,
  password: string = ADMIN_PASSWORD,
): Promise<void> {
  await page.goto('/');
  await field(page, 'Username or e-mail').fill(username);
  await field(page, 'Password').fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  // The shell renders its navigation landmark only once authenticated. The first
  // sign-in of a run also pays the Vite dev-server cold compile, so allow extra
  // time here rather than let it surface as flake.
  await expect(page.getByRole('navigation')).toBeVisible({ timeout: 45_000 });
}

// Submit the currently open Mantine modal by its primary button ("Create" on a
// new object, "Save" on an edit). Scoped to the dialog so the same label on the
// page underneath is never matched.
export async function submitModal(page: Page, name: 'Create' | 'Save' = 'Create'): Promise<void> {
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name, exact: true }).click();
  await expect(dialog).toBeHidden();
}
