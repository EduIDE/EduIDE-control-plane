import { useEffect, useState } from 'react';

import type { Api } from '../api';
import type { Overview } from '../api-client';

/** Follows the overview stream: it starts with the current state, and reconnects with a growing pause when it drops. */
export function useOverview(api: Api): { overview?: Overview; lost: boolean } {
    const [overview, setOverview] = useState<Overview>();
    const [lost, setLost] = useState(false);

    useEffect(() => {
        const controller = new AbortController();
        let wait = 1000;
        const follow = async (): Promise<void> => {
            while (!controller.signal.aborted) {
                try {
                    await api.watchOverview(o => {
                        wait = 1000;
                        setLost(false);
                        setOverview(o);
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
    }, [api]);

    return { overview, lost };
}
