import { test, expect } from '@playwright/test';
import { adminApi, field, signIn, submitModal, uniqueSuffix } from './helpers';

// Browser coverage: schedule lifecycle through the UI — create a schedule, put a
// rotation of participants on it, add an override, and see the coverage preview.
// Schedules v2 (rotation/override/coverage) had no browser gate before this.

test('an operator can build a rotation, add an override and see coverage in the UI', async ({ page }) => {
  const s = uniqueSuffix();
  const scheduleName = `e2e-sched-${s}`;

  // Two participants for the rotation (created over the API — the point of this
  // spec is the schedule UI, not user creation, which setup-flow already covers).
  const api = await adminApi();
  const alice = await (await api.post('/api/v1/users', { data: { name: `Alice ${s}`, username: `alice-${s}` } })).json();
  const bob = await (await api.post('/api/v1/users', { data: { name: `Bob ${s}`, username: `bob-${s}` } })).json();

  await signIn(page);

  // Create the schedule through the form.
  await page.goto('/schedules');
  await page.getByRole('button', { name: 'Add schedule', exact: true }).click();
  await field(page.getByRole('dialog'), 'Name').fill(scheduleName);
  await submitModal(page);
  await expect(page.getByRole('main').getByText(scheduleName, { exact: true })).toBeVisible();

  // Open its detail page.
  const schedules = await (await api.get('/api/v1/schedules')).json();
  const created = (schedules.items as Array<{ id: string; name: string }>).find((x) => x.name === scheduleName);
  expect(created, 'schedule created in the UI should be listed').toBeTruthy();
  await page.goto(`/schedules/${created!.id}`);

  // Build a rotation of the two participants and save it. Mantine associates the
  // label with both the input and the options listbox, so click the input (first).
  await page.getByLabel('Participants (in handoff order)').first().click();
  await page.getByRole('option', { name: `Alice ${s}`, exact: true }).click();
  await page.getByRole('option', { name: `Bob ${s}`, exact: true }).click();
  await page.keyboard.press('Escape');
  await page.getByLabel('Rotation start').first().fill(datetimeLocal(0));
  await page.getByRole('button', { name: 'Save rotation', exact: true }).click();

  // Wait for the save to have actually landed before asserting on the panels it
  // feeds. "via rotation" only appears once the stored schedule resolves the
  // current on-call through the rotation layer, so it is the operation's own
  // completion signal — the previous version raced the post-save refetch and
  // asserted on sections that were briefly unmounted.
  await expect(page.getByText('via rotation')).toBeVisible();

  // Coverage preview must render for a covered rotation.
  await expect(page.getByText('Coverage (4 weeks)')).toBeVisible();
  await expect(page.getByText('Next 4 weeks')).toBeVisible();

  // Add an override for Bob and confirm it lands in the overrides list.
  await field(page, 'User').first().click();
  await page.getByRole('option', { name: `Bob ${s}`, exact: true }).click();
  await page.getByLabel('Until').first().fill(datetimeLocal(2));
  await page.getByRole('button', { name: 'Add override', exact: true }).click();
  await expect(page.getByRole('main').getByText(`Bob ${s}`).first()).toBeVisible();

  await api.dispose();
});

// datetimeLocal returns a value for an <input type="datetime-local">, `days` from
// now, truncated to the minute.
function datetimeLocal(days: number): string {
  const d = new Date(Date.now() + days * 86_400_000);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
