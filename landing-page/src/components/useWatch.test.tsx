import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { LOST_AFTER_MS, useWatch } from './useWatch';

afterEach(() => vi.useRealTimers());

const tick = async (ms: number): Promise<void> => {
    await act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
    });
};

describe('useWatch', () => {
    it('does not report a lost connection for a drop that the next attempt repairs', async () => {
        vi.useFakeTimers();
        let attempt = 0;
        const watch = async (on: (d: number) => void): Promise<void> => {
            attempt += 1;
            on(attempt);
            // The first stream ends like a gateway timeout, the second one stays open.
            if (attempt === 1) {
                throw new Error('stream dropped');
            }
            await new Promise<void>(() => undefined);
        };
        const { result } = renderHook(() => useWatch<number>(watch));
        await tick(0);
        expect(result.current.lost).toBe(false);
        await tick(1000);
        expect(result.current).toMatchObject({ data: 2, lost: false });
        await tick(60_000);
        expect(result.current.lost).toBe(false);
    });

    it('reports it after reconnecting failed for about ten seconds, and hides it with the next event', async () => {
        vi.useFakeTimers();
        let up = false;
        const watch = vi.fn(async (on: (d: string) => void): Promise<void> => {
            if (!up) {
                throw new Error('down');
            }
            on('back');
            await new Promise<void>(() => undefined);
        });
        const { result } = renderHook(() => useWatch<string>(watch));
        await tick(LOST_AFTER_MS - 1000);
        expect(result.current.lost).toBe(false);
        await tick(1500);
        expect(result.current.lost).toBe(true);
        up = true;
        await tick(15_000);
        expect(result.current).toMatchObject({ data: 'back', lost: false });
    });

    it('opens nothing while disabled and stops the timers on unmount', async () => {
        vi.useFakeTimers();
        const watch = vi.fn(async (): Promise<void> => {
            throw new Error('down');
        });
        const off = renderHook(() => useWatch<string>(watch, false));
        await tick(60_000);
        expect(watch).not.toHaveBeenCalled();
        off.unmount();
        const on = renderHook(() => useWatch<string>(watch));
        await tick(100);
        on.unmount();
        const calls = watch.mock.calls.length;
        await tick(120_000);
        expect(watch.mock.calls.length).toBe(calls);
    });
});
