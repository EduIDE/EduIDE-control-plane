import { beforeEach, describe, expect, it, vi } from 'vitest';

import { captureArtemisToken, clearArtemisToken, readArtemisToken } from './artemisToken';

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

    it('still reads the query parameter and logs a deprecation warning', () => {
        const warn = vi.fn();
        expect(readArtemisToken('?artemisToken=q', sessionStorage, warn)).toBe('q');
        expect(warn).toHaveBeenCalledOnce();
        expect(warn.mock.calls[0][0]).not.toContain('q');
    });
});
