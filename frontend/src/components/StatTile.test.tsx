import { MantineProvider } from '@mantine/core';
import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { StatTile } from './StatTile';

// The home page tiles showed "—" for everyone who had not asked the OS for
// reduced motion: the count-up started from nothing, found nothing to animate
// and never set the number that had arrived.
describe('StatTile', () => {
  it('shows the first number that arrives after loading', () => {
    const tile = (value: number | undefined, loading: boolean) => (
      <MantineProvider>
        <StatTile label="Open" value={value} loading={loading} />
      </MantineProvider>
    );
    const { rerender } = render(tile(undefined, true));
    rerender(tile(20, false));
    expect(screen.getByText('20')).toBeInTheDocument();
    expect(screen.queryByText('—')).not.toBeInTheDocument();
  });
});
