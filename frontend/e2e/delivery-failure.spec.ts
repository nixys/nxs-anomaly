import { test, expect } from '@playwright/test';
import { adminApi, signIn, uniqueSuffix } from './helpers';

// Browser coverage: the two honesty-critical delivery signals.
//   1. Provider test — an operator can fire a test notification from a user's
//      policy editor and see the verdict.
//   2. Permanent failure — a notification that cannot be delivered is surfaced as
//      "permanently failed after retries", not silently swallowed.
// The e2e API runs with NXS_ANOMALY_NOTIFICATION_MAX_RETRIES=1 so the failure is
// terminal on the first attempt (see e2e/start-api.sh).

test('an operator can send a provider test and see the verdict', async ({ page }) => {
  const s = uniqueSuffix();
  const userName = `Tester ${s}`;
  const api = await adminApi();
  // A default policy with a log step: log delivers locally, so the test verdict is
  // deterministic and the policy editor renders a "Send a test log" button.
  await api.post('/api/v1/users', {
    data: {
      name: userName,
      username: `tester-${s}`,
      notification_policies: { default: [{ channel: 'log', target: '', wait_minutes: 0 }] },
    },
  });
  await api.dispose();

  await signIn(page);
  await page.goto('/users');
  await page.getByLabel(`Edit ${userName}`).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await dialog.getByLabel('Send a test log').click();

  // The verdict shows as a notification toast: "Test log: delivered".
  await expect(page.getByText(/Test log:\s*delivered/i)).toBeVisible();
});

test('a permanently failed delivery is surfaced on the alert group', async ({ page }) => {
  const s = uniqueSuffix();
  const alertTitle = `e2e dead-webhook ${s}`;
  const api = await adminApi();

  // A user whose only channel is a webhook to a closed port — every attempt fails.
  const user = await (
    await api.post('/api/v1/users', {
      data: {
        name: `Deadrouter ${s}`,
        username: `deadrouter-${s}`,
        notification_targets: [{ type: 'webhook', target: 'http://127.0.0.1:1/dead' }],
      },
    })
  ).json();
  const chain = await (
    await api.post('/api/v1/escalation-chains', {
      data: { name: `e2e-fail-chain-${s}`, steps: [{ kind: 'NOTIFY_USER', user_ids: [user.id] }] },
    })
  ).json();
  const integration = await (
    await api.post('/api/v1/integrations', {
      data: {
        name: `e2e-fail-integration-${s}`,
        routes: [{ name: 'default', match_type: 'all', is_default: true, escalation_chain_id: chain.id }],
      },
    })
  ).json();
  const ingest = await api.post(`/integrations/v1/webhook/${integration.key}`, { data: { title: alertTitle } });
  expect(ingest.ok(), `ingest failed: ${ingest.status()}`).toBeTruthy();

  await signIn(page);
  await page.goto('/alert-groups');
  const groupLink = page.getByRole('main').getByRole('link', { name: alertTitle });
  await expect(async () => {
    await page.reload();
    await expect(groupLink).toBeVisible({ timeout: 2_000 });
  }).toPass({ timeout: 30_000 });
  await groupLink.click();

  // The worker attempts the dead webhook, fails terminally, and the detail page
  // must say so rather than pretend the page was delivered.
  await expect(async () => {
    await page.reload();
    await expect(page.getByText(/permanently failed after retries/i)).toBeVisible({ timeout: 2_000 });
  }).toPass({ timeout: 40_000 });

  await api.dispose();
});
