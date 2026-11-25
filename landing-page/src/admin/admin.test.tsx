import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Api } from '../api';
import type {
    AdminConfig,
    AdminPools,
    AdminProblems,
    AdminSessionDetail,
    AdminSessionList,
    AdminStatus,
    AdminTemplate,
    AdminWorkspaceList
} from '../apiTypes';
import type { AppConfig } from '../config';
import { I18nProvider } from '../I18nProvider';
import { AdminApp } from './AdminApp';

const iso = (ms: number): string => new Date(Date.now() + ms).toISOString();
const MIN = 60_000;
const DAY = 24 * 60 * MIN;

const appConfig: AppConfig = {
    apiBaseUrl: '/api/v1',
    keycloak: { url: 'https://kc.example.org', realm: 'r', clientId: 'c' },
    legal: { imprint: { url: 'https://example.org/i' }, privacy: { url: 'https://example.org/p' } }
};
const user = { firstName: 'Alex', initials: 'AS', label: 'Alex Schmidt (ga58roj)' };

const status = (over: Partial<AdminStatus> = {}): AdminStatus => ({
    runningSessions: 142,
    startingSessions: 3,
    idleReady: 13,
    idleTarget: 29,
    capacityPressure: false,
    warnings: [
        { code: 'pool-empty', pool: 'java-17', since: iso(-4 * MIN - 5000), message: 'Pool java-17 has no idle instance for 4 min.' }
    ],
    stats: {
        startsLastHour: 212,
        failuresLastHour: 3,
        startP95: [
            { template: 'java-17', persistence: 'volume', seconds: 29 },
            { template: 'c', persistence: 'none', seconds: 1.1 }
        ],
        cpuRequestsPercent: 71,
        nodeCount: 9,
        startsPerHour: [
            { time: iso(-2 * 3600_000), warm: 10, cold: 2, failed: 0 },
            { time: iso(-3600_000), warm: 20, cold: 4, failed: 1 }
        ],
        poolFree: [{ pool: 'java-17', points: [{ time: iso(-MIN), value: 0 }] }]
    },
    ...over
});
const template = (name: string, over: Partial<AdminTemplate> = {}): AdminTemplate => ({
    name,
    displayName: name === 'java-17' ? 'Java 17' : name.toUpperCase(),
    image: 'registry.example.org/java:1',
    imageVersion: 'EduIDE 2026.11.2',
    imageDigest: 'sha256:4c1e0000000000009a02',
    persistence: ['none', 'volume'],
    defaultPersistence: 'volume',
    injectableKeys: [
        { name: 'GIT_URI', source: 'Request', required: false },
        { name: 'TEMPLATE', source: 'Request', required: true }
    ],
    timeouts: { idleSeconds: 3600, warningSeconds: 3000, maxSeconds: 86400 },
    variants: [{ id: 'maven', name: 'Maven' }],
    includes: [{ group: 'Language', items: [{ name: 'OpenJDK', version: '17' }] }],
    startEstimate: { none: { seconds: 0.4, strategy: 'warm', samples: 3 }, volume: { seconds: 25, strategy: 'cold', samples: 3 } },
    pool: { name: 'java-17', minIdle: 2, targetIdle: 14, maxInstances: 20 },
    ...over
});
const adminSession = (name: string, over: object = {}): AdminSessionList['items'][number] => ({
    name,
    template: 'java-17',
    persistence: 'none',
    phase: 'Ready',
    startStrategy: 'warm-ready',
    ownerHash: 'u-3f9a1c0d2b7e4a55',
    createdAt: iso(-12 * MIN),
    stopsAt: iso(5 * MIN),
    stopReason: 'idle',
    ...over
});

