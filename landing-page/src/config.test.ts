import { describe, expect, it } from 'vitest';

import { parseConfig } from './config';

describe('parseConfig', () => {
    it('accepts the documented shape', () => {
        const c = parseConfig({
            apiBaseUrl: '/api/v1',
            keycloak: { url: 'https://kc.example.org', realm: 'r', clientId: 'landing' },
            title: 'EduIDE',
            legacy: false
        });
        expect(c.apiBaseUrl).toBe('/api/v1');
    });
    it('rejects an incomplete one', () => {
        expect(() => parseConfig({ apiBaseUrl: '/api/v1', keycloak: { url: 'x' } })).toThrow();
        expect(() => parseConfig(undefined)).toThrow();
    });
    it('accepts http(s) legal URLs and none at all', () => {
        const base = { apiBaseUrl: '/api/v1', keycloak: { url: 'https://kc.example.org', realm: 'r', clientId: 'landing' } };
        expect(parseConfig(base).legal).toBeUndefined();
        const legal = { imprintUrl: 'https://example.org/imprint', privacyUrl: 'http://example.org/privacy' };
        expect(parseConfig({ ...base, legal }).legal).toEqual(legal);
    });
    it('rejects legal URLs that are not http(s)', () => {
        const base = { apiBaseUrl: '/api/v1', keycloak: { url: 'https://kc.example.org', realm: 'r', clientId: 'landing' } };
        for (const bad of ['javascript:alert(1)', '/imprint', 'not a url', 42]) {
            expect(() => parseConfig({ ...base, legal: { imprintUrl: bad } })).toThrow('legal.imprintUrl');
            expect(() => parseConfig({ ...base, legal: { privacyUrl: bad } })).toThrow('legal.privacyUrl');
        }
    });
});
