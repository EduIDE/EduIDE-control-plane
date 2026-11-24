import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Api } from '../api';
import type { Template } from '../api-client';
import type { GitProvider } from '../apiTypes';
import { I18nProvider } from '../I18nProvider';
import { GitAccounts } from './GitAccounts';
import { GitApproval } from './GitApproval';
import { GitCallback } from './GitCallback';
import { GitGate } from './GitGate';
import { RepoPicker } from './RepoPicker';

const wrap = (ui: React.ReactNode): React.JSX.Element => <I18nProvider initial='en'>{ui}</I18nProvider>;
const apiWith = (client: Record<string, unknown>): Api => ({ client }) as unknown as Api;
const flush = async (): Promise<void> => {
    await act(async () => {
        await new Promise(resolve => setTimeout(resolve, 0));
    });
};

const gh: GitProvider = { name: 'gh', type: 'github', host: 'github.com', displayName: 'GitHub', connected: false };
const gl: GitProvider = { name: 'gl', type: 'gitlab', host: 'gitlab.example.org', displayName: 'GitLab', connected: true, login: 'alex' };

afterEach(() => window.history.replaceState({}, '', '/'));

describe('GitAccounts', () => {
    it('shows nothing without providers', () => {
        const { container } = render(wrap(<GitAccounts api={apiWith({})} providers={[]} onChange={vi.fn()} />));
        expect(container.textContent).toBe('');
    });

    it('says what EduIDE does with the account and shows one row per provider', () => {
        render(wrap(<GitAccounts api={apiWith({})} providers={[gh, gl]} onChange={vi.fn()} />));
        screen.getByRole('heading', { name: 'Git accounts' });
        screen.getByText(/only for the repository a session was started with/);
        screen.getByText(/never enters the IDE/);
        screen.getByText(/logs pushes and refused Git requests/);
        screen.getByText(/inactivity period/);
        const ghRow = within(screen.getByTestId('git-provider-gh'));
        ghRow.getByText('GitHub');
        ghRow.getByText('Not connected');
        ghRow.getByRole('button', { name: 'Connect GitHub' });
        const glRow = within(screen.getByTestId('git-provider-gl'));
        glRow.getByText('Connected as alex');
        expect(glRow.queryByRole('button', { name: /^Connect/ })).toBeNull();
    });

    it('starts the flow for the clicked provider and returns to the current page', async () => {
        window.history.replaceState({}, '', '/?appDef=java-17&gitUri=https%3A%2F%2Fgithub.com%2Fa%2Fb');
        const POST = vi.fn(async () => ({ error: { code: 'rate-limited' } }));
        render(wrap(<GitAccounts api={apiWith({ POST })} providers={[gh]} onChange={vi.fn()} />));
        fireEvent.click(screen.getByRole('button', { name: 'Connect GitHub' }));
        await waitFor(() => screen.getByRole('alert'));
        expect(POST).toHaveBeenCalledWith('/git/providers/{name}/connect', {
            params: { path: { name: 'gh' } },
            body: { returnTo: '/?appDef=java-17&gitUri=https%3A%2F%2Fgithub.com%2Fa%2Fb' }
        });
        screen.getByText('Too many requests. Please wait a moment and try again.');
    });

    it('asks before it disconnects and deletes only after the confirmation', async () => {
        const DELETE = vi.fn(async () => ({ response: { status: 204 } }));
        const onChange = vi.fn();
        render(wrap(<GitAccounts api={apiWith({ DELETE })} providers={[gl]} onChange={onChange} />));
        fireEvent.click(screen.getByRole('button', { name: 'Disconnect GitLab' }));
        const dialog = screen.getByRole('dialog');
        within(dialog).getByText(/revokes it at the provider/);
        expect(DELETE).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
        expect(DELETE).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole('button', { name: 'Disconnect GitLab' }));
        fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Disconnect' }));
        await waitFor(() => expect(DELETE).toHaveBeenCalledWith('/git/providers/{name}', { params: { path: { name: 'gl' } } }));
        await waitFor(() => expect(onChange).toHaveBeenCalled());
    });
});