const data: Record<string, unknown> = {
    '/admin/status': status(),
    '/admin/config': {
        deployment: 'test3',
        namespace: 'eduide-test3',
        hosts: { service: 'ide.example.org', instance: 'i.example.org', webview: 'w.example.org' },
        oidc: {
            issuerURL: 'https://kc.example.org/realms/r',
            clientIDs: ['landing'],
            userClaim: 'preferred_username',
            adminRolePath: 'resource_access.eduide.roles',
            adminRole: 'eduide-admin'
        },
        sessionsPerUser: 3,
        allowedHosts: ['git.example.org'],
        workspaces: { retentionSeconds: 14 * 86400, graceSeconds: 86400, defaultSizeBytes: 2 * 1024 ** 3 },
        capacity: { exhaustedAfterSeconds: 45 },
        rateLimit: { launchesPerMinute: 10, adminPerMinute: 60 },
        launchDataMaxBytes: 8192,
        telemetry: { otlpEndpoint: 'otel-collector:4317', userIdsHashed: true },
        admin: { prometheusConfigured: true, links: { grafana: 'https://grafana.example.org', jaeger: 'https://jaeger.example.org' } }
    } satisfies AdminConfig,
    '/admin/templates': [template('java-17'), template('c', { pool: undefined, variants: [], persistence: ['none'] })],
    '/admin/pools': {
        pools: [
            {
                name: 'java-17',
                template: 'java-17',
                minIdle: 2,
                targetIdle: 14,
                maxInstances: 20,
                idleReady: 0,
                idleStarting: 4,
                claimed: 96,
                state: 'empty',
                emptySince: iso(-4 * MIN),
                capacityPressure: false,
                scalerMode: 'predictive'
            }
        ],
        decisions: [{ time: iso(-MIN), pool: 'java-17', reason: 'ScaledUp', message: 'predicted 31 launches' }]
    } satisfies AdminPools,
    '/admin/problems': {
        since: '24h',
        codes: [
            { code: 'capacity-exhausted', count: 9 },
            { code: 'rate-limited', count: 6 }
        ],
        events: [
            {
                time: iso(-5 * MIN),
                reason: 'Failed',
                code: 'capacity-exhausted',
                message: 'No room for a new instance',
                template: 'java-17',
                ownerHash: 'u-3f9a1c0d2b7e4a55',
                session: 's-x81kq',
                traceUrl: 'https://jaeger.example.org/trace/abc'
            }
        ]
    } satisfies AdminProblems,
    '/admin/workspaces': {
        items: [
            {
                name: 'ws-7tq0e',
                template: 'java-17',
                phase: 'Ready',
                state: 'expiring',
                title: 'Hello World',
                ownerHash: 'u-b21c07aa',
                requestedBytes: 2 * 1024 ** 3,
                createdAt: iso(-20 * DAY),
                lastUsed: iso(-11 * DAY),
                deletesAt: iso(2 * DAY)
            },
            {
                name: 'ws-a91k2',
                template: 'java-17',
                phase: 'Ready',
                state: 'active',
                title: 'Linked lists',
                ownerHash: 'u-3f9a1c0d',
                requestedBytes: 2 * 1024 ** 3,
                createdAt: iso(-20 * DAY),
                deletesAt: iso(9 * DAY)
            }
        ],
        total: 2,
        summary: { total: 1208, inUse: 412, expiringIn3Days: 47, expired: 12, requestedBytes: 2.4 * 1024 ** 4 }
    } satisfies AdminWorkspaceList,
    '/admin/sessions': {
        items: [adminSession('s-8kq2m'), adminSession('s-z0r3e', { persistence: 'volume', phase: 'Failed' })],
        total: 2
    } satisfies AdminSessionList,
    '/admin/sessions/{name}': {
        ...adminSession('s-8kq2m', { title: 'Linked lists', traceUrl: 'https://jaeger.example.org/trace/xyz', workspace: undefined }),
        launchDataKeys: ['GIT_URI', 'ARTEMIS_TOKEN'],
        timestamps: { claimed: iso(-12 * MIN), ready: iso(-12 * MIN + 1000) }
    } satisfies AdminSessionDetail
};

interface MockApi {
    api: Api;
    get: ReturnType<typeof vi.fn>;
    /** The abort signals of every stream the page opened. */
    signals: { status: AbortSignal[]; sessions: AbortSignal[] };
}
const mockApi = (over: Record<string, unknown> = {}, statusCode: Record<string, number> = {}): MockApi => {
    const all = { ...data, ...over };
    const get = vi.fn(async (path: string, _opts?: unknown) => {
        const code = statusCode[path];
        if (code) {
            return { response: { status: code } };
        }
        return { data: all[path], response: { status: 200 } };
    });
    const hang = (): Promise<void> => new Promise<void>(() => undefined);
    const signals: MockApi['signals'] = { status: [], sessions: [] };
    const api = {
        client: { GET: get },
        watchAdminStatus: async (on: (s: unknown) => void, signal: AbortSignal) => {
            signals.status.push(signal);
            on(all['/admin/status']);
            await hang();
        },
        watchAdminSessions: async (on: (s: unknown) => void, signal: AbortSignal) => {
            signals.sessions.push(signal);
            on(all['/admin/sessions']);
            await hang();
        }
    } as unknown as Api;
    return { api, get, signals };
};

