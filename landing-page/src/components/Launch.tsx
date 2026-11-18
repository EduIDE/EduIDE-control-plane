import { useCallback, useEffect, useState } from 'react';

import type { Api } from '../api';
import type { LaunchRequest } from '../api-client';
import { clearArtemisToken } from '../artemisToken';
import type { LaunchState } from '../launch';
import { runLaunch } from '../runLaunch';
import { ErrorComponent } from './ErrorComponent';
import { Loading } from './Loading';

interface LaunchProps {
    api: Api;
    request: LaunchRequest;
    onBack: () => void;
}

/** Starts the session, shows progress and redirects when it is ready. */
export const Launch = ({ api, request, onBack }: LaunchProps): React.JSX.Element => {
    const [state, setState] = useState<LaunchState>({ kind: 'creating' });
    const [attempt, setAttempt] = useState(0);

    useEffect(() => {
        const controller = new AbortController();
        runLaunch(api.launchDeps, request, setState, controller.signal).catch(console.error);
        return () => controller.abort();
    }, [api, request, attempt]);

    useEffect(() => {
        if (state.kind === 'ready') {
            try {
                clearArtemisToken(window.sessionStorage);
            } catch {
                // Blocked storage holds no token either.
            }
            window.location.replace(state.url);
        }
    }, [state]);

    const stopAndRetry = useCallback(
        async (name: string): Promise<void> => {
            await api.client.DELETE('/sessions/{name}', { params: { path: { name } } });
            setAttempt(a => a + 1);
        },
        [api]
    );

    if (state.kind === 'failed') {
        return (
            <>
                <ErrorComponent message={`${state.message} (${state.code})`} />
                {state.sessions && (
                    <ul className='Overview__list'>
                        {state.sessions.map(s => (
                            <li key={s.name} className='Overview__row'>
                                <span className='Overview__title'>{s.display?.title || s.template}</span>
                                {s.url && (
                                    <a className='App__try-now-button' href={s.url}>
                                        Open
                                    </a>
                                )}
                                <button className='App__try-now-button' onClick={() => stopAndRetry(s.name).catch(console.error)}>
                                    Stop and retry
                                </button>
                            </li>
                        ))}
                    </ul>
                )}
                <div className='App__session-ended-actions'>
                    <button className='App__try-now-button' onClick={() => setAttempt(a => a + 1)}>
                        Try again
                    </button>
                    <button className='App__try-now-button' onClick={onBack}>
                        Back
                    </button>
                </div>
            </>
        );
    }
    const text = state.kind === 'starting' && state.session.startStrategy === 'warm-ready' ? 'Almost there...' : undefined;
    return <Loading text={text} />;
};
