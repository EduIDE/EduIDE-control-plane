import { describe, expect, it, vi } from 'vitest';

import type { Api } from './api';
import {
    captureGitCallback,
    completeCallback,
    currentReturnTo,
    loadProviders,
    normalizeRepository,
    parseApproval,
    preflight,
    safeReturnTo,
    startConnect,
    takeGitCallback
} from './git';

const apiWith = (client: Record<string, unknown>): Api => ({ client }) as unknown as Api;
const store = (): Storage => {
    sessionStorage.clear();
    return sessionStorage;
};

describe('safeReturnTo', () => {
    it.each([
        ['/', '/'],
        ['/?appDef=java&gitUri=https%3A%2F%2Fh%2Fa', '/?appDef=java&gitUri=https%3A%2F%2Fh%2Fa'],
        ['/status', '/status'],
        ['//evil.example', '/'],
        ['/\\evil.example', '/'],
        ['https://evil.example/', '/'],
        ['javascript:alert(1)', '/'],
        ['evil', '/'],
        ['', '/'],
        ['/a\nb', '/'],
        ['/a\u0000b', '/'],
        ['/a\\b', '/'],
        ['/' + 'x'.repeat(600), '/'],
        [undefined, '/']
    ])('%j becomes %j', (input, want) => {
        expect(safeReturnTo(input)).toBe(want);
    });

    it('keeps the pending start link of the current page', () => {
        expect(currentReturnTo({ pathname: '/', search: '?appDef=java-17&gitUri=https%3A%2F%2Fh%2Fa' })).toBe(
            '/?appDef=java-17&gitUri=https%3A%2F%2Fh%2Fa'
        );
    });
});

describe('captureGitCallback', () => {
    it('moves code and state out of the address and takes them once', () => {
        const s = store();
        expect(captureGitCallback({ pathname: '/git/callback', search: '?code=c1&state=s1' }, s)).toBe('/git/callback');
        expect(takeGitCallback(s)).toEqual({ state: 's1', code: 'c1', error: undefined });
        expect(takeGitCallback(s)).toBeUndefined();
    });

    it('keeps the error of the host', () => {
        const s = store();
        captureGitCallback({ pathname: '/git/callback/', search: '?error=access_denied&state=s1' }, s);
        expect(takeGitCallback(s)).toEqual({ state: 's1', code: undefined, error: 'access_denied' });
    });

    it('removes the query even when nothing usable is in it, and stores nothing', () => {
        const s = store();
        expect(captureGitCallback({ pathname: '/git/callback', search: '?code=c1' }, s)).toBe('/git/callback');
        expect(captureGitCallback({ pathname: '/git/callback', search: '?state=' + 'x'.repeat(3000) + '&code=c' }, s)).toBe(
            '/git/callback'
        );
        expect(takeGitCallback(s)).toBeUndefined();
    });

    it('ignores other paths', () => {
        const s = store();
        expect(captureGitCallback({ pathname: '/', search: '?code=c1&state=s1' }, s)).toBeUndefined();
        expect(s.length).toBe(0);
    });
});

describe('completeCallback', () => {
    it('answers the validated return path on success', async () => {
        const POST = vi.fn(async () => ({ data: { provider: 'gh', login: 'alex', returnTo: '//evil' } }));
        const r = await completeCallback(apiWith({ POST }), { state: 's', code: 'c' });
        expect(r).toEqual({ ok: true, provider: 'gh', login: 'alex', returnTo: '/' });
        expect(POST).toHaveBeenCalledWith('/git/callback', { body: { state: 's', code: 'c' } });
    });

    it('sends error and state, never a code, when the host reported an error', async () => {
        const POST = vi.fn(async () => ({ error: { code: 'git-denied' } }));
        const r = await completeCallback(apiWith({ POST }), { state: 's', code: 'c', error: 'access_denied' });
        expect(r).toEqual({ ok: false, code: 'git-denied' });
        expect(POST).toHaveBeenCalledWith('/git/callback', { body: { state: 's', error: 'access_denied' } });
    });

    it('reports a network error', async () => {
        const r = await completeCallback(apiWith({ POST: vi.fn().mockRejectedValue(new Error('x')) }), { state: 's', code: 'c' });
        expect(r).toEqual({ ok: false, code: 'unreachable' });
    });
});

