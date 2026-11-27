import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Api } from './api';
import App from './App';
import type { AppConfig } from './config';
import { I18nProvider } from './I18nProvider';

const config: AppConfig = {
    apiBaseUrl: '/api/v1',
    keycloak: { url: 'https://kc.example.org', realm: 'r', clientId: 'c' },
    legal: { imprint: { url: 'https://example.org/i' }, privacy: { url: 'https://example.org/p' } }
};
const java = {
    name: 'java-17',
    displayName: 'Java 17',
    listed: true,
    persistence: ['none'],
    defaultPersistence: 'none',
    injectableKeys: ['GIT_URI']
};
const plain = { name: 'plain', displayName: 'Plain', listed: true, persistence: ['none'], defaultPersistence: 'none', injectableKeys: [] };
const hidden = { ...java, name: 'hidden', displayName: 'Hidden', listed: false };
const provider = { name: 'gh', type: 'github', host: 'github.com', displayName: 'GitHub', connected: false };

interface Setup {
    templates?: unknown[];
    providers?: unknown[];
    preflight?: () => Promise<unknown>;
}

const setup = (
    search: string,
    o: Setup = {}
): { launch: ReturnType<typeof vi.fn>; post: ReturnType<typeof vi.fn>; get: ReturnType<typeof vi.fn> } => {
    window.history.replaceState({}, '', `/${search}`);
    const launch = vi.fn(async () => new Promise<never>(() => undefined));
    const post = vi.fn(o.preflight ?? (async () => ({ data: { result: 'ok' } })));
    const get = vi.fn(async (path: string) => {
        if (path === '/templates') {
            return { data: o.templates ?? [java, plain, hidden], response: { status: 200 } };
        }
        if (path === '/git/providers') {
            return { data: o.providers ?? [provider], response: { status: 200 } };
        }
        if (path === '/sessions/{name}') {
            return {
                data: { name: 's-abc', template: 'java-17', phase: 'Ready', createdAt: '2026-01-01T00:00:00Z' },
                response: { status: 200 }
            };
        }
        if (path === '/sessions/{name}/git/repositories') {
            return { data: { mode: 'write', maxPerSession: 20, approved: [] }, response: { status: 200 } };
        }
        return { data: [], response: { status: 200 } };
    });
    const api = {
        client: { GET: get, POST: post },
        launchDeps: { launch },
        watchOverview: () => new Promise<void>(() => undefined)
    } as unknown as Api;
    render(
        <I18nProvider initial='en'>
            <App config={config} api={api} user={{ initials: 'AS', label: 'Alex' }} />
        </I18nProvider>
    );
    return { launch, post, get };
};
const link = (extra: string): string => `?appDef=java-17&gitUri=${encodeURIComponent('https://github.com/org/repo')}${extra}`;

afterEach(() => {
    window.history.replaceState({}, '', '/');
    sessionStorage.clear();
});

describe('Git accounts section', () => {
    it('shows the providers on the home page', async () => {
        setup('');
        await screen.findByRole('heading', { name: 'Git accounts' });
        screen.getByRole('button', { name: 'Connect GitHub' });
    });

    it('is absent when the service lists no provider (or is too old)', async () => {
        const { get } = setup('', { providers: [] });
        await waitFor(() => expect(get).toHaveBeenCalledWith('/git/providers'));
        await screen.findByRole('button', { name: 'Start Java 17' });
        expect(screen.queryByRole('heading', { name: 'Git accounts' })).toBeNull();
    });
});

