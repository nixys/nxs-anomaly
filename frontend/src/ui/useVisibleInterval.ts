import { useEffect, useState } from 'react';

/**
 * A polling interval that stops while the tab is hidden.
 *
 * Every list in this product refetches on a timer so a responder never reads a
 * stale queue. Left running in a background tab that is exactly a request every
 * fifteen seconds, per open tab, forever — against an installation whose whole
 * job is to stay responsive during an incident. Returning `false` is what
 * TanStack Query reads as "do not poll"; the query refetches on its own when the
 * tab comes back.
 */
export function useVisibleInterval(ms: number): number | false {
  const [visible, setVisible] = useState(() =>
    typeof document === 'undefined' ? true : !document.hidden,
  );

  useEffect(() => {
    const onChange = () => setVisible(!document.hidden);
    document.addEventListener('visibilitychange', onChange);
    return () => document.removeEventListener('visibilitychange', onChange);
  }, []);

  return visible ? ms : false;
}
