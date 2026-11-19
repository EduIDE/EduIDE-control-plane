import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Api } from '../api';
import type { Template } from '../api-client';
import { useTemplates } from './useTemplates';

afterEach(() => vi.useRealTimers());

const java = { name: 'java-17', displayName: 'Java 17' } as Template;
const tick = async (ms: number): Promise<void> => {
    await act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
    });
};
const unavailable = { response: { status: 503 }, error: { title: 'Service Unavailable', status: 503 } };

const apiWith = (get: ReturnType<typeof vi.fn>): Api => ({ client: { GET: get } }) as unknown as Api;

describe('useTemplates', () => {
    it('retries with a growing pause, then fills the templates and clears the error', async () => {
        vi.useFakeTimers();
        const get = vi
            .fn()
            .mockResolvedValueOnce(unavailable)
            .mockResolvedValueOnce(unavailable)
            .mockResolvedValue({ data: [java], response: { status: 200 } });
        const api = apiWith(get);
        const { result } = renderHook(() => useTemplates(api, 'fallback'));
        await tick(0);
        expect(result.current).toMatchObject({ error: 'Service Unavailable', templates: undefined });
        expect(get).toHaveBeenCalledTimes(1);
        await tick(1000);
        expect(get).toHaveBeenCalledTimes(2);
        await tick(1999);
        expect(get).toHaveBeenCalledTimes(2);
        await tick(1);
        expect(get).toHaveBeenCalledTimes(3);
        expect(result.current.templates).toEqual([java]);
        expect(result.current.error).toBeUndefined();
        await tick(60_000);
        expect(get).toHaveBeenCalledTimes(3);
    });

    it('caps the pause at 15 seconds', async () => {
        vi.useFakeTimers();
        const get = vi.fn().mockResolvedValue(unavailable);
        const api = apiWith(get);
        renderHook(() => useTemplates(api, 'fallback'));
        await tick(0);
        // 1 + 2 + 4 + 8 = 15 s for the first four retries, then every 15 s.
        await tick(15_000);
        expect(get).toHaveBeenCalledTimes(5);
        await tick(15_000);
        expect(get).toHaveBeenCalledTimes(6);
    });

    it('uses the fallback text when the request itself fails', async () => {
        vi.useFakeTimers();
        const get = vi.fn().mockRejectedValue(new Error('network'));
        const api = apiWith(get);
        const { result } = renderHook(() => useTemplates(api, 'Could not load the environments.'));
        await tick(0);
        expect(result.current.error).toBe('Could not load the environments.');
    });

    it('never has two requests in flight, also when reload is called meanwhile', async () => {
        vi.useFakeTimers();
        let release: (v: unknown) => void = () => undefined;
        const get = vi.fn(() => new Promise(resolve => (release = resolve)));
        const api = apiWith(get);
        const { result } = renderHook(() => useTemplates(api, 'fallback'));
        await tick(0);
        act(() => result.current.reload());
        act(() => result.current.reload());
        expect(get).toHaveBeenCalledTimes(1);
        await act(async () => release({ data: [java], response: { status: 200 } }));
        expect(result.current.templates).toEqual([java]);
    });

    it('reload asks again at once and restarts the pause', async () => {
        vi.useFakeTimers();
        const get = vi
            .fn()
            .mockResolvedValueOnce(unavailable)
            .mockResolvedValueOnce(unavailable)
            .mockResolvedValue({ data: [java], response: { status: 200 } });
        const api = apiWith(get);
        const { result } = renderHook(() => useTemplates(api, 'fallback'));
        await tick(0);
        await act(async () => result.current.reload());
        expect(get).toHaveBeenCalledTimes(2);
        await tick(1000);
        expect(get).toHaveBeenCalledTimes(3);
        expect(result.current.templates).toEqual([java]);
    });

    it('stops retrying when it unmounts', async () => {
        vi.useFakeTimers();
        const get = vi.fn().mockResolvedValue(unavailable);
        const api = apiWith(get);
        const { unmount } = renderHook(() => useTemplates(api, 'fallback'));
        await tick(0);
        unmount();
        await vi.advanceTimersByTimeAsync(120_000);
        expect(get).toHaveBeenCalledTimes(1);
    });
});