describe('preflight before a launch with a repository', () => {
    it.each(['ok', 'not-a-provider', 'unknown'])('launches as before on %s', async result => {
        const { launch, post } = setup(link(''), { preflight: async () => ({ data: { result } }) });
        await waitFor(() => expect(launch).toHaveBeenCalledWith(expect.objectContaining({ template: 'java-17' })));
        expect(post).toHaveBeenCalledWith('/git/preflight', { body: { gitUri: 'https://github.com/org/repo' } });
    });

    it.each([
        ['an error answer', async () => ({ error: { status: 404 } })],
        ['a rejected request', async () => Promise.reject(new Error('down'))]
    ])('never blocks the launch on %s', async (_n, preflight) => {
        const { launch } = setup(link(''), { preflight });
        await waitFor(() => expect(launch).toHaveBeenCalled());
    });

    it('asks to connect and does not launch', async () => {
        const { launch } = setup(link(''), {
            preflight: async () => ({ data: { result: 'connect', provider: { name: 'gh', displayName: 'GitHub' } } })
        });
        await screen.findByText('This repository needs your GitHub account');
        expect(screen.queryByRole('button', { name: /anyway/i })).toBeNull();
        expect(launch).not.toHaveBeenCalled();
    });

    it('names the account on no access and does not launch', async () => {
        const { launch } = setup(link(''), {
            providers: [{ ...provider, connected: true, login: 'alex' }],
            preflight: async () => ({ data: { result: 'no-access', provider: { name: 'gh', displayName: 'GitHub' } } })
        });
        await screen.findByText(/GitHub account alex has no access/);
        screen.getByRole('button', { name: 'Connect another account' });
        expect(launch).not.toHaveBeenCalled();
    });

    it('does not send a repository URL with credentials to the preflight', async () => {
        sessionStorage.setItem('eduide.gitUri', 'https://u:t@github.com/org/repo');
        const { launch, post } = setup('?appDef=java-17');
        await waitFor(() => expect(launch).toHaveBeenCalled());
        expect(post).not.toHaveBeenCalled();
    });

    it('does not call it for a link without a repository', async () => {
        const { launch, post } = setup('?appDef=plain');
        await waitFor(() => expect(launch).toHaveBeenCalled());
        expect(post).not.toHaveBeenCalled();
    });
});

describe('a link with a repository but without a usable template', () => {
    it('shows the picker with the repository and only listed templates that take one', async () => {
        const { launch } = setup(`?gitUri=${encodeURIComponent('https://u:secret@github.com/org/repo.git')}`);
        await screen.findByRole('heading', { name: 'Open github.com/org/repo.git' });
        expect(document.body.textContent).not.toContain('secret');
        screen.getByRole('button', { name: 'Start Java 17' });
        expect(screen.queryByRole('button', { name: 'Start Plain' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Start Hidden' })).toBeNull();
        expect(launch).not.toHaveBeenCalled();
    });

    it('launches with the repository after a pick', async () => {
        const { launch, post } = setup(
            `?gitUri=${encodeURIComponent('https://github.com/org/repo')}&artemisUrl=https%3A%2F%2Fa.example.org`
        );
        await screen.findByTestId('repo-picker');
        fireEvent.click(await screen.findByRole('button', { name: 'Start Java 17' }));
        await waitFor(() => expect(launch).toHaveBeenCalled());
        expect(post).toHaveBeenCalledWith('/git/preflight', expect.anything());
        expect(launch).toHaveBeenCalledWith(
            expect.objectContaining({ template: 'java-17', data: { GIT_URI: 'https://github.com/org/repo' } })
        );
    });

    it('treats an unknown template name like a missing one when the link has a repository', async () => {
        setup(`?appDef=nope&gitUri=${encodeURIComponent('https://github.com/org/repo')}`);
        await screen.findByText('The template nope from the link does not exist.');
        screen.getByRole('button', { name: 'Start Java 17' });
    });

    it('keeps the old error for an unknown template without a repository', async () => {
        setup('?appDef=nope');
        await screen.findByText(/Unknown template: nope/);
    });

    it('says so when no template takes a repository', async () => {
        setup(`?gitUri=${encodeURIComponent('https://github.com/org/repo')}`, { templates: [plain] });
        await screen.findByText('No template can open a repository.');
    });

    it('does nothing for a link without repository and template', async () => {
        const { launch } = setup('?title=x');
        await screen.findByRole('button', { name: 'Start Java 17' });
        expect(launch).not.toHaveBeenCalled();
    });
});

describe('approval link', () => {
    const approve = (extra = ''): string =>
        `?session=s-abc&approveRepo=${encodeURIComponent('https://github.com/org/repo')}&access=read${extra}`;

    it('shows the confirmation, starts nothing and approves nothing', async () => {
        const { launch, post } = setup(approve('&appDef=java-17'));
        await screen.findByRole('heading', { name: 'Allow Git access for a session?' });
        screen.getByRole('button', { name: 'Allow' });
        expect(launch).not.toHaveBeenCalled();
        expect(post).not.toHaveBeenCalled();
    });

    it('refuses a hostile access value without any call about the session', async () => {
        const { get } = setup(approve().replace('access=read', 'access=root'));
        await screen.findByText('This link is not valid. Nothing was approved.');
        expect(get.mock.calls.filter(c => String(c[0]).startsWith('/sessions'))).toHaveLength(0);
    });
});
