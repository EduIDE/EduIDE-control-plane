import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { Api } from './api';
import App from './App';
import type { AppConfig } from './config';
import { I18nProvider } from './I18nProvider';

const config: AppConfig = {
    apiBaseUrl: '/api/v1',
    keycloak: { url: 'https://kc.example.org', realm: 'r', clientId: 'c' },
    legal: { imprint: { url: 'https://example.org/i' }, privacy: { url: 'https://example.org/p' } }
};

const apiWith = (adminStatus: number): { api: Api; get: ReturnType<typeof vi.fn> } => {
    const get = vi.fn(async (path: string) =>
        path === '/admin/status' ? { response: { status: adminStatus } } : { data: [], response: { status: 200 } }
    );
    const api = { client: { GET: get }, watchOverview: () => new Promise<void>(() => undefined) } as unknown as Api;
    return { api, get };
};

const openMenu = async (adminStatus: number): Promise<{ get: ReturnType<typeof vi.fn> }> => {
    const { api, get } = apiWith(adminStatus);
    render(
        <I18nProvider initial='en'>
            <App config={config} api={api} user={{ initials: 'AS', label: 'Alex (ga1)' }} logoutUrl='https://kc.example/logout' />
        </I18nProvider>
    );
    // Nothing asks the admin API until somebody opens the menu.
    expect(get).not.toHaveBeenCalledWith('/admin/status');
    fireEvent.click(screen.getByRole('button', { name: /Account/ }));
    await waitFor(() => expect(get).toHaveBeenCalledWith('/admin/status'));
    await new Promise(resolve => setTimeout(resolve, 0));
    return { get };
};

describe('admin menu entry', () => {
    it('asks the admin API once, however often the menu opens', async () => {
        const { get } = await openMenu(200);
        const button = screen.getByRole('button', { name: /Account/ });
        fireEvent.click(button);
        fireEvent.click(button);
        expect(get.mock.calls.filter(c => c[0] === '/admin/status')).toHaveLength(1);
    });

    it('appears only when the service answers 200 for the admin status', async () => {
        await openMenu(200);
        expect(screen.getByRole('link', { name: 'Admin' }).getAttribute('href')).toBe('/admin');
    });

    it.each([403, 404])('stays hidden for %i', async code => {
        await openMenu(code);
        expect(screen.queryByRole('link', { name: 'Admin' })).toBeNull();
        screen.getByRole('link', { name: 'Sign out' });
    });
});

describe('templates after an outage', () => {
    it('keeps asking, then shows the tiles and drops the alert, and hides the loading text meanwhile', async () => {
        vi.useFakeTimers();
        try {
            const java = { name: 'java-17', displayName: 'Java 17', persistence: ['none'], defaultPersistence: 'none', injectableKeys: [] };
            const get = vi
                .fn()
                .mockResolvedValueOnce({ response: { status: 503 }, error: { title: 'Service Unavailable', status: 503 } })
                .mockResolvedValue({ data: [java], response: { status: 200 } });
            const api = { client: { GET: get }, watchOverview: () => new Promise<void>(() => undefined) } as unknown as Api;
            render(
                <I18nProvider initial='en'>
                    <App config={config} api={api} user={{ initials: 'AS', label: 'Alex (ga1)' }} />
                </I18nProvider>
            );
            await act(async () => {
                await vi.advanceTimersByTimeAsync(0);
            });
            screen.getByRole('alert');
            screen.getByText('Service Unavailable');
            expect(screen.queryByText('Loading your environments...')).toBeNull();
            expect(screen.queryByRole('button', { name: 'Start Java 17' })).toBeNull();
            await act(async () => {
                await vi.advanceTimersByTimeAsync(1000);
            });
            screen.getByRole('button', { name: 'Start Java 17' });
            expect(screen.queryByRole('alert')).toBeNull();
            screen.getByText('Loading your environments...');
            expect(get.mock.calls.filter(c => c[0] === '/templates')).toHaveLength(2);
        } finally {
            vi.useRealTimers();
        }
    });

    it('asks again when the overview stream comes back after a drop', async () => {
        vi.useFakeTimers();
        try {
            let streams = 0;
            const get = vi.fn().mockResolvedValue({ data: [], response: { status: 200 } });
            const api = {
                client: { GET: get },
                watchOverview: async (on: (o: unknown) => void) => {
                    streams += 1;
                    on({ items: [] });
                    if (streams === 1) {
                        throw new Error('dropped');
                    }
                    await new Promise<void>(() => undefined);
                }
            } as unknown as Api;
            render(
                <I18nProvider initial='en'>
                    <App config={config} api={api} user={{ initials: 'AS', label: 'Alex (ga1)' }} />
                </I18nProvider>
            );
            await act(async () => {
                await vi.advanceTimersByTimeAsync(0);
            });
            expect(get.mock.calls.filter(c => c[0] === '/templates')).toHaveLength(1);
            await act(async () => {
                await vi.advanceTimersByTimeAsync(1000);
            });
            expect(streams).toBe(2);
            expect(get.mock.calls.filter(c => c[0] === '/templates')).toHaveLength(2);
        } finally {
            vi.useRealTimers();
        }
    });
});

describe('launch data from an abandoned link', () => {
    const renderAt = (search: string): void => {
        window.history.replaceState({}, '', `/${search}`);
        const { api } = apiWith(200);
        render(
            <I18nProvider initial='en'>
                <App config={config} api={api} user={{ initials: 'AS', label: 'Alex (ga1)' }} logoutUrl='https://kc.example/logout' />
            </I18nProvider>
        );
    };

    it('is forgotten when the page opens without a launch link', async () => {
        sessionStorage.setItem('eduide.gitUri', 'https://u:t@h/a.git');
        sessionStorage.setItem('eduide.artemisToken', 't');
        renderAt('');
        await waitFor(() => expect(sessionStorage.length).toBe(0));
    });

    it('is forgotten when the link names an unknown template', async () => {
        sessionStorage.setItem('eduide.gitUri', 'https://u:t@h/a.git');
        renderAt('?appDef=nope');
        await screen.findByText(/nope/);
        expect(sessionStorage.getItem('eduide.gitUri')).toBeNull();
        window.history.replaceState({}, '', '/');
    });
});
