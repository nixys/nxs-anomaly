// The product mark: a bell whose clapper frame draws an A.
//
// Served from /logo.png rather than inlined, so the browser caches it once for
// the header, the sign-in screen and anywhere else it appears, and so the file
// stays the single copy that /public owns. It is the mark alone — every place
// that shows it already sets the product name in text beside it, and a lockup
// carrying its own wordmark would print the name twice.
//
// The source assets are in frontend/logo/; frontend/public/logo.png is derived
// from logo-full.png (cropped to the mark and re-padded square), because the
// original is 1600x1600 and 1.1 MB.
export function Logo({ size = 28 }: { size?: number }) {
  return (
    <img
      src="/logo.png"
      width={size}
      height={size}
      alt=""
      // Decorative: the product name is beside it as real text, so a screen
      // reader announcing the mark as well would just repeat it.
      aria-hidden
      style={{ display: 'block', objectFit: 'contain' }}
    />
  );
}
