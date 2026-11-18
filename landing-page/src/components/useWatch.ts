import { useEffect, useState } from 'react';

/** Follows an event stream: it starts with the current state, and reconnects with a growing pause when it drops. */
export function useWatch<T>(watch: (on: (data: T) => void, signal: AbortSignal) => Promise<void>): { data?: T; lost: boolean } {
    const [data, setData] = useState<T>();
    const [lost, setLost] = useState(false);

    useEffect(() => {
        const controller = new AbortController();
        let wait = 1000;
        const follow = async (): Promise<void> => {
            while (!controller.signal.aborted) {
                try {
                    await watch(d => {
                        wait = 1000;
                        setLost(false);
                        setData(d);
                    }, controller.signal);
                } catch {
                    if (controller.signal.aborted) {
                        return;
                    }
                    setLost(true);
                }
                await new Promise(resolve => setTimeout(resolve, wait));
                wait = Math.min(wait * 2, 15000);
            }
        };
        follow().catch(console.error);
        return () => controller.abort();
        // The watch function belongs to the api object, which lives as long as the page.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    return { data, lost };
}
