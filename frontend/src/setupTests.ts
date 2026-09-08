import '@testing-library/jest-dom/vitest';

// jsdom implements neither of these, and Mantine's responsive components call
// both on mount; without them every component test throws before it renders.
window.matchMedia ??= ((query: string) => ({
  matches: false,
  media: query,
  onchange: null,
  addListener: () => {},
  removeListener: () => {},
  addEventListener: () => {},
  removeEventListener: () => {},
  dispatchEvent: () => false,
})) as typeof window.matchMedia;

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

// jsdom has no layout, so it implements no scrolling either: Element.scrollIntoView
// does not exist. Two things call it during these tests — the alert-group list,
// keeping the keyboard cursor in view, and Mantine's Combobox, keeping the
// highlighted option in view — and Mantine's call happens on a timer after the
// test that triggered it has finished. The throw then lands outside any test:
// every test passes, vitest reports unhandled errors and exits non-zero, which
// is exactly how this reached CI green-looking and failed there.
Element.prototype.scrollIntoView ??= () => {};
