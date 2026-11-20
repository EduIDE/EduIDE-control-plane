import { describe, expect, it, vi } from 'vitest';

import type { Api } from './api';
import type { Overview, Session, Template } from './api-client';
import { deleteRow, sessionToRow, toRow, toRows } from './overview';

const now = Date.parse('2026-06-10T12:00:00Z');
const base = { template: 'java-17', templateDisplayName: 'Java' };
const session = (over: Partial<Session> = {}): Session => ({
    name: 's-1',
    template: 'java-17',
    phase: 'Ready',
    createdAt: '2026-06-10T11:00:00Z',
    url: 'https://ide.example.org/s-1/',
    ...over
});
const workspace = { name: 'w-1', template: 'java-17', phase: 'Ready' as const, createdAt: '2026-05-01T00:00:00Z' };

describe('toRow', () => {
    it('shows a saved running session with its stop time and reason, grey', () => {
        const row = toRow(
            {
                ...base,
                kind: 'session',
                title: 'ex01',
                running: true,
                stopsAt: '2026-06-10T12:05:00Z',
                session: session({ workspace: 'w-1', stopReason: 'max', stopsAt: '2026-06-10T12:05:00Z' }),
                workspace
            },
            now
        );
        expect(row).toMatchObject({ saved: true, workspaceName: 'w-1', openUrl: 'https://ide.example.org/s-1/' });
        expect(row.life).toEqual({ kind: 'stops', at: '2026-06-10T12:05:00Z', reason: 'max' });
        expect(row.since).toEqual({ kind: 'started', at: '2026-06-10T11:00:00Z' });
        expect(row.danger).toBe(false);
    });

    it('turns red for a temporary session stopping within ten minutes, not for a later one', () => {
        const item = (stopsAt: string): Overview['items'][number] => ({
            ...base,
            kind: 'session',
            title: 'x',
            running: true,
            stopsAt,
            session: session({ stopsAt })
        });
        expect(toRow(item('2026-06-10T12:10:00Z'), now)).toMatchObject({ saved: false, danger: true });
        expect(toRow(item('2026-06-10T12:11:00Z'), now).danger).toBe(false);
    });

    it('turns red for a saved workspace deleted within three days, grey after that', () => {
        const item = (deletesAt: string): Overview['items'][number] => ({
            ...base,
            kind: 'workspace',
            title: 'x',
            running: false,
            lastUsed: '2026-06-01T00:00:00Z',
            deletesAt,
            workspace
        });
        const soon = toRow(item('2026-06-13T12:00:00Z'), now);
        expect(soon).toMatchObject({ danger: true, life: { kind: 'deletes', soon: true }, since: { kind: 'lastUsed' } });
        expect(toRow(item('2026-06-14T12:00:00Z'), now)).toMatchObject({ danger: false, life: { kind: 'deletes', soon: false } });
    });

    it('does not open an address that is not http(s)', () => {
        const row = toRow({ ...base, kind: 'session', title: 'x', running: true, session: session({ url: 'javascript:alert(1)' }) }, now);
        expect(row.openUrl).toBeUndefined();
    });

    it('uses the template name when the title is empty, never a technical name', () => {
        expect(toRow({ ...base, kind: 'workspace', title: '', running: false }, now).title).toBe('Java');
    });
});

describe('toRows', () => {
    it('lists running first, then the most recently used', () => {
        const rows = toRows(
            {
                items: [
                    { ...base, kind: 'workspace', title: 'old', running: false, lastUsed: '2026-06-01T00:00:00Z', workspace },
                    { ...base, kind: 'workspace', title: 'new', running: false, lastUsed: '2026-06-09T00:00:00Z', workspace },
                    { ...base, kind: 'session', title: 'run', running: true, session: session() }
                ]
            },
            now
        );
        expect(rows.map(r => r.title)).toEqual(['run', 'new', 'old']);
    });
});

describe('sessionToRow', () => {
    it('takes name and logo from the template', () => {
        const templates = [{ name: 'java-17', displayName: 'Java', logoUrl: '/l.png' }] as Template[];
        expect(sessionToRow(session(), templates, now)).toMatchObject({ title: 'Java', logoUrl: '/l.png', running: true });
    });
});

describe('sessionToRow with an unlisted template', () => {
    it('still takes name and logo from the template', () => {
        const templates = [{ name: 'java-17', displayName: 'Java', logoUrl: '/l.png', listed: false }] as Template[];
        expect(sessionToRow(session(), templates, now)).toMatchObject({ title: 'Java', logoUrl: '/l.png' });
    });
});

describe('deleteRow', () => {
    const row = (over: object): ReturnType<typeof toRow> => ({
        ...toRow({ ...base, kind: 'workspace', title: 'x', running: false, workspace }, now),
        ...over
    });
    const client = (status: number, code?: string): { client: Api['client']; del: ReturnType<typeof vi.fn> } => {
        const del = vi.fn(async () => ({ response: { ok: status < 300, status }, error: code ? { code } : undefined }));
        return { client: { DELETE: del } as unknown as Api['client'], del };
    };

    it('deletes the workspace when there is one, else the session', async () => {
        const a = client(204);
        expect(await deleteRow(a.client, row({}))).toEqual({ ok: true });
        expect(a.del).toHaveBeenCalledWith('/workspaces/{name}', { params: { path: { name: 'w-1' } } });
        const b = client(204);
        await deleteRow(b.client, row({ workspaceName: undefined, sessionName: 's-9' }));
        expect(b.del).toHaveBeenCalledWith('/sessions/{name}', { params: { path: { name: 's-9' } } });
    });

    it('reports a timeout for internal errors, a failure otherwise, and treats a missing item as deleted', async () => {
        expect(await deleteRow(client(500, 'internal').client, row({}))).toEqual({ ok: false, retry: true });
        expect(await deleteRow(client(403, 'forbidden').client, row({}))).toEqual({ ok: false, retry: false });
        expect(await deleteRow(client(404, 'not-found').client, row({}))).toEqual({ ok: true });
        const broken = { DELETE: vi.fn().mockRejectedValue(new Error('network')) } as unknown as Api['client'];
        expect(await deleteRow(broken, row({}))).toEqual({ ok: false, retry: false });
    });
});
