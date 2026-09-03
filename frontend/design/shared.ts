import path from 'node:path';
import type { Page } from '@playwright/test';

export type ColorScheme = 'dark' | 'light';

// Everything the export produces lands here, next to the frontend it describes.
// Gitignored: these are regenerated artefacts, and a repository is the wrong
// place to accumulate 68 PNGs on every UI change.
//
// Resolved from the cwd rather than from this file: the package is ESM
// ("type": "module"), so `__dirname` is not available here, and the npm scripts
// that drive the export always run from frontend/.
export const OUT_DIR = path.resolve(process.cwd(), 'design-export');

// Where Mantine's default ColorSchemeManager keeps the user's choice. Setting
// it before any page script runs is what makes the light-theme pass real: the
// alternative — clicking the header toggle — depends on the interface language
// and on being signed in first, neither of which the export controls.
const STORAGE_KEY = 'mantine-color-scheme-value';

/**
 * Pin the colour scheme for every page this context will open.
 *
 * The caller must assert `html[data-mantine-color-scheme]` afterwards: if
 * Mantine ever changes the storage key, this silently becomes a no-op and the
 * run produces two identical dark sets, one of them labelled "light".
 */
export async function applyColorScheme(page: Page, scheme: ColorScheme): Promise<void> {
  await page.addInitScript(
    ([key, value]) => {
      window.localStorage.setItem(key, value);
    },
    [STORAGE_KEY, scheme] as const,
  );
}