const open = async (path: string, mock = mockApi(), logoutUrl?: string): Promise<MockApi> => {
    window.history.pushState({}, '', path);
    render(
        <I18nProvider initial='en'>
            <AdminApp config={appConfig} api={mock.api} user={user} logoutUrl={logoutUrl} />
        </I18nProvider>
    );
    return mock;
};

afterEach(() => window.history.pushState({}, '', '/'));

describe('access', () => {
    it.each([403, 404])('shows a clear No access page for %i and no panel', async code => {
        await open('/admin', mockApi({}, { '/admin/status': code }));
        await screen.findByText('No access');
        expect(screen.queryByRole('navigation', { name: 'Admin' })).toBeNull();
        expect(screen.getByRole('link', { name: 'Back to EduIDE' }).getAttribute('href')).toBe('/');
    });

    it('offers a retry when the check fails for another reason', async () => {
        await open('/admin', mockApi({}, { '/admin/status': 500 }));
        await screen.findByText('The admin panel could not be reached.');
        screen.getByRole('button', { name: 'Try again' });
    });
});

describe('layout', () => {
    it('has the nav with counts, the live and read only marks, and the shared footer', async () => {
        await open('/admin');
        const nav = await screen.findByRole('navigation', { name: 'Admin' });
        within(nav).getByRole('link', { name: /Status/ });
        // The count comes with the first status event, which can land after the nav renders.
        await waitFor(() => expect(within(nav).getByRole('link', { name: /Sessions/ }).textContent).toContain('145'));
        await waitFor(() => expect(within(nav).getByRole('link', { name: /Templates/ }).textContent).toContain('2'));
        expect(within(nav).getByRole('link', { name: 'Grafana' }).getAttribute('href')).toBe('https://grafana.example.org/');
        screen.getByText('Live');
        screen.getByText('Read only');
        screen.getByRole('link', { name: 'GitHub' });
        screen.getByRole('link', { name: 'Status', hidden: false });
    });

    it('navigates without a reload and keeps real paths for deep links', async () => {
        await open('/admin');
        const nav = await screen.findByRole('navigation', { name: 'Admin' });
        const pools = within(nav).getByRole('link', { name: /Pools/ });
        expect(pools.getAttribute('href')).toBe('/admin/pools');
        fireEvent.click(pools);
        await screen.findByRole('heading', { level: 1, name: 'Pools' });
        expect(window.location.pathname).toBe('/admin/pools');
        expect(pools.getAttribute('aria-current')).toBe('page');
    });

    it('opens a deep link directly', async () => {
        await open('/admin/limits');
        await screen.findByRole('heading', { level: 1, name: 'Limits and retention' });
    });

    it('has the user menu with language, landing page and sign out, and Escape returns the focus', async () => {
        await open('/admin', mockApi(), 'https://kc.example/logout');
        await screen.findByRole('navigation', { name: 'Admin' });
        const button = screen.getByRole('button', { name: /Account Alex Schmidt/ });
        fireEvent.click(button);
        screen.getByText('Admin');
        expect(screen.getByRole('link', { name: 'Open landing page' }).getAttribute('href')).toBe('/');
        expect(screen.getByRole('link', { name: 'Sign out' }).getAttribute('href')).toBe('https://kc.example/logout');
        fireEvent.click(screen.getByRole('button', { name: 'DE' }));
        await screen.findByRole('link', { name: 'Landing Page öffnen' });
        localStorage.clear();
        fireEvent.keyDown(document, { key: 'Escape' });
        expect(document.activeElement).toBe(button);
    });
});