describe('GitCallback', () => {
    const replace = vi.fn();
    const stubLocation = (): void => {
        vi.stubGlobal('location', { ...window.location, replace });
    };

    it('posts code and state, then goes to the return path', async () => {
        stubLocation();
        const POST = vi.fn(async () => ({ data: { provider: 'gh', login: 'alex', returnTo: '/?appDef=java-17' } }));
        render(wrap(<GitCallback api={apiWith({ POST })} params={{ state: 's1', code: 'c1' }} />));
        await waitFor(() => expect(replace).toHaveBeenCalledWith('/?appDef=java-17'));
        expect(POST).toHaveBeenCalledWith('/git/callback', { body: { state: 's1', code: 'c1' } });
        screen.getByText('Account alex connected. Taking you back...');
    });

    it.each([
        ['git-invalid-state', /another account or is not valid/],
        ['git-state-expired', /took too long/],
        ['git-denied', /did not get your permission/],
        ['git-provider-error', /did not accept the connection/],
        ['rate-limited', /Too many requests/],
        ['something-new', /Something went wrong/]
    ])('shows a clear message for %s and stays on the page', async (code, text) => {
        stubLocation();
        replace.mockClear();
        render(
            wrap(
                <GitCallback
                    api={apiWith({ POST: async () => ({ error: { code, detail: 'provider said secret-detail' } }) })}
                    params={{ state: 's1', code: 'c1' }}
                />
            )
        );
        const alert = await screen.findByRole('alert');
        expect(alert.textContent).toMatch(text);
        expect(alert.textContent).not.toContain('secret-detail');
        expect(replace).not.toHaveBeenCalled();
        expect(screen.getByRole('link', { name: 'Back to EduIDE' }).getAttribute('href')).toBe('/');
    });

    it('takes the stored values once, even when React runs the effect twice', async () => {
        stubLocation();
        sessionStorage.setItem('eduide.gitCallback', JSON.stringify({ state: 's1', code: 'c1' }));
        const POST = vi.fn(async () => ({ data: { provider: 'gh', login: 'alex', returnTo: '/' } }));
        render(
            <I18nProvider initial='en'>
                <GitCallback api={apiWith({ POST })} />
            </I18nProvider>
        );
        await flush();
        expect(POST).toHaveBeenCalledTimes(1);
        expect(sessionStorage.getItem('eduide.gitCallback')).toBeNull();
    });

    it('says so when there is nothing to complete (a reload after the values were used)', async () => {
        render(wrap(<GitCallback api={apiWith({ POST: vi.fn() })} />));
        await screen.findByText('There is nothing to complete. Start the connection again.');
    });
});

describe('GitGate', () => {
    const provider = { name: 'gh', displayName: 'GitHub' };

    it('asks to connect, names the account and offers no way to start anyway', () => {
        render(wrap(<GitGate api={apiWith({})} gate={{ kind: 'connect', provider }} onBack={vi.fn()} />));
        screen.getByText('This repository needs your GitHub account');
        screen.getByText(/may also not exist/);
        screen.getByRole('button', { name: 'Connect' });
        expect(screen.queryByRole('button', { name: /anyway/i })).toBeNull();
    });

    it('connects and returns to the same link', async () => {
        window.history.replaceState({}, '', '/?appDef=java-17&gitUri=https%3A%2F%2Fgithub.com%2Fa%2Fb');
        const POST = vi.fn(async () => ({ error: { code: 'internal' } }));
        render(wrap(<GitGate api={apiWith({ POST })} gate={{ kind: 'connect', provider }} onBack={vi.fn()} />));
        fireEvent.click(screen.getByRole('button', { name: 'Connect' }));
        await waitFor(() => expect(POST).toHaveBeenCalled());
        expect(POST).toHaveBeenCalledWith('/git/providers/{name}/connect', {
            params: { path: { name: 'gh' } },
            body: { returnTo: '/?appDef=java-17&gitUri=https%3A%2F%2Fgithub.com%2Fa%2Fb' }
        });
    });

    it('names the account on no access and offers to switch accounts, or to go back', async () => {
        const DELETE = vi.fn(async () => ({ response: { status: 204 } }));
        const POST = vi.fn(async () => ({ error: { code: 'internal' } }));
        const onBack = vi.fn();
        render(wrap(<GitGate api={apiWith({ DELETE, POST })} gate={{ kind: 'no-access', provider, login: 'alex' }} onBack={onBack} />));
        screen.getByText(/your GitHub account alex has no access/);
        fireEvent.click(screen.getByRole('button', { name: 'Back' }));
        expect(onBack).toHaveBeenCalled();
        fireEvent.click(screen.getByRole('button', { name: 'Disconnect and connect another account' }));
        await waitFor(() => expect(POST).toHaveBeenCalled());
        expect(DELETE).toHaveBeenCalledWith('/git/providers/{name}', { params: { path: { name: 'gh' } } });
    });
});