describe('providers and connect', () => {
    it('lists only entries shaped like providers, and nothing on failure', async () => {
        const ok = apiWith({
            GET: async () => ({
                data: [
                    { name: 'a', host: 'h', connected: false },
                    { name: 'java', displayName: 'Java' }
                ]
            })
        });
        expect(await loadProviders(ok)).toHaveLength(1);
        expect(await loadProviders(apiWith({ GET: async () => ({ error: { status: 404 } }) }))).toEqual([]);
        expect(await loadProviders(apiWith({}))).toEqual([]);
    });

    it('posts the return path and navigates to the authorize URL', async () => {
        const POST = vi.fn(async () => ({ data: { authorizeUrl: 'https://git.example.org/oauth/authorize?x=1' } }));
        const go = vi.fn();
        expect(await startConnect(apiWith({ POST }), 'gh', '/?appDef=a', go)).toBeUndefined();
        expect(POST).toHaveBeenCalledWith('/git/providers/{name}/connect', {
            params: { path: { name: 'gh' } },
            body: { returnTo: '/?appDef=a' }
        });
        expect(go).toHaveBeenCalledWith('https://git.example.org/oauth/authorize?x=1');
    });

    it('never navigates to a non-http address and reports the problem code', async () => {
        const go = vi.fn();
        expect(await startConnect(apiWith({ POST: async () => ({ data: { authorizeUrl: 'javascript:alert(1)' } }) }), 'gh', '/', go)).toBe(
            'internal'
        );
        expect(await startConnect(apiWith({ POST: async () => ({ error: { code: 'rate-limited' } }) }), 'gh', '/', go)).toBe(
            'rate-limited'
        );
        expect(go).not.toHaveBeenCalled();
    });
});

describe('preflight', () => {
    it('answers the result', async () => {
        const data = { result: 'connect', provider: { name: 'gh', displayName: 'GitHub' } };
        expect(await preflight(apiWith({ POST: async () => ({ data }) }), 'https://h/a')).toEqual(data);
    });

    it.each([
        ['404 of an older service', async () => ({ error: { status: 404 } })],
        ['rate limit', async () => ({ error: { code: 'rate-limited' } })],
        ['network error', async () => Promise.reject(new Error('down'))]
    ])('fails open on %s', async (_name, POST) => {
        expect(await preflight(apiWith({ POST }), 'https://h/a')).toBeUndefined();
    });
});

describe('parseApproval', () => {
    const link = (extra: string): string => `?session=s-abc&access=write&approveRepo=${encodeURIComponent(extra)}`;

    it('is no approval without approveRepo', () => {
        expect(parseApproval('?session=s&access=read')).toBeUndefined();
        expect(parseApproval('')).toBeUndefined();
    });

    it('shows the repository the way the service compares it', () => {
        expect(parseApproval(link('https://GitHub.com/Org/Repo.git/'))).toEqual({
            session: 's-abc',
            access: 'write',
            repository: 'https://github.com/Org/Repo'
        });
    });

    it.each([
        ['credentials', 'https://u:p@h/a/b'],
        ['http', 'http://h/a/b'],
        ['javascript scheme', 'javascript:alert(1)'],
        ['query', 'https://h/a/b?x=1'],
        ['fragment', 'https://h/a/b#x'],
        ['port', 'https://h:8443/a/b'],
        ['no path', 'https://h/'],
        ['backslash', 'https://h/a\\b'],
        ['control character', 'https://h/a\nb'],
        ['space', 'https://h/a b'],
        ['percent escape', 'https://h/a/%73ecret'],
        ['dot git segment', 'https://h/a/b/.git/info'],
        ['double git suffix', 'https://h/a/b.git.git'],
        ['git suffix in the middle', 'https://h/a.git/b'],
        ['one segment', 'https://h/a'],
        ['hidden segment', 'https://h/a/.b'],
        ['too deep', 'https://h/a/b/c/d/e/f/g/h/i/j/k']
    ])('refuses a repository with %s', (_n, repo) => {
        expect(parseApproval(link(repo))).toBe('invalid');
    });

    it.each(['', 'S_ABC', '../x', 'a/b', '-a', 'a'.repeat(64)])('refuses the session name %j', name => {
        expect(parseApproval(`?session=${encodeURIComponent(name)}&access=read&approveRepo=https%3A%2F%2Fh%2Fa%2Fb`)).toBe('invalid');
    });

    it.each([undefined, '', 'admin', 'READ', 'read,write'])('refuses access %j', access => {
        const a = access === undefined ? '' : `&access=${encodeURIComponent(access)}`;
        expect(parseApproval(`?session=s-abc${a}&approveRepo=https%3A%2F%2Fh%2Fa%2Fb`)).toBe('invalid');
    });

    it('punycodes a look-alike host so the user sees what the service sees', () => {
        expect(normalizeRepository('https://gíthub.com/a/b')).toBe('https://xn--gthub-zsa.com/a/b');
    });
});