describe('status page', () => {
    it('shows warnings, KPIs, pools with bars, charts, problems, workspaces and sessions', async () => {
        await open('/admin');
        await screen.findByText('Pool java-17 has had no idle instance for 4 min.');
        expect(screen.getByRole('alert').textContent).toContain('no idle instance');
        const kpis = (await screen.findByText('Running sessions')).closest('.a-kpis') as HTMLElement;
        within(kpis).getByText('142');
        within(kpis).getByText('3 starting');
        within(kpis).getByText('below target');
        within(kpis).getByText('29.0');
        within(kpis).getByText('Failed starts, 1 h');
        within(kpis).getByText('of 212 starts');
        within(kpis).getByText('71');
        within(kpis).getByText('CPU requests, 9 nodes');
        await screen.findByRole('img', { name: '0 / 4 / 14' });
        screen.getByRole('img', { name: 'Starts, last 12 h' });
        screen.getByText('Start time p95 per template');
        await screen.findByText('1208');
        screen.getByText('47');
        screen.getByText(/Retention 14 days after last use/);
        await screen.findByText('s-8kq2m');
        await screen.findAllByRole('link', { name: 'Trace' });
        expect(document.body.textContent).not.toContain('ARTEMIS_TOKEN');
    });

    it('hides charts and figures that need Prometheus and says so neutrally, with the Grafana link', async () => {
        await open('/admin', mockApi({ '/admin/status': status({ stats: undefined, warnings: [] }) }));
        await screen.findByText(/need Prometheus are hidden/);
        expect(screen.getByRole('link', { name: 'Open Grafana' }).getAttribute('href')).toBe('https://grafana.example.org/');
        expect(screen.queryByText('Start time p95 per template')).toBeNull();
        expect(screen.queryByRole('img', { name: 'Starts, last 12 h' })).toBeNull();
        expect(screen.queryByText('Failed starts, 1 h')).toBeNull();
        expect(screen.queryByRole('alert')).toBeNull();
        screen.getByText('Running sessions');
    });

    it('shows only the warnings the API sends, by code, and keeps the service message for unknown codes', async () => {
        await open(
            '/admin',
            mockApi({
                '/admin/status': status({
                    warnings: [
                        { code: 'scaler-silent', since: iso(-3 * MIN - 5000), message: 'english text from the service' },
                        { code: 'something-new' as 'pool-empty', message: 'A warning of a newer service.' }
                    ]
                })
            })
        );
        await screen.findByText('The pool scaler has not reported for 3 min.');
        screen.getByText('A warning of a newer service.');
        expect(screen.queryByText(/no idle instance/)).toBeNull();
        expect(screen.queryByText('english text from the service')).toBeNull();
    });

    it('shows warnings in German from the code, not from the service text', async () => {
        localStorage.setItem('lang', 'de');
        window.history.pushState({}, '', '/admin');
        render(
            <I18nProvider initial='de'>
                <AdminApp config={appConfig} api={mockApi().api} user={user} />
            </I18nProvider>
        );
        await screen.findByText('Pool java-17 hat seit 4 min keine freie Instanz.');
        localStorage.clear();
    });
});

describe('pools page', () => {
    it('shows cards with the state and the free series, and the scaler decisions', async () => {
        await open('/admin/pools');
        await screen.findByText('Scaler decisions');
        screen.getByText('empty');
        screen.getByText('predictive');
        screen.getByRole('img', { name: 'Idle ready, last 6 h' });
        screen.getByText('predicted 31 launches');
        screen.getByText(/scaled up/);
    });
});

describe('sessions page', () => {
    it('sends the filters to the API and shows the detail with launch data keys only and the trace link', async () => {
        const mock = await open('/admin/sessions');
        await screen.findByText('s-8kq2m');
        fireEvent.change(screen.getByRole('combobox', { name: 'Phase' }), { target: { value: 'Failed' } });
        await waitFor(() =>
            expect(mock.get).toHaveBeenCalledWith('/admin/sessions', {
                params: { query: expect.objectContaining({ phase: 'Failed', limit: 100 }) }
            })
        );
        fireEvent.change(screen.getByRole('combobox', { name: 'Phase' }), { target: { value: '' } });
        // The table is rebuilt from the other channel, so look the row up again.
        fireEvent.click((await screen.findByText('s-8kq2m')).closest('tr') as HTMLElement);
        await screen.findByText('GIT_URI, ARTEMIS_TOKEN', { exact: false });
        screen.getByText('(values hidden)', { exact: false });
        expect(screen.getByRole('link', { name: /Launch trace/ }).getAttribute('href')).toBe('https://jaeger.example.org/trace/xyz');
        expect(mock.get).toHaveBeenCalledWith('/admin/sessions/{name}', { params: { path: { name: 's-8kq2m' } } });
        screen.getByText('Timeline');
    });

    it('marks a temporary session that stops within ten minutes in red', async () => {
        await open('/admin/sessions');
        const row = (await screen.findByText('s-8kq2m')).closest('tr') as HTMLElement;
        expect(
            within(row)
                .getByText(/if inactive/)
                .closest('td')?.className
        ).toContain('bad-t');
        const saved = screen.getByText('s-z0r3e').closest('tr') as HTMLElement;
        expect(saved.querySelector('td.bad-t')).toBeNull();
    });

    it('selects with the keyboard', async () => {
        await open('/admin/sessions');
        const row = (await screen.findByText('s-8kq2m')).closest('tr') as HTMLElement;
        fireEvent.keyDown(row, { key: 'Enter' });
        await screen.findByText('Timeline');
    });
});

