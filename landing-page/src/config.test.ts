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
});
