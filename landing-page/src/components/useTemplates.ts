import { useCallback, useEffect, useRef, useState } from 'react';

import type { Api } from '../api';
import type { Template } from '../api-client';

const FIRST_WAIT_MS = 1000;
const MAX_WAIT_MS = 15_000;

/**
 * Loads the templates and retries with a growing pause until it works, because the service can be away for a
 * minute (a deploy) while the page is open. `reload` asks again at once, for example after the event stream is back.
 * Only one request is in flight at a time.
 */
export function useTemplates(api: Api, failedText: string): { templates?: Template[]; error?: string; reload: () => void } {
    const [templates, setTemplates] = useState<Template[]>();
    const [error, setError] = useState<string>();
    const text = useRef(failedText);
    text.current = failedText;
    const load = useRef<() => void>(() => undefined);

    useEffect(() => {
        let alive = true;
        let inFlight = false;
        let wait = FIRST_WAIT_MS;
        let timer: ReturnType<typeof setTimeout> | undefined;
        const retryLater = (message: string): void => {
            setError(message);
            timer = setTimeout(run, wait);
            wait = Math.min(wait * 2, MAX_WAIT_MS);
        };
        function run(): void {
            if (inFlight || !alive) {
                return;
            }
            inFlight = true;
            clearTimeout(timer);
            api.client
                .GET('/templates')
                .then(({ data, error: problem }) => {
                    inFlight = false;
                    if (!alive) {
                        return;
                    }
                    if (data) {
                        wait = FIRST_WAIT_MS;
                        setTemplates(data);
                        setError(undefined);
                    } else {
                        retryLater(problem?.detail ?? problem?.title ?? text.current);
                    }
                })
                .catch(() => {
                    inFlight = false;
                    if (alive) {
                        retryLater(text.current);
                    }
                });
        }
        load.current = () => {
            wait = FIRST_WAIT_MS;
            run();
        };
        run();
        return () => {
            alive = false;
            clearTimeout(timer);
        };
    }, [api]);

    return { templates, error, reload: useCallback(() => load.current(), []) };
}
