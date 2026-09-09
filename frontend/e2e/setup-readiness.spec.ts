import { expect, test } from '@playwright/test';
import { adminApi, signIn } from './helpers';

// BETA-051: the setup wizard and the readiness report.
//
// The value of these in a browser is that the wizard's state is *derived* from
// the readiness report — the unit tests pin the derivation, this pins that the
// page is actually wired to the live report rather than to a hardcoded list.

test.describe('setup and readiness', () => {
  test('readiness names the checks and reports a verdict', async ({ page }) => {
    await signIn(page);
    await page.getByRole('link', { name: 'Readiness', exact: true }).click();

    await expect(page.getByRole('heading', { name: 'Readiness', exact: true })).toBeVisible();

    // Every check the engine can emit is titled on the page. The e2e API runs
    // with no backup reported, so at least that one must be a blocker — a page
    // that renders "ready" here would be lying.
    await expect(page.getByText('Database reachable')).toBeVisible();
    await expect(page.getByText('Every route reaches a chain with steps')).toBeVisible();
    await expect(page.getByText('A recent backup was reported')).toBeVisible();
    await expect(page.getByText('no backup has ever been reported', { exact: false })).toBeVisible();
  });

  test('reporting a backup clears that blocker on the next check', async ({ page }) => {
    const api = await adminApi();
    const before = await api.get('/api/v1/readiness');
    expect(before.ok()).toBeTruthy();
    const beforeBody = await before.json();
    const backupCheck = (b: { checks: { key: string; severity: string }[] }) =>
      b.checks.find((c) => c.key === 'backup');
    expect(backupCheck(beforeBody)?.severity).toBe('blocker');

    const reported = await api.post('/api/v1/backups/report', { data: { kind: 'e2e' } });
    expect(reported.ok()).toBeTruthy();

    await signIn(page);
    await page.getByRole('link', { name: 'Readiness', exact: true }).click();
    await expect(page.getByText('The last backup was reported', { exact: false })).toBeVisible();
    await api.dispose();
  });

  test('the wizard derives its steps from the same report', async ({ page }) => {
    await signIn(page);
    await page.getByRole('link', { name: 'Setup', exact: true }).click();

    await expect(page.getByRole('heading', { name: 'Setup', exact: true })).toBeVisible();
    await expect(page.getByText('of 5 steps done', { exact: false })).toBeVisible();

    // The steps are the wizard's own labels, in order.
    await expect(page.getByText('1. Add your team')).toBeVisible();
    await expect(page.getByText('6. Send a test alert')).toBeVisible();

    // The final step dry-runs routing without delivering anything. The button
    // renders whether or not an integration is selected, so this asserts the
    // control exists, not that a dry run succeeds.
    await expect(page.getByRole('button', { name: 'Dry run', exact: true })).toBeVisible();
  });
});
