import { describe, expect, it } from 'vitest';

import { appRoute } from './routes';

describe('appRoute', () => {
    it.each([
        ['/', { kind: 'home' }],
        ['/me/overview', { kind: 'home' }],
        ['/imprint', { kind: 'legal', route: 'imprint' }],
        ['/privacy/', { kind: 'legal', route: 'privacy' }],
        ['/status', { kind: 'status' }],
        ['/status/', { kind: 'status' }],
        ['/admin', { kind: 'admin' }],
        ['/admin/pools', { kind: 'admin' }],
        ['/administrator', { kind: 'home' }]
    ])('%s', (path, route) => {
        expect(appRoute(path)).toEqual(route);
    });
});
