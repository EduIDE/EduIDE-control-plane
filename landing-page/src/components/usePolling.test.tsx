import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { usePolling } from './usePolling';

afterEach(() => vi.useRealTimers());

describe('usePolling', () => {
    it('clears data and failure when the request changes, and ignores the answer to the old one', async () => {
        vi.useFakeTimers();
        let resolveOld: (v: string) => void = () => undefined;
        const loads: Record<string, () => Promise<string>> = {
            a: async () => 'answer a',
            b: () => new Promise(resolve => (resolveOld = resolve))
        };
        const { result, rerender } = renderHook(({ key }) => usePolling(loads[key], 30_000, [key]), { initialProps: { key: 'a' } });
        await act(async () => {
            await vi.advanceTimersByTimeAsync(0);
        });
        expect(result.current.data).toBe('answer a');
        rerender({ key: 'b' });
        expect(result.current.data).toBeUndefined();
        expect(result.current.loading).toBe(true);
        rerender({ key: 'a' });
        await act(async () => {
            resolveOld('late answer b');
            await vi.advanceTimersByTimeAsync(0);
        });
        expect(result.current.data).toBe('answer a');
    });

    it('drops the failure with the request, but keeps data across a failed refresh of the same request', async () => {
        vi.useFakeTimers();
        let fail = false;
        const load = async (): Promise<string> => {
            if (fail) {
                throw new Error('down');
            }
            return 'ok';
        };
        const { result, rerender } = renderHook(({ key }) => usePolling(load, 30_000, [key]), { initialProps: { key: 'a' } });
        await act(async () => {
            await vi.advanceTimersByTimeAsync(0);
        });
        fail = true;
        await act(async () => {
            await vi.advanceTimersByTimeAsync(30_000);
        });
        expect(result.current).toMatchObject({ data: 'ok', failed: true });
        rerender({ key: 'b' });
        expect(result.current.failed).toBe(false);
        expect(result.current.data).toBeUndefined();
    });

    it('does nothing while disabled', async () => {
        vi.useFakeTimers();
        const load = vi.fn(async () => 'x');
        const { rerender } = renderHook(({ on }) => usePolling(load, 30_000, [], on), { initialProps: { on: false } });
        await vi.advanceTimersByTimeAsync(90_000);
        expect(load).not.toHaveBeenCalled();
        rerender({ on: true });
        await act(async () => {
            await vi.advanceTimersByTimeAsync(0);
        });
        expect(load).toHaveBeenCalledTimes(1);
    });
});
