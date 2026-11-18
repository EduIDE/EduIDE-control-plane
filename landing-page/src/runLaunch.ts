import type { LaunchRequest, Session } from './api-client';
import { applySession, failureFromProblem, isFinal, type LaunchState } from './launch';

export interface LaunchDeps {
    launch: (request: LaunchRequest) => Promise<{ session?: Session; problem?: Parameters<typeof failureFromProblem>[0] }>;
    /** Streams session events until the server closes the stream. */
    watch: (name: string, onSession: (s: Session) => void, signal: AbortSignal) => Promise<void>;
    delay?: (ms: number) => Promise<void>;
}

const MAX_RECONNECTS = 5;

/** Creates the session and follows its events until Ready or Failed, reconnecting when the stream drops. */
export async function runLaunch(
    deps: LaunchDeps,
    request: LaunchRequest,
    onState: (s: LaunchState) => void,
    signal: AbortSignal
): Promise<void> {
    const delay = deps.delay ?? (ms => new Promise(resolve => setTimeout(resolve, ms)));
    onState({ kind: 'creating' });
    let created;
    try {
        created = await deps.launch(request);
    } catch {
        if (!signal.aborted) {
            onState({ kind: 'failed', code: 'internal', message: 'The service could not be reached.' });
        }
        return;
    }
    if (!created.session) {
        onState(failureFromProblem(created.problem));
        return;
    }
    let state = applySession(created.session);
    onState(state);
    for (let attempt = 0; !isFinal(state) && !signal.aborted; attempt += 1) {
        if (attempt > MAX_RECONNECTS) {
            onState({ kind: 'failed', code: 'internal', message: 'Lost the connection to the service.' });
            return;
        }
        try {
            await deps.watch(
                created.session.name,
                s => {
                    state = applySession(s);
                    onState(state);
                },
                signal
            );
        } catch {
            if (signal.aborted) {
                return;
            }
        }
        if (!isFinal(state) && !signal.aborted) {
            await delay(Math.min(1000 * (attempt + 1), 5000));
        }
    }
}
