import { describe, expect, it } from 'vitest';

import { toRows } from './overview';

const now = Date.parse('2026-06-10T00:00:00Z');
const base = { template: 'java-17', templateDisplayName: 'Java' };

describe('toRows', () => {
    const rows = toRows(
        {
            items: [
                {
                    ...base,
                    kind: 'workspace',
                    title: 'ex01',
                    running: false,
                    lastUsed: '2026-06-01T00:00:00Z',
                    expiresAt: '2026-06-12T00:00:00Z',
                    workspace: { name: 'w-1', template: 'java-17', phase: 'Ready', createdAt: '2026-05-01T00:00:00Z' }
                },
                { ...base, kind: 'workspace', title: 'ex02', running: false, expiresAt: '2026-07-01T00:00:00Z' },
                {
                    ...base,
                    kind: 'session',
                    title: '',
                    running: true,
                    lastUsed: '2026-06-09T00:00:00Z',
                    session: { name: 's-1', template: 'java-17', phase: 'Ready', createdAt: '2026-06-09T00:00:00Z', url: 'https://x/' }
                }
            ]
        },
        now
    );

    it('lists running sessions first and offers the actions of G15', () => {
        expect(rows.map(r => r.actions)).toEqual([
            ['open', 'stop'],
            ['start', 'delete'],
            ['start', 'delete']
        ]);
        expect(rows[0].key).toBe('session:s-1');
    });

    it('uses the template name when the title is empty, never a technical name', () => {
        expect(rows[0].title).toBe('Java');
        expect(rows[0].subtitle).toBe('Java');
    });

    it('warns three days before expiry only', () => {
        const byTitle = Object.fromEntries(rows.map(r => [r.title, r]));
        expect(byTitle.ex01.expiresSoon).toBe('2026-06-12T00:00:00Z');
        expect(byTitle.ex02.expiresSoon).toBeUndefined();
        expect(rows[0].expiresSoon).toBeUndefined();
    });
});
