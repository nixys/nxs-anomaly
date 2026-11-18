import { MantineProvider } from '@mantine/core';
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { HomePage } from './HomePage';

const get = vi.fn();
vi.mock('../api/client', () => ({ api: { get: (...args: unknown[]) => get(...args) } }));
vi.mock('../components/StatTile', () => ({
  StatTile: ({ label, value }: { label: string; value?: number }) => <div>{label}<output>{value}</output></div>,
}));

afterEach(() => {
  vi.useRealTimers();
  focusManager.setFocused(undefined);
});

it('updates all four incident and delivery totals while the overview stays open', async () => {
  vi.useFakeTimers();
  focusManager.setFocused(true);
  let total = 1;
  let fail = false;
  get.mockImplementation(() => fail ? Promise.reject(new Error('offline')) : Promise.resolve({ open: { items: [], total }, acknowledged: { items: [], total }, failed: { items: [], total }, retrying: { items: [], total }, integrations: { items: [], total: 0 }, on_call: { items: [] } }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const { unmount } = render(
    <MantineProvider env="test">
      <QueryClientProvider client={client}>
        <MemoryRouter><HomePage /></MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
  await act(() => vi.advanceTimersByTimeAsync(100));
  expect(screen.getAllByText('1', { selector: 'output' })).toHaveLength(4);
  total = 7;
  await act(() => vi.advanceTimersByTimeAsync(15_100));
  expect(screen.getAllByText('7', { selector: 'output' })).toHaveLength(4);
  expect(get.mock.calls.every(call => call[0] === '/api/v1/overview')).toBe(true);
  fail = true;
  await act(() => vi.advanceTimersByTimeAsync(15_100));
  expect(screen.getAllByText('7', { selector: 'output' })).toHaveLength(4);
  expect(screen.getByRole('button', { name: /retry/i })).toBeVisible();
  unmount();
  client.clear();
});
