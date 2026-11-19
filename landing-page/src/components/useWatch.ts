import { useEffect, useState } from 'react';

/** The gateway drops event streams now and then and the next attempt succeeds, so a short gap is no news. */
export const LOST_AFTER_MS = 10_000;

/**
 * Follows an event stream: it starts with the current state, and reconnects with a growing pause when it drops.
 * `lost` turns true only when reconnecting has failed for `LOST_AFTER_MS`, and turns false with the next event.
 */
export function useWatch<T>(
    watch: (on: (data: T) => void, signal: AbortSignal) => Promise<void>,
    enabled = true
): { data?: T; lost: boolean } {
    const [data, setData] = useState<T>();
    const [lost, setLost] = useState(false);

    useEffect(() => {
        if (!enabled) {
            return;
        }
        const controller = new AbortController();
        let wait = 1000;
        let lostTimer: ReturnType<typeof setTimeout> | undefined;
        const clearLostTimer = (): void => {
            clearTimeout(lostTimer);
            lostTimer = undefined;
        };
        const follow = async (): Promise<void> => {
            while (!controller.signal.aborted) {
                try {
                    await watch(d => {
                        wait = 1000;
                        clearLostTimer();
                        setLost(false);
                        setData(d);
                    }, controller.signal);
                } catch {
                    if (controller.signal.aborted) {
                        return;
                    }
                }
                if (controller.signal.aborted) {
                    return;
                }
                lostTimer ??= setTimeout(() => setLost(true), LOST_AFTER_MS);
                await new Promise(resolve => setTimeout(resolve, wait));
                wait = Math.min(wait * 2, 15000);
            }
        };
        follow().catch(console.error);
        return () => {
            controller.abort();
            clearLostTimer();
        };
        // The watch function belongs to the api object, which lives as long as the page.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [enabled]);

    return { data, lost };
}