describe('RepoPicker', () => {
    const tpl = (name: string): Template => ({
        name,
        displayName: name,
        listed: true,
        persistence: ['none'],
        defaultPersistence: 'none',
        injectableKeys: ['GIT_URI']
    });

    it('names the repository and starts the chosen environment', () => {
        const onPick = vi.fn();
        render(wrap(<RepoPicker repository='github.com/a/b' templates={[tpl('java'), tpl('python')]} onPick={onPick} onBack={vi.fn()} />));
        screen.getByRole('heading', { name: 'Open github.com/a/b' });
        fireEvent.click(screen.getByRole('button', { name: 'Start python' }));
        expect(onPick).toHaveBeenCalledWith(expect.objectContaining({ name: 'python' }));
    });

    it('says so when no environment takes a repository, and when the link named an unknown one', () => {
        render(wrap(<RepoPicker repository='github.com/a/b' unknownName='nope' templates={[]} onPick={vi.fn()} onBack={vi.fn()} />));
        screen.getByText('The environment nope from the link does not exist.');
        screen.getByText('No environment can open a repository.');
    });
});

describe('GitApproval', () => {
    const request = { session: 's-abc', repository: 'https://github.com/org/repo', access: 'write' as const };
    const session = {
        name: 's-abc',
        template: 'java-17',
        phase: 'Ready',
        createdAt: new Date().toISOString(),
        display: { title: 'Exercise 1' }
    };
    const repos = {
        mode: 'write',
        maxPerSession: 20,
        sessionRepository: 'https://github.com/org/start',
        approved: [{ repository: 'https://github.com/org/old', access: 'read', approvedAt: '2026-01-01T00:00:00Z' }]
    };
    const clientWith = (over: Record<string, unknown> = {}): Record<string, ReturnType<typeof vi.fn>> =>
        ({
            GET: vi.fn(async (path: string) => (path === '/sessions/{name}' ? { data: session } : { data: repos })),
            PUT: vi.fn(async () => ({ data: { repository: request.repository, access: 'write', approvedAt: 'x' } })),
            DELETE: vi.fn(async () => ({ response: { status: 204 } })),
            POST: vi.fn(async () => ({ error: { code: 'internal' } })),
            ...over
        }) as Record<string, ReturnType<typeof vi.fn>>;

    it('shows session, repository, access and the warning, and approves nothing before the click', async () => {
        const client = clientWith();
        render(wrap(<GitApproval api={apiWith(client)} request={request} providers={[gh]} />));
        await screen.findByText(/Exercise 1/);
        expect(screen.getByTestId('approve-repository').textContent).toBe('https://github.com/org/repo');
        expect(screen.getByTestId('approve-access').textContent).toBe('Read and push');
        screen.getByText('Only allow this if you just ran a Git command in this environment yourself.');
        await screen.findByText('https://github.com/org/start');
        expect(client.PUT).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole('button', { name: 'Decline' }));
        expect(client.PUT).not.toHaveBeenCalled();
        screen.getByText('Nothing was approved. You can close this page.');
    });

    it('approves on Allow and tells the student to repeat the Git command', async () => {
        const client = clientWith();
        render(wrap(<GitApproval api={apiWith(client)} request={request} providers={[]} />));
        await screen.findByText('https://github.com/org/start');
        fireEvent.click(screen.getByRole('button', { name: 'Allow' }));
        await screen.findByText(/Repeat the Git command in your IDE/);
        expect(client.PUT).toHaveBeenCalledWith('/sessions/{name}/git/repositories', {
            params: { path: { name: 's-abc' } },
            body: { repository: 'https://github.com/org/repo', access: 'write' }
        });
    });

    it('lists the approved repositories and removes one', async () => {
        const client = clientWith();
        render(wrap(<GitApproval api={apiWith(client)} request={request} providers={[]} />));
        await screen.findByText('https://github.com/org/old');
        fireEvent.click(screen.getByRole('button', { name: 'Remove https://github.com/org/old' }));
        await waitFor(() =>
            expect(client.DELETE).toHaveBeenCalledWith('/sessions/{name}/git/repositories', {
                params: { path: { name: 's-abc' } },
                body: { repository: 'https://github.com/org/old' }
            })
        );
    });

    it.each([
        ['git-approval-disabled', /does not allow approving more/],
        ['git-access-not-allowed', /does not allow this kind of access/],
        ['git-approval-limit', /reached the limit/],
        ['git-not-a-provider', /not a Git provider/],
        ['not-found', /does not exist, is not yours/],
        ['rate-limited', /Too many requests/]
    ])('shows %s as a clear message', async (code, text) => {
        const client = clientWith({ PUT: vi.fn(async () => ({ error: { code } })) });
        render(wrap(<GitApproval api={apiWith(client)} request={request} providers={[]} />));
        await screen.findByText('https://github.com/org/start');
        fireEvent.click(screen.getByRole('button', { name: 'Allow' }));
        expect((await screen.findByRole('alert')).textContent).toMatch(text);
    });

    it('offers Connect when no account exists, for the provider of the repository host', async () => {
        const client = clientWith({ PUT: vi.fn(async () => ({ error: { code: 'git-not-connected' } })) });
        render(wrap(<GitApproval api={apiWith(client)} request={request} providers={[gh, gl]} />));
        await screen.findByText('https://github.com/org/start');
        fireEvent.click(screen.getByRole('button', { name: 'Allow' }));
        fireEvent.click(await screen.findByRole('button', { name: 'Connect GitHub' }));
        await waitFor(() =>
            expect(client.POST).toHaveBeenCalledWith(
                '/git/providers/{name}/connect',
                expect.objectContaining({ params: { path: { name: 'gh' } } })
            )
        );
    });

    it('does not allow anything for an unknown or foreign session', async () => {
        const client = clientWith({ GET: vi.fn(async () => ({ error: { code: 'not-found' } })) });
        render(wrap(<GitApproval api={apiWith(client)} request={request} providers={[]} />));
        await screen.findByRole('alert');
        expect((screen.getByRole('button', { name: 'Allow' }) as HTMLButtonElement).disabled).toBe(true);
    });

    it('does not allow anything when approval is off', async () => {
        const client = clientWith({
            GET: vi.fn(async (path: string) => (path === '/sessions/{name}' ? { data: session } : { data: { ...repos, mode: 'off' } }))
        });
        render(wrap(<GitApproval api={apiWith(client)} request={request} providers={[]} />));
        await screen.findByText(/does not allow approving more/);
        expect((screen.getByRole('button', { name: 'Allow' }) as HTMLButtonElement).disabled).toBe(true);
    });

    it('renders an invalid link as an error and calls nothing', () => {
        const client = clientWith();
        render(wrap(<GitApproval api={apiWith(client)} request='invalid' providers={[]} />));
        screen.getByText('This link is not valid. Nothing was approved.');
        expect(screen.queryByRole('button', { name: 'Allow' })).toBeNull();
        expect(client.GET).not.toHaveBeenCalled();
    });

    it('renders hostile text as text', async () => {
        const hostile = {
            ...session,
            display: { title: '<img src=x onerror="window.__pwned=1">' }
        };
        const client = clientWith({
            GET: vi.fn(async (path: string) => (path === '/sessions/{name}' ? { data: hostile } : { data: repos }))
        });
        const { container } = render(wrap(<GitApproval api={apiWith(client)} request={request} providers={[]} />));
        await screen.findByText(/onerror/);
        expect(container.querySelector('img')).toBeNull();
    });
});
