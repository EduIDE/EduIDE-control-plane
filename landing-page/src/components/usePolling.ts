import { useEffect, useRef, useState } from 'react';

interface Polling<T> {
    data?: T;
    /** The last load failed. Data from an earlier load stays. */
    failed: boolean;
    /** What the last failed load threw, so a page can tell "not found" from "unreachable". */
    error?: unknown;
    loading: boolean;
}

/**
 * Loads when it starts and then every `ms`. A failed load keeps the last data and sets `failed`.
 * `deps` name the request (filters, selected item): when one changes, data and failure are cleared first,
 * so a view never shows the answer to the previous request. With `enabled` false nothing loads or ticks.
 */
export function usePolling<T>(load: () => Promise<T>, ms = 30_000, deps: unknown[] = [], enabled = true): Polling<T> {
    const [state, setState] = useState<Polling<T>>({ failed: false, loading: true });
    const loadRef = useRef(load);
    loadRef.current = load;

    useEffect(() => {
        if (!enabled) {
            return;
        }
        let cancelled = false;
        setState({ failed: false, loading: true });
        const run = async (): Promise<void> => {
            try {
                const next = await loadRef.current();
                if (!cancelled) {
                    setState({ data: next, failed: false, loading: false });
                }
            } catch (error) {
                if (!cancelled) {
                    setState(s => ({ ...s, failed: true, error, loading: false }));
                }
            }
        };
        run().catch(console.error);
        const id = setInterval(() => run().catch(console.error), ms);
        return () => {
            cancelled = true;
            clearInterval(id);
        };
        // The caller lists what restarts the polling (filters), the load function itself is read through the ref.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [ms, enabled, ...deps]);

    return state;
}
