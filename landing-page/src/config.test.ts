import { describe, expect, it } from 'vitest';

import { parseConfig } from './config';

const base = {
    apiBaseUrl: '/api/v1',
    keycloak: { url: 'https://kc.example.org', realm: 'r', clientId: 'landing' },
    legal: { imprint: { url: 'https://example.org/imprint' }, privacy: { path: '/legal/privacy.md' } }
};

describe('parseConfig', () => {
    it('accepts the documented shape', () => {
        const c = parseConfig({ ...base, title: 'EduIDE', legacy: false });
        expect(c.apiBaseUrl).toBe('/api/v1');
        expect(c.legal).toEqual(base.legal);
    });

    it('rejects an incomplete one', () => {
        expect(() => parseConfig({ apiBaseUrl: '/api/v1', keycloak: { url: 'x' } })).toThrow();
        expect(() => parseConfig(undefined)).toThrow();
    });

    it('requires both legal pages', () => {
        expect(() => parseConfig({ ...base, legal: undefined })).toThrow('legal.imprint');
        expect(() => parseConfig({ ...base, legal: { imprint: base.legal.imprint } })).toThrow('legal.privacy');
    });

    it.each(['javascript:alert(1)', '/imprint', 'not a url', 42])('rejects the legal url %s', bad => {
        expect(() => parseConfig({ ...base, legal: { ...base.legal, imprint: { url: bad } } })).toThrow('legal.imprint.url');
    });

    it.each(['/etc/passwd', '/legal/../config.json', 'legal/privacy.md', '/legal/imprint.md', '/legal/other.md', 7])(
        'rejects the legal path %s',
        bad => {
            expect(() => parseConfig({ ...base, legal: { ...base.legal, privacy: { path: bad } } })).toThrow('legal.privacy.path');
        }
    );

    it('rejects an entry with both or neither of url and path', () => {
        const both = { url: 'https://example.org/x', path: '/legal/imprint.md' };
        expect(() => parseConfig({ ...base, legal: { ...base.legal, imprint: both } })).toThrow('exactly one');
        expect(() => parseConfig({ ...base, legal: { ...base.legal, imprint: {} } })).toThrow('exactly one');
    });

    it('accepts http(s) footer links', () => {
        expect(parseConfig({ ...base, footerLinks: { github: { url: 'https://github.com/x/y' } } }).footerLinks?.github?.url).toBe(
            'https://github.com/x/y'
        );
    });

    it.each(['github', 'bugReport', 'featureRequest', 'about'])('rejects a footer link %s that is not http(s)', key => {
        expect(() => parseConfig({ ...base, footerLinks: { [key]: { text: 't', url: 'javascript:1' } } })).toThrow(`footerLinks.${key}`);
    });
});
