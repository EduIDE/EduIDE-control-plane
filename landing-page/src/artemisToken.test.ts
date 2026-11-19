import { beforeEach, describe, expect, it, vi } from 'vitest';

import { captureArtemisToken, clearArtemisToken, readArtemisToken, readGitUri } from './artemisToken';

describe('artemis token fragment', () => {
    beforeEach(() => sessionStorage.clear());

    it('moves the token into storage and returns the URL without the fragment', () => {
        const url = captureArtemisToken({ hash: '#artemisToken=abc%2B1', search: '?appDef=java&gitUri=x', pathname: '/' }, sessionStorage);
        expect(url).toBe('/?appDef=java&gitUri=x');
        expect(sessionStorage.getItem('eduide.artemisToken')).toBe('abc+1');
    });

    it('keeps other fragment parameters', () => {
        const url = captureArtemisToken({ hash: '#artemisToken=t&x=1', search: '', pathname: '/' }, sessionStorage);
        expect(url).toBe('/#x=1');
    });

    it('does nothing without a token', () => {
        expect(captureArtemisToken({ hash: '#state=1', search: '', pathname: '/' }, sessionStorage)).toBeUndefined();
        expect(sessionStorage.length).toBe(0);
    });

    it('reads the stored token, then clears it', () => {
        sessionStorage.setItem('eduide.artemisToken', 't');
        expect(readArtemisToken('', sessionStorage)).toBe('t');
        clearArtemisToken(sessionStorage);
        expect(readArtemisToken('', sessionStorage)).toBeUndefined();
    });

    it('moves the deprecated query token out of the URL, so it never reaches Keycloak in the redirect URI', () => {
        const url = captureArtemisToken({ hash: '', search: '?appDef=java&artemisToken=q%2B1&title=T', pathname: '/' }, sessionStorage);
        expect(url).toBe('/?appDef=java&title=T');
        expect(readArtemisToken('?appDef=java&title=T', sessionStorage)).toBe('q+1');
    });

    it('prefers the fragment token over the query token', () => {
        const url = captureArtemisToken({ hash: '#artemisToken=f', search: '?artemisToken=q', pathname: '/' }, sessionStorage);
        expect(url).toBe('/');
        expect(sessionStorage.getItem('eduide.artemisToken')).toBe('f');
    });

    it('moves a query gitUri with credentials out of the URL and leaves one without credentials', () => {
        const withCreds = captureArtemisToken(
            { hash: '', search: '?appDef=java&gitUri=https%3A%2F%2Fu%3Avcs%40git.example.org%2Fx.git', pathname: '/' },
            sessionStorage
        );
        expect(withCreds).toBe('/?appDef=java');
        expect(readGitUri('?appDef=java', sessionStorage)).toBe('https://u:vcs@git.example.org/x.git');
        sessionStorage.clear();
        expect(
            captureArtemisToken({ hash: '', search: '?appDef=java&gitUri=https%3A%2F%2Fgit.example.org%2Fx.git', pathname: '/' }, sessionStorage)
        ).toBeUndefined();
        expect(sessionStorage.length).toBe(0);
    });

    it('still reads the query parameter and logs a deprecation warning', () => {
        const warn = vi.fn();
        expect(readArtemisToken('?artemisToken=q', sessionStorage, warn)).toBe('q');
        expect(warn).toHaveBeenCalledOnce();
        expect(warn.mock.calls[0][0]).not.toContain('q');
    });

    it('moves gitUri with credentials out of the fragment, before Keycloak sees it', () => {
        const url = captureArtemisToken(
            {
                hash: '#artemisToken=t&gitUri=https%3A%2F%2Fu%3Avcs@artemis.example.org%2Fgit%2Fx.git',
                search: '?appDef=java',
                pathname: '/'
            },
            sessionStorage
        );
        expect(url).toBe('/?appDef=java');
        expect(readGitUri('?appDef=java', sessionStorage)).toBe('https://u:vcs@artemis.example.org/git/x.git');
        clearArtemisToken(sessionStorage);
        expect(sessionStorage.length).toBe(0);
    });

    it('captures a gitUri fragment without a token', () => {
        expect(captureArtemisToken({ hash: '#gitUri=https%3A%2F%2Fh%2Fr.git', search: '', pathname: '/' }, sessionStorage)).toBe('/');
        expect(readGitUri('', sessionStorage)).toBe('https://h/r.git');
    });

    it('prefers the fragment gitUri over the query one', () => {
        sessionStorage.setItem('eduide.gitUri', 'https://fragment/r.git');
        expect(readGitUri('?gitUri=https://query/r.git', sessionStorage)).toBe('https://fragment/r.git');
    });

    it('reads gitUri from the query, and warns only when it carries credentials', () => {
        const warn = vi.fn();
        expect(readGitUri('?gitUri=https://github.com/a/b.git', sessionStorage, warn)).toBe('https://github.com/a/b.git');
        expect(warn).not.toHaveBeenCalled();
        expect(readGitUri('?gitUri=https://u:secret@h/r.git', sessionStorage, warn)).toBe('https://u:secret@h/r.git');
        expect(warn).toHaveBeenCalledOnce();
        expect(warn.mock.calls[0][0]).not.toContain('secret');
    });

    it('strips the fragment even when storage refuses the value', () => {
        const store = {
            setItem: () => {
                throw new Error('quota');
            }
        } as unknown as Storage;
        const url = captureArtemisToken(
            { hash: '#artemisToken=t&gitUri=https%3A%2F%2Fu%3As@h%2Fr.git', search: '?appDef=c', pathname: '/' },
            store
        );
        expect(url).toBe('/?appDef=c');
    });
});
