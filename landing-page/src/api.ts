import { createControlPlaneClient, type Overview, readEvents, type Session } from './api-client';
import type { AdminSessionList, AdminStatus } from './apiTypes';
import { newTraceparent } from './launch';
import type { LaunchDeps } from './runLaunch';

export interface Api {
    client: ReturnType<typeof createControlPlaneClient>;
    launchDeps: LaunchDeps;
    watchOverview: (onOverview: (o: Overview) => void, signal: AbortSignal) => Promise<void>;
    watchAdminStatus: (on: (s: AdminStatus) => void, signal: AbortSignal) => Promise<void>;
    watchAdminSessions: (on: (s: AdminSessionList) => void, signal: AbortSignal) => Promise<void>;
}

export function createApi(baseUrl: string, token: () => Promise<string>): Api {
    const client = createControlPlaneClient(baseUrl, token);
    const stream = async <T>(path: string, event: string, onData: (d: T) => void, signal: AbortSignal): Promise<void> =>
        readEvents<T>(`${baseUrl}${path}`, await token(), (e, d) => e === event && onData(d), signal);

    const launchDeps: LaunchDeps = {
        launch: async request => {
            const { data, error } = await client.POST('/sessions', {
                body: request,
                params: { header: { traceparent: newTraceparent() } }
            });
            return { session: data, problem: error };
        },
        watch: (name, onSession, signal) => stream<Session>(`/sessions/${name}/events`, 'session', onSession, signal)
    };
    return {
        client,
        launchDeps,
        watchOverview: (onOverview: (o: Overview) => void, signal: AbortSignal) =>
            stream<Overview>('/me/overview/events', 'overview', onOverview, signal),
        watchAdminStatus: (on, signal) => stream<AdminStatus>('/admin/status/events', 'status', on, signal),
        watchAdminSessions: (on, signal) => stream<AdminSessionList>('/admin/sessions/events', 'sessions', on, signal)
    };
}
