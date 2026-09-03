import { test, expect } from '@playwright/test';
import { adminApi, field, signIn, submitModal, uniqueSuffix } from './helpers';

// Browser coverage: a first-time operator builds the whole routing config through
// the UI — team, user, escalation chain, integration — and an alert ingested
// against that UI-built integration lands as a visible group. Unlike
// responder-flow (which sets config up over the API), here every object is
// created by clicking through the forms, so the create screens are release-gated.

test('an operator can set up team, user, chain and integration entirely through the UI', async ({ page }) => {
  const s = uniqueSuffix();
  const teamName = `e2e-team-${s}`;
  const userName = `E2E User ${s}`;
  const chainName = `e2e-chain-${s}`;
  const integrationName = `e2e-integration-${s}`;

  await signIn(page);

  // Team.
  await page.goto('/teams');
  await page.getByRole('button', { name: 'Add team', exact: true }).click();
  await field(page.getByRole('dialog'), 'Name').fill(teamName);
  await submitModal(page);
  await expect(page.getByRole('main').getByText(teamName, { exact: true })).toBeVisible();

  // User.
  await page.goto('/users');
  await page.getByRole('button', { name: 'Add user', exact: true }).click();
  await field(page.getByRole('dialog'), 'Name').fill(userName);
  await field(page.getByRole('dialog'), 'Username').fill(`e2e-user-${s}`);
  await submitModal(page);
  await expect(page.getByRole('main').getByText(userName, { exact: true })).toBeVisible();

  // Escalation chain.
  await page.goto('/escalation-chains');
  await page.getByRole('button', { name: 'Add chain', exact: true }).click();
  await field(page.getByRole('dialog'), 'Name').fill(chainName);
  await submitModal(page);
  await expect(page.getByRole('main').getByText(chainName, { exact: true })).toBeVisible();

  // Integration routed to that chain by default.
  await page.goto('/integrations');
  await page.getByRole('button', { name: 'Add integration', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await field(dialog, 'Name').fill(integrationName);
  // Mantine's Select associates its label with both the input and the options
  // listbox, so target the input (first match) then pick the option.
  await dialog.getByLabel('Default escalation chain').first().click();
  await page.getByRole('option', { name: chainName }).click();
  await submitModal(page);
  await expect(page.getByRole('main').getByText(integrationName, { exact: true })).toBeVisible();

  // Ingest against the UI-built integration and confirm it routes to a visible
  // group. The ingest key is not surfaced for copy-paste convenience in this
  // flow, so read it back over the API (the integration itself was built in UI).
  const api = await adminApi();
  const list = await (await api.get('/api/v1/integrations')).json();
  const created = (list.items as Array<{ name: string; key: string }>).find((i) => i.name === integrationName);
  expect(created, 'integration created through the UI should be listed').toBeTruthy();
  const alertTitle = `e2e setup boom ${s}`;
  const ingest = await api.post(`/integrations/v1/webhook/${created!.key}`, { data: { title: alertTitle } });
  expect(ingest.ok(), `ingest failed: ${ingest.status()}`).toBeTruthy();

  await page.goto('/alert-groups');
  const groupLink = page.getByRole('main').getByRole('link', { name: alertTitle });
  await expect(async () => {
    await page.reload();
    await expect(groupLink).toBeVisible({ timeout: 2_000 });
  }).toPass({ timeout: 30_000 });

  await api.dispose();
});
