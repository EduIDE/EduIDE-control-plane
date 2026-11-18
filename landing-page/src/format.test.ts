import { describe, expect, it } from 'vitest';

import { formatAgo, formatClock } from './format';

describe('formatClock', () => {
    it('rounds down to five minutes, because the service may stop up to five minutes early', () => {
        // Local time zone of the test run: compare against the same rounding done by hand.
        const at = new Date(2026, 5, 10, 15, 7, 59);
        expect(formatClock(at.toISOString(), 'en')).toBe('15:05');
        expect(formatClock(new Date(2026, 5, 10, 15, 4, 0).toISOString(), 'de')).toBe('15:00');
        expect(formatClock(new Date(2026, 5, 10, 0, 2, 0).toISOString(), 'en')).toBe('00:00');
    });
});

describe('formatAgo', () => {
    const now = Date.parse('2026-06-10T12:00:00Z');
    const ago = (ms: number, lang: 'en' | 'de' = 'en'): string => formatAgo(new Date(now - ms).toISOString(), now, lang);
    it.each([
        [30 * 1000, 'now'],
        [12 * 60 * 1000, '12 minutes ago'],
        [3 * 60 * 60 * 1000, '3 hours ago'],
        [11 * 24 * 60 * 60 * 1000, '11 days ago']
    ])('%i ms', (ms, text) => {
        expect(ago(ms)).toBe(text);
    });
    it('speaks German', () => {
        expect(ago(5 * 24 * 60 * 60 * 1000, 'de')).toBe('vor 5 Tagen');
    });
});
