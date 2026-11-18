import { afterEach, expect, it, vi } from 'vitest';
import { api } from './client';

afterEach(() => vi.unstubAllGlobals());

it('forwards cancellation to the actual HTTP request', async () => {
  const controller = new AbortController();
  const fetchMock = vi.fn((_url: string, init: RequestInit) => new Promise<Response>((_resolve, reject) => {
    init.signal?.addEventListener('abort', () => reject(new DOMException('Cancelled', 'AbortError')));
  }));
  vi.stubGlobal('fetch', fetchMock);
  const pending = api.get('/api/v1/users', { limit: 1000 }, controller.signal);
  controller.abort();
  await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/users?limit=1000', expect.objectContaining({ signal: controller.signal }));
});
