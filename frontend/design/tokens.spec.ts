import { test, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { OUT_DIR, applyColorScheme, type ColorScheme } from './shared';

// Reads the design tokens out of the running application rather than out of
// Mantine's documentation, and writes them in the shape Tokens Studio for
// Figma imports.
//
// The distinction matters: the documented defaults are what Mantine ships, the
// computed variables are what this installation actually renders, including
// anything createTheme() overrides now or later. A designer importing this file
// gets Figma Variables that cannot drift from the product by construction.
//
// No sign-in: the login screen already mounts MantineProvider, so every
// variable is present on `:root` before anyone authenticates.

const SCHEMES: readonly ColorScheme[] = ['dark', 'light'];

// Mantine names its variables `--mantine-<category>-<name>`. Mapping the
// category to a Tokens Studio type is what makes the import land as colours,
// spacing and radii instead of 400 anonymous strings.
const CATEGORIES: ReadonlyArray<{ prefix: string; group: string; type: string }> = [
  { prefix: 'color-', group: 'color', type: 'color' },
  { prefix: 'spacing-', group: 'spacing', type: 'spacing' },
  { prefix: 'radius-', group: 'radius', type: 'borderRadius' },
  { prefix: 'font-size-', group: 'fontSize', type: 'fontSizes' },
  { prefix: 'line-height-', group: 'lineHeight', type: 'lineHeights' },
  { prefix: 'font-family-', group: 'fontFamily', type: 'fontFamilies' },
  { prefix: 'font-family', group: 'fontFamily', type: 'fontFamilies' },
  { prefix: 'shadow-', group: 'shadow', type: 'boxShadow' },
  { prefix: 'breakpoint-', group: 'breakpoint', type: 'sizing' },
];

type Token = { value: string; type: string };
type TokenSet = Record<string, Record<string, Token>>;

function classify(name: string): { group: string; key: string; type: string } {
  const bare = name.replace('--mantine-', '');
  for (const { prefix, group, type } of CATEGORIES) {
    if (bare.startsWith(prefix)) {
      // `--mantine-font-family` itself has no suffix; call it "default".
      const key = bare.slice(prefix.length) || 'default';
      return { group, key, type };
    }
  }
  // Headings (`--mantine-h1-font-size`), z-indexes and anything Mantine adds
  // later. Kept rather than dropped: a designer would rather see an unsorted
  // token than silently not have it.
  return { group: 'other', key: bare, type: 'other' };
}

test('export theme tokens', async ({ page }) => {
  await mkdir(OUT_DIR, { recursive: true });
  const sets: Record<string, TokenSet> = {};

  for (const scheme of SCHEMES) {
    await applyColorScheme(page, scheme);
    await page.goto('/');
    await expect(page.locator('html')).toHaveAttribute('data-mantine-color-scheme', scheme);

    const raw = await page.evaluate(() => {
      // Custom property names are not enumerable from getComputedStyle, so they
      // have to be harvested from the stylesheets first. Mantine's CSS is
      // same-origin, so cssRules is readable; the try/catch covers anything a
      // future dependency loads from a CDN, where the access throws.
      const names = new Set<string>();
      const walk = (rules: CSSRuleList) => {
        for (const rule of Array.from(rules)) {
          const style = (rule as CSSStyleRule).style;
          if (style) {
            for (const prop of Array.from(style)) {
              if (prop.startsWith('--mantine-')) names.add(prop);
            }
          }
          const nested = (rule as CSSGroupingRule).cssRules;
          if (nested) walk(nested);
        }
      };
      for (const sheet of Array.from(document.styleSheets)) {
        try {
          walk(sheet.cssRules);
        } catch {
          continue;
        }
      }
      const computed = getComputedStyle(document.documentElement);
      const out: Record<string, string> = {};
      for (const name of names) {
        const value = computed.getPropertyValue(name).trim();
        if (value) out[name] = value;
      }
      return out;
    });

    const count = Object.keys(raw).length;
    // A theme with no variables means the harvest silently failed — better to
    // stop than to hand a designer an empty token file that imports cleanly.
    expect(count, `no --mantine-* variables found in the ${scheme} theme`).toBeGreaterThan(50);

    const set: TokenSet = {};
    for (const [name, value] of Object.entries(raw)) {
      const { group, key, type } = classify(name);
      (set[group] ??= {})[key] = { value, type };
    }
    sets[`mantine-${scheme}`] = set;
    console.log(`${scheme}: ${count} tokens`);
  }

  // Tokens Studio's native JSON shape (`value`/`type`, one object per set),
  // which its "import from file" reads directly. $metadata fixes the order the
  // sets appear in, so light never shadows dark by accident.
  const document_ = {
    ...sets,
    $metadata: { tokenSetOrder: Object.keys(sets) },
  };
  await writeFile(
    path.join(OUT_DIR, 'tokens.json'),
    `${JSON.stringify(document_, null, 2)}\n`,
    'utf8',
  );
});
