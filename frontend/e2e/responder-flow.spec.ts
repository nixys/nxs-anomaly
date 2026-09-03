import { test, expect, request as playwrightRequest, type APIRequestContext } from '@playwright/test';
import { field } from './helpers';

// BETA-002 critical responder flow, end to end through a real browser:
// login → (config set up over the API) → ingest an alert → the browser sees the
// group → acknowledge → resolve → delivery attempts are visible. Every alert
// action-route is exercised through the UI; a failure of any blocks the release.

const API_BASE = `http://localhost:${process.env.E2E_API_PORT ?? '8080'}`;
const API_KEY = process.env.E2E_API_KEY ?? 'e2e-key';
const ADMIN_USER = process.env.E2E_ADMIN_USER ?? 'e2e-admin';
const ADMIN_PASSWORD = process.env.E2E_ADMIN_PASSWORD ?? 'e2e-password-1234';

// A unique suffix so repeated local runs against a persistent DB don't collide.
const suffix = Date.now().toString(36);

async function setupConfig(api: APIRequestContext): Promise<{ integrationKey: string; alertTitle: string }> {
  const chain = await (
    await api.post('/api/v1/escalation-chains', {
      data: { name: `e2e-chain-${suffix}`, steps: [{ kind: 'NOTIFY_USER', user_ids: [] }] },
    })
  ).json();
  const user = await (
    await api.post('/api/v1/users', {
      data: {
        name: `E2E Responder ${suffix}`,
        username: `e2e-responder-${suffix}`,
        // The log channel delivers locally, so the flow needs no external provider.
        notification_targets: [{ type: 'log', target: '' }],
      },
    })
  ).json();
  await api.put(`/api/v1/escalation-chains/${chain.id}`, {
    data: { steps: [{ kind: 'NOTIFY_USER', user_ids: [user.id] }] },
  });
  const integration = await (
    await api.post('/api/v1/integrations', {
      data: {
        name: `e2e-integration-${suffix}`,
        routes: [
          { name: 'default', match_type: 'all', is_default: true, escalation_chain_id: chain.id },
        ],
      },
    })
  ).json();
  const alertTitle = `e2e boom ${suffix}`;
  const ingest = await api.post(`/integrations/v1/webhook/${integration.key}`, {
    data: { title: alertTitle },
  });
  expect(ingest.ok(), `ingest failed: ${ingest.status()}`).toBeTruthy();
  return { integrationKey: integration.key, alertTitle };
}

async function signIn(page: import('@playwright/test').Page) {
  await page.goto('/');
  await field(page, 'Username or e-mail').fill(ADMIN_USER);
  await field(page, 'Password').fill(ADMIN_PASSWORD);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  // The shell renders its navigation landmark only once authenticated. Anchor on
  // the landmark itself, not a link inside it: the dashboard repeats several nav
  // targets ("Users", "All alert groups") as in-content links, so a bare link
  // name is ambiguous. Navigation between pages below uses page.goto for the same
  // reason — it is unambiguous and still exercises the SPA router.
  await expect(page.getByRole('navigation')).toBeVisible();
}

test('responder can sign in, triage an alert, acknowledge and resolve it', async ({ page }) => {
  const api = await playwrightRequest.newContext({
    baseURL: API_BASE,
    extraHTTPHeaders: { 'X-API-Key': API_KEY },
  });
  const { alertTitle } = await setupConfig(api);

  await signIn(page);

  // The ingested alert group should appear; the worker groups it under its title.
  await page.goto('/alert-groups');
  const groupLink = page.getByRole('main').getByRole('link', { name: alertTitle });
  await expect(async () => {
    await page.reload();
    await expect(groupLink).toBeVisible({ timeout: 2_000 });
  }).toPass({ timeout: 30_000 });

  await groupLink.click();

  // Acknowledge, then resolve — both through the UI action buttons.
  await page.getByRole('button', { name: 'Acknowledge', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Unacknowledge' })).toBeVisible();

  await page.getByRole('button', { name: 'Resolve', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Unresolve' })).toBeVisible();

  // Delivery must be visible: the log notification for the paged user was queued
  // and delivered by the worker. The section header carries the count.
  await expect(async () => {
    await page.reload();
    await expect(page.getByText(/Notifications \(\s*[1-9]/)).toBeVisible({ timeout: 2_000 });
  }).toPass({ timeout: 30_000 });

  await api.dispose();
});
