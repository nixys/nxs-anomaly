import { test, expect, type Page } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { signIn } from '../e2e/helpers';
import { seedDemoData } from './seed';
import { OUT_DIR, applyColorScheme, type ColorScheme } from './shared';

// Photographs every screen of the product in both themes at two widths, and
// writes a manifest describing what each file is. The output is the reference
// a designer works against in Figma: the kit gives them editable components,
// this gives them the ground truth of what the product actually looks like.

const SCREENS_DIR = path.join(OUT_DIR, 'screens');

// The navigation, in navbar order (components/Shell.tsx). Kept as a literal
// list rather than parsed out of the app: a route the export forgets is a
// screen the designer never sees, so the omission should be visible here.
const ROUTES = [
  { route: '/', name: 'home' },
  { route: '/alert-groups', name: 'alert-groups' },
  { route: '/alerts', name: 'alerts' },
  { route: '/users', name: 'users' },
  { route: '/teams', name: 'teams' },
  { route: '/integrations', name: 'integrations' },
  { route: '/escalation-chains', name: 'escalation-chains' },
  { route: '/schedules', name: 'schedules' },
  { route: '/maintenance', name: 'maintenance' },
  { route: '/notifications', name: 'notifications' },
  { route: '/insights', name: 'insights' },
  { route: '/audit', name: 'audit' },
  { route: '/setup', name: 'setup' },
  { route: '/readiness', name: 'readiness' },
  { route: '/settings', name: 'settings' },
] as const;

// Detail screens have no fixed URL — they hang off whatever the seed created.
// Each is reached by following the first link of its kind on the list page,
// which is also how a person reaches it.
const DETAILS = [
  { from: '/alert-groups', hrefPrefix: '/alert-groups/', name: 'alert-group-detail' },
  { from: '/integrations', hrefPrefix: '/integrations/', name: 'integration-detail' },
  { from: '/schedules', hrefPrefix: '/schedules/', name: 'schedule-detail' },
] as const;

const VIEWPORTS = [
  { name: 'desktop', size: { width: 1440, height: 900 } },
  // Below the `sm` breakpoint the navbar collapses behind the burger — a
  // different layout, not a narrower one, and the one most often forgotten.
  { name: 'mobile', size: { width: 390, height: 844 } },
] as const;

const SCHEMES: readonly ColorScheme[] = ['dark', 'light'];

type Shot = {
  file: string;
  screen: string;
  route: string;
  scheme: ColorScheme;
  viewport: string;
  width: number;
};

const shots: Shot[] = [];

test.beforeAll(async () => {
  await mkdir(SCREENS_DIR, { recursive: true });
  await seedDemoData();
});

test.afterAll(async () => {
  // One manifest for the whole run, written once every combination is on disk.
  // A Figma plugin or an import script reads this instead of parsing filenames.
  await writeFile(
    path.join(OUT_DIR, 'screens.json'),
    `${JSON.stringify({ capturedAt: new Date().toISOString(), shots }, null, 2)}\n`,
    'utf8',
  );
});

/**
 * Screenshot the current page once it has stopped moving.
 *
 * Mantine skeletons and react-query's loading states resolve after navigation,
 * so shooting on `load` reliably captures the skeleton rather than the screen.
 * Waiting for the network to go quiet is what separates a usable reference from
 * 68 pictures of a spinner.
 */
async function shoot(page: Page, meta: Omit<Shot, 'file'>): Promise<void> {
  const file = `${meta.screen}--${meta.viewport}-${meta.scheme}.png`;
  await page.waitForLoadState('networkidle');
  await page.screenshot({ path: path.join(SCREENS_DIR, file), fullPage: true });
  shots.push({ file, ...meta });
}

for (const viewport of VIEWPORTS) {
  for (const scheme of SCHEMES) {
    test.describe(`${viewport.name} · ${scheme}`, () => {
      test.use({ viewport: viewport.size });

      test('capture every screen', async ({ page }) => {
        await applyColorScheme(page, scheme);
        await signIn(page);
        await expect(page.locator('html')).toHaveAttribute('data-mantine-color-scheme', scheme);

        const common = { scheme, viewport: viewport.name, width: viewport.size.width };

        for (const { route, name } of ROUTES) {
          await page.goto(route);
          await shoot(page, { screen: name, route, ...common });
        }

        for (const { from, hrefPrefix, name } of DETAILS) {
          await page.goto(from);
          await page.waitForLoadState('networkidle');
          // Nothing seeded for this list (schedules, for one) means no detail
          // screen exists to photograph. That is a fact about the export, not
          // a failure — record nothing and move on.
          const link = page.locator(`a[href^="${hrefPrefix}"]`).first();
          if ((await link.count()) === 0) continue;
          const href = await link.getAttribute('href');
          await link.click();
          await shoot(page, { screen: name, route: href ?? hrefPrefix, ...common });
        }
      });
    });
  }
}
