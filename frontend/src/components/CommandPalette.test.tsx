import { MantineProvider } from '@mantine/core';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { CommandPalette } from './CommandPalette';

function LocationProbe() {
  const location = useLocation();
  return <span data-testid="path">{location.pathname}</span>;
}

function renderPalette(run = vi.fn()) {
  render(
    <MantineProvider>
      <MemoryRouter initialEntries={['/']}>
        <CommandPalette
          commands={[
            { id: 'groups', label: 'Alert groups', to: '/alert-groups', hint: 'Respond' },
            { id: 'schedules', label: 'Schedules', to: '/schedules', hint: 'On call' },
            { id: 'theme', label: 'Toggle theme', run },
          ]}
        />
        <LocationProbe />
      </MemoryRouter>
    </MantineProvider>,
  );
  return run;
}

describe('CommandPalette', () => {
  it('stays out of the way until ⌘K', async () => {
    const user = userEvent.setup();
    renderPalette();
    expect(screen.queryByPlaceholderText(/Go to a page/i)).toBeNull();

    await user.keyboard('{Meta>}k{/Meta}');
    expect(await screen.findByPlaceholderText(/Go to a page/i)).toBeInTheDocument();
  });

  it('narrows to what was typed and opens it with Enter', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.keyboard('{Control>}k{/Control}');
    const input = await screen.findByPlaceholderText(/Go to a page/i);

    await user.type(input, 'sched');
    expect(screen.queryByText('Alert groups')).toBeNull();

    await user.keyboard('{Enter}');
    await waitFor(() => expect(screen.getByTestId('path').textContent).toBe('/schedules'));
  });

  it('runs a command that is an action rather than a destination', async () => {
    const user = userEvent.setup();
    const run = renderPalette();
    await user.keyboard('{Control>}k{/Control}');
    const input = await screen.findByPlaceholderText(/Go to a page/i);

    await user.type(input, 'theme');
    await user.keyboard('{Enter}');
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('says so when nothing matches instead of showing an empty box', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.keyboard('{Control>}k{/Control}');
    await user.type(await screen.findByPlaceholderText(/Go to a page/i), 'zzz');
    expect(await screen.findByText('Nothing matches')).toBeInTheDocument();
  });
});
