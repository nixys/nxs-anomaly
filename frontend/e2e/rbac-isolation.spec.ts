import { test, expect, type APIRequestContext } from '@playwright/test';
import { adminApi, signIn, uniqueSuffix } from './helpers';

// Browser coverage: role-based access and cross-team isolation as an operator
// actually experiences them in the browser.
//   - A viewer does not get the admin-only Audit section; an admin does.
//   - A team-scoped user sees only their team's schedules, not another team's.

// createSignInUser makes a user that can sign in: a role plus a password.
async function createSignInUser(
  api: APIRequestContext,
  opts: { name: string; username: string; role: string; password: string },
): Promise<{ id: string }> {
  const user = await (
    await api.post('/api/v1/users', { data: { name: opts.name, username: opts.username, role: opts.role } })
  ).json();
  const pw = await api.put(`/api/v1/users/${user.id}/password`, { data: { password: opts.password } });
  expect(pw.ok(), `set password failed: ${pw.status()}`).toBeTruthy();
  return user;
}

test('an admin sees the admin-only Audit section', async ({ page }) => {
  await signIn(page);
  await expect(page.getByRole('navigation').getByRole('link', { name: 'Audit' })).toBeVisible();
});

test('a viewer is denied the admin-only Audit section', async ({ page }) => {
  const s = uniqueSuffix();
  const password = 'viewer-pass-1234';
  const api = await adminApi();
  await createSignInUser(api, { name: `Viewer ${s}`, username: `viewer-${s}`, role: 'viewer', password });
  await api.dispose();

  await signIn(page, `viewer-${s}`, password);
  await expect(page.getByRole('navigation')).toBeVisible();
  await expect(page.getByRole('navigation').getByRole('link', { name: 'Audit' })).toHaveCount(0);
});

// NXS_ANOMALY_TEAM_SCOPING enforcement (internal/engine/scope.go,
// internal/server/auth.go's applyTeamScope) is not gated by edition — only its
// *advertisement* is (editionHasTeamScoping, read by /health and
// /api/v1/capabilities, says the feature is not officially supported in the
// community build, not that the code refuses to run it). So this scenario
// holds in both editions as long as the harness sets the env var, which
// frontend/e2e/start-api.sh does unconditionally — verified by reading
// scope.go and auth.go rather than assumed from the README's edition table.
test('a team-scoped user sees only their own team\'s schedules', async ({ page }) => {
  const s = uniqueSuffix();
  const password = 'editor-pass-1234';
  const api = await adminApi();

  const editor = await createSignInUser(api, {
    name: `Editor ${s}`,
    username: `editor-${s}`,
    role: 'editor',
    password,
  });
  // Team membership is stored as member_ids (see engine CreateTeam); the editor
  // belongs to team A only.
  const teamA = await (await api.post('/api/v1/teams', { data: { name: `team-a-${s}`, member_ids: [editor.id] } })).json();
  const teamB = await (await api.post('/api/v1/teams', { data: { name: `team-b-${s}`, member_ids: [] } })).json();
  const mineName = `sched-mine-${s}`;
  const theirsName = `sched-theirs-${s}`;
  await api.post('/api/v1/schedules', { data: { name: mineName, timezone: 'UTC', team_id: teamA.id } });
  await api.post('/api/v1/schedules', { data: { name: theirsName, timezone: 'UTC', team_id: teamB.id } });
  await api.dispose();

  await signIn(page, `editor-${s}`, password);
  await page.goto('/schedules');
  const main = page.getByRole('main');
  await expect(main.getByText(mineName, { exact: true })).toBeVisible();
  await expect(main.getByText(theirsName, { exact: true })).toHaveCount(0);
});
