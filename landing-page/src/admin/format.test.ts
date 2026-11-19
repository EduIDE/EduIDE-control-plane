import { describe, expect, it } from 'vitest';

import { duration, formatBytes } from './format';
import { adminPage, adminPath } from './routes';

describe('duration', () => {
    it.each([
        [45, 45, 's'],
        [3000, 50, 'min'],
        [3600 * 24, 24, 'h'],
        [14 * 86400, 14, 'days']
    ])('%i s', (seconds, n, unit) => {
        expect(duration(seconds)).toEqual({ n, unit });
    });
});

describe('formatBytes', () => {
    it('uses binary units', () => {
        expect(formatBytes(2 * 1024 ** 3)).toBe('2.0 GiB');
        expect(formatBytes(512)).toBe('512 B');
        expect(formatBytes(2.4 * 1024 ** 4)).toBe('2.4 TiB');
        expect(formatBytes(120 * 1024 ** 3)).toBe('120 GiB');
    });
});

describe('admin routes', () => {
    it('maps paths to pages and back, unknown paths to the status page', () => {
        expect(adminPage('/admin')).toBe('status');
        expect(adminPage('/admin/')).toBe('status');
        expect(adminPage('/admin/pools')).toBe('pools');
        expect(adminPage('/admin/sessions/')).toBe('sessions');
        expect(adminPage('/admin/nope')).toBe('status');
        expect(adminPath('limits')).toBe('/admin/limits');
        expect(adminPath('status')).toBe('/admin');
    });
});
