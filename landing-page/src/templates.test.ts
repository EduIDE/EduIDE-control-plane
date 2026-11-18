import { describe, expect, it } from 'vitest';

import type { Template } from './api-client';
import { buildLaunchInfo, startLine } from './templates';

const template: Template = {
    name: 'c',
    displayName: 'C',
    logoUrl: '/c.png',
    persistence: ['none', 'volume'],
    defaultPersistence: 'volume',
    injectableKeys: [],
    startEstimate: {
        none: { seconds: 1.2, strategy: 'cold', samples: 5 },
        volume: { seconds: 24.6, strategy: 'cold', samples: 5 }
    }
};

describe('startLine', () => {
    it('says instant for warm starts and below two seconds, about N otherwise, nothing without data', () => {
        expect(startLine({ seconds: 30, strategy: 'warm', samples: 1 })).toEqual({ kind: 'instant' });
        expect(startLine({ seconds: 1.9, strategy: 'cold', samples: 1 })).toEqual({ kind: 'instant' });
        expect(startLine({ seconds: 24.6, strategy: 'cold', samples: 1 })).toEqual({ kind: 'about', seconds: 25 });
        expect(startLine(undefined)).toBeUndefined();
    });
});

describe('buildLaunchInfo', () => {
    it('uses the estimate of the persistence the launch will get', () => {
        expect(buildLaunchInfo(template, { template: 'c' }).seconds).toBe(25);
        expect(buildLaunchInfo(template, { template: 'c', persistence: 'none' }).seconds).toBeUndefined();
        expect(buildLaunchInfo(template, { template: 'c', workspace: 'w' }).seconds).toBe(25);
    });
    it('prefers known names and falls back to the template, then the template id', () => {
        expect(buildLaunchInfo(template, { template: 'c', display: { title: 'Ex 1' } })).toMatchObject({
            title: 'Ex 1',
            templateName: 'C',
            logoUrl: '/c.png'
        });
        expect(buildLaunchInfo(undefined, { template: 'c' }, { title: 'Mine', templateName: 'C lang' })).toMatchObject({
            title: 'Mine',
            templateName: 'C lang'
        });
        expect(buildLaunchInfo(undefined, { template: 'c' })).toMatchObject({ title: 'c', templateName: 'c' });
    });
});
