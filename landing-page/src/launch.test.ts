import { describe, expect, it } from 'vitest';

import type { Session, Template } from './api-client';
import { applySession, buildLaunchRequest, displaySource, failureFromProblem, newTraceparent, parseLaunchParams } from './launch';
import { runLaunch } from './runLaunch';

const template: Template = {
    name: 'java-17',
    displayName: 'Java',
    persistence: ['none', 'volume'],
    defaultPersistence: 'none',
    injectableKeys: ['GIT_URI', 'ARTEMIS_TOKEN']
};
const session = (over: Partial<Session>): Session => ({
    name: 's-1',
    template: 'java-17',
    phase: 'Pending',
    createdAt: '2026-01-01T00:00:00Z',
    ...over
});

describe('buildLaunchRequest', () => {
    it('sends only injectable keys and strips credentials from the display source', () => {
        const req = buildLaunchRequest(
            {
                appDef: 'java-17',
                gitUri: 'https://user:secret@git.example.org/a/ex01-alice.git',
                artemisToken: 'tok',
                artemisUrl: 'https://a.example.org',
                title: ' Ex 01 '
            },
            template
        );
        expect(req).toEqual({
            template: 'java-17',
            data: { GIT_URI: 'https://user:secret@git.example.org/a/ex01-alice.git', ARTEMIS_TOKEN: 'tok' },
            display: { title: 'Ex 01', source: 'https://git.example.org/a/ex01-alice.git' }
        });
        expect(JSON.stringify(req.display)).not.toContain('secret');
    });

    it('takes the template default persistence unless the param is valid for the template', () => {
        expect(buildLaunchRequest({ appDef: 'x' }, template)).toEqual({ template: 'java-17' });
        expect(buildLaunchRequest({ appDef: 'x', persistence: 'volume' }, template).persistence).toBe('volume');
        expect(
            buildLaunchRequest({ appDef: 'x', persistence: 'volume' }, { ...template, persistence: ['none'] }).persistence
        ).toBeUndefined();
        expect(buildLaunchRequest({ appDef: 'x', persistence: 'weird' }, template).persistence).toBeUndefined();
    });

    it('reads the link parameters', () => {
        expect(parseLaunchParams('?appDef=java-17&gitUri=g&title=T')).toMatchObject({ appDef: 'java-17', gitUri: 'g', title: 'T' });
        expect(parseLaunchParams('?gitUri=g')).toBeUndefined();
        expect(displaySource('not a url')).toBe('');
    });
});

describe('session state', () => {
    it('maps phases', () => {
        expect(applySession(session({ phase: 'Starting' })).kind).toBe('starting');
        expect(applySession(session({ phase: 'Ready', url: 'https://ide.example.org/abc/' }))).toEqual({
            kind: 'ready',
            url: 'https://ide.example.org/abc/'
        });
        expect(applySession(session({ phase: 'Ready', url: 'javascript:alert(1)' })).kind).toBe('failed');
        expect(applySession(session({ phase: 'Failed', errorCode: 'capacity-exhausted' }))).toMatchObject({
            kind: 'failed',
            code: 'capacity-exhausted'
        });
    });

    it('names the problem code and lists sessions at the limit', () => {
        const s = failureFromProblem({ code: 'session-limit-reached', sessions: [session({})] });
        expect(s).toMatchObject({ kind: 'failed', code: 'session-limit-reached' });
        expect(s.kind === 'failed' && s.sessions).toHaveLength(1);
    });

    it('builds a traceparent', () => {
        expect(newTraceparent()).toMatch(/^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
    });
});

describe('runLaunch', () => {
    const noDelay = (): Promise<void> => Promise.resolve();

    it('follows events to Ready', async () => {
        const kinds: string[] = [];
        await runLaunch(
            {
                launch: async () => ({ session: session({ phase: 'Pending' }) }),
                watch: async (_n, on) => {
                    on(session({ phase: 'Starting' }));
                    on(session({ phase: 'Ready', url: 'https://ide.example.org/abc/' }));
                },
                delay: noDelay
            },
            { template: 'java-17' },
            s => kinds.push(s.kind),
            new AbortController().signal
        );
        expect(kinds).toEqual(['creating', 'starting', 'starting', 'ready']);
    });

    it('reconnects after a dropped stream', async () => {
        let calls = 0;
        const kinds: string[] = [];
        await runLaunch(
            {
                launch: async () => ({ session: session({}) }),
                watch: async (_n, on) => {
                    calls += 1;
                    if (calls === 1) {
                        throw new Error('network');
                    }
                    on(session({ phase: 'Failed', errorCode: 'capacity-exhausted' }));
                },
                delay: noDelay
            },
            { template: 't' },
            s => kinds.push(s.kind),
            new AbortController().signal
        );
        expect(calls).toBe(2);
        expect(kinds[kinds.length - 1]).toBe('failed');
    });

    it('fails on a problem response and gives up when the stream never recovers', async () => {
        let last = '';
        await runLaunch(
            { launch: async () => ({ problem: { code: 'capacity-exhausted' } }), watch: async () => undefined, delay: noDelay },
            { template: 't' },
            s => (last = s.kind === 'failed' ? s.code : s.kind),
            new AbortController().signal
        );
        expect(last).toBe('capacity-exhausted');
        await runLaunch(
            { launch: async () => ({ session: session({}) }), watch: async () => undefined, delay: noDelay },
            { template: 't' },
            s => (last = s.kind === 'failed' ? s.message : s.kind),
            new AbortController().signal
        );
        expect(last).toContain('Lost the connection');
    });
});