describe('workspaces page', () => {
    it('shows the summary and marks deletion within three days in red', async () => {
        await open('/admin/workspaces');
        await screen.findByText('1208');
        screen.getByText('412 in use right now');
        screen.getByText('2.4 TiB');
        const soon = screen.getByText('ws-7tq0e').closest('tr') as HTMLElement;
        expect(soon.querySelector('td.bad-t')).not.toBeNull();
        const later = screen.getByText('ws-a91k2').closest('tr') as HTMLElement;
        expect(later.querySelector('td.bad-t')).toBeNull();
        screen.getByText('2 of 2 workspaces');
    });

    it('filters by state through the API', async () => {
        const mock = await open('/admin/workspaces');
        await screen.findByText('ws-7tq0e');
        fireEvent.change(screen.getByRole('combobox', { name: 'State' }), { target: { value: 'expired' } });
        await waitFor(() =>
            expect(mock.get).toHaveBeenCalledWith('/admin/workspaces', { params: { query: expect.objectContaining({ state: 'expired' }) } })
        );
    });
});

describe('problems page', () => {
    it('shows bars per code, the last hour, and explains codes in a tooltip instead of a legend', async () => {
        await open('/admin/problems');
        await screen.findByText('By code, last 24 h');
        const pill = screen.getAllByText('capacity-exhausted')[0];
        expect(pill.getAttribute('data-tip')).toContain('No room in the cluster');
        expect(pill.getAttribute('tabindex')).toBe('0');
        expect(pill.getAttribute('aria-label')).toContain('capacity-exhausted');
        screen.getByText('Last hour');
        screen.getByText('No room for a new instance');
        expect(screen.queryByText(/legend|meaning of/i)).toBeNull();
        expect(screen.getByRole('link', { name: /Trace/ }).getAttribute('href')).toBe('https://jaeger.example.org/trace/abc');
    });

    it('hides the 24 h bars without Prometheus', async () => {
        await open('/admin/problems', mockApi({ '/admin/problems': { since: '24h', events: [] } }));
        await screen.findByText(/need Prometheus are hidden/);
        expect(screen.queryByText('By code, last 24 h')).toBeNull();
        screen.getByText('No problems in the last hour.');
    });
});

describe('templates and limits pages', () => {
    it('shows template cards with image, variants, estimates, timeouts, keys and the pool', async () => {
        await open('/admin/templates');
        await screen.findByText('Java 17');
        expect(screen.getAllByText('EduIDE 2026.11.2')).toHaveLength(2);
        screen.getByText('Maven');
        expect(screen.getAllByText(/Temporary session: 0.4 s/).length).toBeGreaterThan(0);
        expect(screen.getAllByText('inactive 60 min, warning after 50 min, max 24 h').length).toBeGreaterThan(0);
        expect(screen.getAllByText('GIT_URI, TEMPLATE (required)').length).toBeGreaterThan(0);
        expect(screen.getByRole('link', { name: 'java-17' }).getAttribute('href')).toBe('/admin/pools');
        expect(screen.getAllByText('OpenJDK 17').length).toBeGreaterThan(0);
        screen.getByText('none, cold starts only');
    });

    it('shows the effective config in groups', async () => {
        await open('/admin/limits');
        await screen.findByText('Sessions and starts');
        screen.getByText('10 per user and minute');
        screen.getByText('14 days');
        screen.getByText('2.0 GiB');
        screen.getByText('resource_access.eduide.roles: eduide-admin');
        screen.getByText('salted hash only');
    });
});

describe('small screens', () => {
    it('wraps every table in a horizontal scroll container', async () => {
        await open('/admin/sessions');
        const table = (await screen.findByText('s-8kq2m')).closest('table') as HTMLElement;
        expect(table.parentElement?.className).toBe('a-scroll');
    });
});

