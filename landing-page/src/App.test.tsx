import { fireEvent, render, screen, waitFor } from '@testing-library/react';
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
