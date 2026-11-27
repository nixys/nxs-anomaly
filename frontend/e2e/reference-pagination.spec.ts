import { test, expect } from '@playwright/test';
import { adminApi, signIn, submitModal, uniqueSuffix } from './helpers';

test('remote references search beyond the first page and retain selected names', async ({ page }) => {
  const api = await adminApi();
  const suffix = uniqueSuffix();
  const ids: string[] = [];
  let teamId: string | undefined;
  try {
    for (let i = 0; i < 61; i++) {
      const response = await api.post('/api/v1/users', { data: { name: `Reference ${suffix} ${i}`, username: `ref-${suffix}-${i}`, email: `ref-${suffix}-${i}@example.org` } });
      expect(response.ok()).toBeTruthy();
      ids.push((await response.json()).id);
    }
    const name = `Reference ${suffix} 0`;
    const teamName = `Reference team ${suffix}`;
    const created = await api.post('/api/v1/teams', { data: { name: teamName, member_ids: [ids[0]] } });
    expect(created.ok()).toBeTruthy();
    teamId = (await created.json()).id;
    await signIn(page);
    await page.goto('/users');
    await page.getByPlaceholder('Search by name').fill(`ref-${suffix}-0@example.org`);
    await expect(page.getByRole('main').getByText(name, { exact: true })).toBeVisible();
    await expect(page.locator('tbody tr')).toHaveCount(1);
    await page.goto('/teams');
    const row = page.getByRole('row').filter({ hasText: teamName });
    await row.getByRole('button', { name: `Edit ${teamName}` }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText(name, { exact: true })).toBeVisible();
    const members = dialog.getByRole('combobox', { name: 'Members', exact: true });
    await members.click();
    await members.fill(`ref-${suffix}-60@example.org`);
    await expect(members).toHaveValue(`ref-${suffix}-60@example.org`);
    await page.getByRole('option', { name: `Reference ${suffix} 60`, exact: true }).click();
    await page.keyboard.press('Escape');
    await submitModal(page, 'Save');
    const saved = await (await api.get(`/api/v1/teams/${teamId}`)).json();
    expect(saved.member_ids).toEqual(expect.arrayContaining([ids[0], ids[60]]));
  } finally {
    if (teamId) await api.delete(`/api/v1/teams/${teamId}`);
    for (const id of ids) await api.delete(`/api/v1/users/${id}`);
    await api.dispose();
  }
});

test('real pages fit desktop and phone viewports without page overflow', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  await signIn(page);
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    for (const route of ['/', '/users', '/teams', '/integrations', '/alert-groups', '/setup']) {
      await page.goto(route);
      await expect(page.getByRole('main')).toBeVisible();
      await page.waitForLoadState('networkidle');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${route} at ${width}`).toBe(true);
      if (route === '/setup') {
        const clipped = await page.locator('main .mantine-Button-label, main .mantine-Badge-label').evaluateAll(elements => elements.filter(el => el.clientWidth > 0 && el.scrollWidth > el.clientWidth + 1).map(el => el.textContent));
        expect(clipped, `setup action labels at ${width}`).toEqual([]);
      }
    }
    await page.screenshot({ path: `test-results/audit-${width}.png`, fullPage: true });
  }
  expect(errors).toEqual([]);
});