describe('unmount', () => {
    it('stops the 30 second polling', async () => {
        vi.useFakeTimers();
        try {
            const mock = mockApi();
            window.history.pushState({}, '', '/admin/pools');
            const { unmount } = render(
                <I18nProvider initial='en'>
                    <AdminApp config={appConfig} api={mock.api} user={user} />
                </I18nProvider>
            );
            await act(async () => {
                await vi.advanceTimersByTimeAsync(0);
            });
            const poolCalls = (): number => mock.get.mock.calls.filter(c => c[0] === '/admin/pools').length;
            const before = poolCalls();
            await act(async () => {
                await vi.advanceTimersByTimeAsync(30_000);
            });
            expect(poolCalls()).toBe(before + 1);
            unmount();
            await vi.advanceTimersByTimeAsync(90_000);
            expect(poolCalls()).toBe(before + 1);
        } finally {
            vi.useRealTimers();
        }
    });
});

describe('sessions page channels', () => {
    it('uses the stream for the unfiltered view and polling only once a filter is set', async () => {
        const mock = await open('/admin/sessions');
        await screen.findByText('s-8kq2m');
        expect(mock.get.mock.calls.filter(c => c[0] === '/admin/sessions')).toHaveLength(0);
        expect(mock.signals.sessions).toHaveLength(1);
        fireEvent.change(screen.getByRole('combobox', { name: 'Phase' }), { target: { value: 'Ready' } });
        await waitFor(() => expect(mock.get.mock.calls.filter(c => c[0] === '/admin/sessions')).toHaveLength(1));
        expect(mock.signals.sessions[0].aborted).toBe(true);
        fireEvent.change(screen.getByRole('combobox', { name: 'Phase' }), { target: { value: '' } });
        await waitFor(() => expect(mock.signals.sessions).toHaveLength(2));
        const calls = mock.get.mock.calls.filter(c => c[0] === '/admin/sessions').length;
        await new Promise(resolve => setTimeout(resolve, 20));
        expect(mock.get.mock.calls.filter(c => c[0] === '/admin/sessions')).toHaveLength(calls);
    });

    it('loads more pages with the API cursor', async () => {
        const first = { items: [adminSession('s-a')], total: 2, next: 'cur-1' };
        const mock = await open('/admin/sessions', mockApi({ '/admin/sessions': first }));
        await screen.findByText('s-a');
        mock.get.mockImplementationOnce(async () => ({ data: { items: [adminSession('s-b')], total: 2 }, response: { status: 200 } }));
        fireEvent.click(screen.getByRole('button', { name: 'Show more' }));
        await screen.findByText('s-b');
        screen.getByText('s-a');
        expect(mock.get).toHaveBeenCalledWith('/admin/sessions', { params: { query: expect.objectContaining({ cursor: 'cur-1' }) } });
        expect(screen.queryByRole('button', { name: 'Show more' })).toBeNull();
    });

    it('shows not found for a missing session and never the previous session', async () => {
        const mock = await open('/admin/sessions');
        fireEvent.click((await screen.findByText('s-8kq2m')).closest('tr') as HTMLElement);
        await screen.findByText('Timeline');
        mock.get.mockImplementation(async (path: string) =>
            path === '/admin/sessions/{name}' ? { response: { status: 404 } } : { data: data[path], response: { status: 200 } }
        );
        fireEvent.click(screen.getByText('s-z0r3e').closest('tr') as HTMLElement);
        await screen.findByText('This session does not exist (anymore).');
        expect(screen.queryByText('Timeline')).toBeNull();
        expect(screen.queryByText('GIT_URI, ARTEMIS_TOKEN', { exact: false })).toBeNull();
    });
});

describe('streams end with the page', () => {
    it('aborts the sessions stream when the status page is left, and both streams when the panel closes', async () => {
        const mock = mockApi();
        window.history.pushState({}, '', '/admin');
        const { unmount } = render(
            <I18nProvider initial='en'>
                <AdminApp config={appConfig} api={mock.api} user={user} />
            </I18nProvider>
        );
        const nav = await screen.findByRole('navigation', { name: 'Admin' });
        await waitFor(() => expect(mock.signals.sessions).toHaveLength(1));
        expect(mock.signals.status).toHaveLength(1);
        fireEvent.click(within(nav).getByRole('link', { name: /Pools/ }));
        await screen.findByRole('heading', { level: 1, name: 'Pools' });
        expect(mock.signals.sessions[0].aborted).toBe(true);
        expect(mock.signals.status[0].aborted).toBe(false);
        unmount();
        expect(mock.signals.status[0].aborted).toBe(true);
    });
});

