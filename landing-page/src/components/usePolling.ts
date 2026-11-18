import { useEffect, useRef, useState } from 'react';

/** Loads on mount and then every `ms`. A failed load keeps the last data and sets `failed`. */
export function usePolling<T>(load: () => Promise<T>, ms = 30_000, deps: unknown[] = []): { data?: T; failed: boolean; loading: boolean } {
    const [data, setData] = useState<T>();
    const [failed, setFailed] = useState(false);
    const [loading, setLoading] = useState(true);
    const loadRef = useRef(load);
    loadRef.current = load;

    useEffect(() => {
        let cancelled = false;
        const run = async (): Promise<void> => {
            try {
                const next = await loadRef.current();
                if (!cancelled) {
                    setData(next);
                    setFailed(false);
                }
            } catch {
                if (!cancelled) {
                    setFailed(true);
                }
            } finally {
                if (!cancelled) {
                    setLoading(false);
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
    }, [ms, ...deps]);

    return { data, failed, loading };
}
