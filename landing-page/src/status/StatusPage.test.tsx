import { act, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { PublicStatus } from '../apiTypes';
import type { AppConfig } from '../config';
import { I18nProvider } from '../I18nProvider';
import { hero, wait } from './level';
import { StatusPage } from './StatusPage';

const config: AppConfig = {
    apiBaseUrl: '/api/v1',
    keycloak: { url: 'https://kc.example.org', realm: 'r', clientId: 'c' },
    legal: { imprint: { url: 'https://example.org/i' }, privacy: { url: 'https://example.org/p' } }
};

const status = (over: Partial<PublicStatus> = {}): PublicStatus => ({
    overall: 'ok',
    updatedAt: new Date().toISOString(),
    parts: { signIn: 'ok', starting: 'ok', workspaces: 'ok' },
    templates: [
        { name: 'java-17', displayName: 'Java 17', warm: true, startEstimate: { none: { seconds: 0.4 }, volume: { seconds: 25 } } },
        { name: 'rust', displayName: 'Rust', warm: false, startEstimate: { volume: { seconds: 75 } } },
        { name: 'empty', displayName: 'Nothing', warm: false, startEstimate: {} }
    ],
    ...over
});

const answer = (body: PublicStatus | number): void => {
    vi.stubGlobal(
        'fetch',
        vi.fn(async () => (typeof body === 'number' ? new Response('', { status: body }) : new Response(JSON.stringify(body))))
    );
};
const renderPage = async (): Promise<void> => {
    render(
        <I18nProvider initial='en'>
            <StatusPage config={config} />
        </I18nProvider>
    );
    await act(async () => {});
};

afterEach(() => vi.useRealTimers());

describe('hero and wait', () => {
    it('names the worst part and uses plain language', () => {
        expect(hero(status()).title).toBe('hero.ok');
        expect(hero(status({ overall: 'slow' })).tone).toBe('warn');
        expect(hero(status({ overall: 'down', parts: { signIn: 'ok', starting: 'down', workspaces: 'ok' } })).title).toBe(
            'hero.downStarting'
        );
        expect(hero(status({ overall: 'down', parts: { signIn: 'down', starting: 'ok', workspaces: 'ok' } })).title).toBe('hero.downOther');
    });
    it('says instantly below two seconds, seconds, then minutes', () => {
        expect(wait(0.4)).toEqual({ kind: 'instant' });
        expect(wait(25.4)).toEqual({ kind: 'seconds', n: 25 });
        expect(wait(75)).toEqual({ kind: 'minutes', n: 1 });
    });
});

describe('StatusPage', () => {
    it('shows the overall state, the three parts and start times split by workspace kind', async () => {
        answer(status());
        await renderPage();
        screen.getByRole('heading', { name: 'Everything is working' });
        screen.getByText('Sign in');
        screen.getByText('Starting environments');
        screen.getByText('Saved workspaces');
        expect(screen.getAllByText('Working')).toHaveLength(3);
        const java = screen.getByText('Java 17').closest('.row') as HTMLElement;
        within(java).getByText('Instantly');
        within(java).getByText('About 25 s');
        within(java).getByText('Ready environments available');
        const rust = screen.getByText('Rust').closest('.row') as HTMLElement;
        within(rust).getByText('Prepared when you start it');
        within(rust).getByText('About 1 min');
        expect(within(rust).queryByText('Temporary')).toBeNull();
        expect(screen.queryByText('Nothing')).toBeNull();
        expect(fetch).toHaveBeenCalledWith('/api/v1/status');
    });

    it('shows no counts, codes or announcements', async () => {
        answer(status());
        await renderPage();
        expect(document.body.textContent).not.toMatch(/pool|instance|error code|incident/i);
    });

    it('shows a slow and an outage state', async () => {
        answer(status({ overall: 'slow', parts: { signIn: 'ok', starting: 'slow', workspaces: 'ok' } }));
        await renderPage();
        screen.getByRole('heading', { name: 'Starting takes longer right now' });
        screen.getByText('Slower');
    });

    it('says the status is not available when the request fails', async () => {
        answer(503);
        await renderPage();
        screen.getByRole('heading', { name: 'Status is not available right now' });
        expect(screen.queryByText('Sign in')).toBeNull();
    });

    it('has the language switch, the way back and the shared footer', async () => {
        answer(status());
        await renderPage();
        expect(screen.getByRole('link', { name: 'Back to EduIDE' }).getAttribute('href')).toBe('/');
        screen.getByRole('button', { name: 'Language' });
        screen.getByRole('link', { name: 'Status' });
        screen.getByRole('link', { name: 'Imprint' });
    });

    it('refreshes every 30 seconds and stops when it unmounts', async () => {
        vi.useFakeTimers();
        answer(status());
        const { unmount } = render(
            <I18nProvider initial='en'>
                <StatusPage config={config} />
            </I18nProvider>
        );
        await act(async () => {
            await vi.advanceTimersByTimeAsync(0);
        });
        expect(fetch).toHaveBeenCalledTimes(1);
        await act(async () => {
            await vi.advanceTimersByTimeAsync(30_000);
        });
        expect(fetch).toHaveBeenCalledTimes(2);
        unmount();
        await vi.advanceTimersByTimeAsync(60_000);
        expect(fetch).toHaveBeenCalledTimes(2);
    });
});