describe('unsafe and unknown values', () => {
    it('renders no link for an API or config address that is not http(s)', async () => {
        const events = [{ time: iso(-MIN), reason: 'Failed', message: 'x', traceUrl: 'javascript:alert(1)' }];
        const config = {
            ...(data['/admin/config'] as AdminConfig),
            admin: { prometheusConfigured: true, links: { grafana: 'javascript:alert(1)', jaeger: 'data:text/html,x' } }
        };
        await open('/admin/problems', mockApi({ '/admin/problems': { since: '24h', events }, '/admin/config': config }));
        await screen.findByText('x');
        await waitFor(() => expect(screen.queryByRole('link', { name: 'Grafana' })).toBeNull());
        expect(screen.queryByRole('link', { name: /Trace/ })).toBeNull();
        expect(screen.queryByRole('link', { name: 'Jaeger' })).toBeNull();
        expect(document.querySelector('a[href^="javascript"]')).toBeNull();
        expect(document.querySelector('a[href^="data"]')).toBeNull();
    });

    it('shows a phase, state and reason from a newer service neutrally', async () => {
        await open('/admin/sessions', mockApi({ '/admin/sessions': { items: [adminSession('s-new', { phase: 'Paused' })], total: 1 } }));
        const row = (await screen.findByText('s-new')).closest('tr') as HTMLElement;
        within(row).getByText('Paused');
    });

    it('shows a workspace state from a newer service neutrally', async () => {
        const list = data['/admin/workspaces'] as AdminWorkspaceList;
        await open(
            '/admin/workspaces',
            mockApi({ '/admin/workspaces': { ...list, items: [{ ...list.items[0], name: 'ws-new', state: 'archived' }] } })
        );
        const row = (await screen.findByText('ws-new')).closest('tr') as HTMLElement;
        within(row).getByText('archived');
    });
});

describe('failed updates', () => {
    it('keeps the data and shows one hint when a later poll fails', async () => {
        vi.useFakeTimers();
        try {
            const mock = mockApi();
            window.history.pushState({}, '', '/admin/pools');
            render(
                <I18nProvider initial='en'>
                    <AdminApp config={appConfig} api={mock.api} user={user} />
                </I18nProvider>
            );
            await act(async () => {
                await vi.advanceTimersByTimeAsync(0);
            });
            screen.getByText('predictive');
            expect(screen.queryByText('Update failed, showing older data')).toBeNull();
            mock.get.mockImplementation(async () => ({ response: { status: 500 } }));
            await act(async () => {
                await vi.advanceTimersByTimeAsync(30_000);
            });
            screen.getByText('Update failed, showing older data');
            screen.getByText('predictive');
        } finally {
            vi.useRealTimers();
        }
    });
});

describe('problem lists', () => {
    it('says no problems in the last 24 hours when the 24 h panel is empty', async () => {
        await open('/admin/problems', mockApi({ '/admin/problems': { since: '24h', codes: [], events: [] } }));
        await screen.findByText('No problems in the last 24 hours.');
        screen.getByText('No problems in the last hour.');
    });

    it('hides the template and user columns of recent problems when no row has a value', async () => {
        const events = [{ time: iso(-MIN), reason: 'Failed', code: 'start-failed', message: 'x' }];
        await open('/admin', mockApi({ '/admin/problems': { since: '24h', events } }));
        const table = (await screen.findByText('start-failed')).closest('table') as HTMLElement;
        expect(within(table).queryByRole('columnheader', { name: 'Template' })).toBeNull();
        expect(within(table).queryByRole('columnheader', { name: 'User' })).toBeNull();
        within(table).getByRole('columnheader', { name: 'Code' });
    });

    it('keeps the columns when a row has the values', async () => {
        await open('/admin');
        const table = (await screen.findByText('capacity-exhausted')).closest('table') as HTMLElement;
        within(table).getByRole('columnheader', { name: 'Template' });
        within(table).getByRole('columnheader', { name: 'User' });
    });
});
