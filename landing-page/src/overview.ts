import type { Api } from './api';
import type { Overview, Session, Template } from './api-client';
import { httpHref } from './url';

type Item = Overview['items'][number];

export const EXPIRY_WARNING_MS = 3 * 24 * 60 * 60 * 1000;
export const STOP_WARNING_MS = 10 * 60 * 1000;

export type Life = { kind: 'stops'; at: string; reason: 'idle' | 'max' } | { kind: 'deletes'; at: string; soon: boolean };

export interface Row {
    key: string;
    title: string;
    template: string;
    templateName: string;
    logoUrl?: string;
    running: boolean;
    /** Files stay after the session ends (the environment has a workspace). */
    saved: boolean;
    since?: { kind: 'started' | 'lastUsed'; at: string };
    life?: Life;
    /** Files are about to be lost: red in the list. */
    danger: boolean;
    sessionName?: string;
    workspaceName?: string;
    /** Only set for a running session with an http(s) address. */
    openUrl?: string;
}

function finish(row: Omit<Row, 'danger'>, now: number): Row {
    const at = row.life && Date.parse(row.life.at);
    let danger = false;
    if (row.life?.kind === 'deletes') {
        danger = row.life.soon;
    } else if (row.life?.kind === 'stops' && !row.saved && at !== undefined) {
        danger = at - now <= STOP_WARNING_MS;
    }
    return { ...row, danger };
}

export function toRow(item: Item, now: number): Row {
    const session = item.session;
    const workspaceName = item.workspace?.name ?? session?.workspace;
    const name = session?.name ?? workspaceName ?? item.title;
    const stopsAt = item.stopsAt ?? session?.stopsAt;
    const deletesAt = item.deletesAt ?? item.workspace?.deletesAt;
    let life: Life | undefined;
    if (item.running && stopsAt) {
        life = { kind: 'stops', at: stopsAt, reason: session?.stopReason === 'max' ? 'max' : 'idle' };
    } else if (!item.running && deletesAt) {
        life = { kind: 'deletes', at: deletesAt, soon: Date.parse(deletesAt) - now <= EXPIRY_WARNING_MS };
    }
    const startedAt = session?.readyAt ?? session?.createdAt;
    const lastUsed = item.lastUsed;
    const templateName = item.templateDisplayName || item.template;
    return finish(
        {
            key: `${item.kind}:${name}`,
            title: item.title || templateName,
            template: item.template,
            templateName,
            logoUrl: item.templateLogoUrl,
            running: item.running,
            saved: workspaceName !== undefined,
            since:
                item.running && startedAt ? { kind: 'started', at: startedAt } : lastUsed ? { kind: 'lastUsed', at: lastUsed } : undefined,
            life,
            sessionName: session?.name,
            workspaceName,
            openUrl: item.running ? httpHref(session?.url) : undefined
        },
        now
    );
}

export function toRows(overview: Overview, now: number = Date.now()): Row[] {
    return overview.items
        .map(i => toRow(i, now))
        .sort((a, b) => Number(b.running) - Number(a.running) || (b.since?.at ?? '').localeCompare(a.since?.at ?? ''));
}

/** A running session from a `session-limit-reached` problem as a list row. */
export function sessionToRow(session: Session, templates: Template[], now: number = Date.now()): Row {
    const template = templates.find(t => t.name === session.template);
    return toRow(
        {
            kind: 'session',
            title: session.display?.title ?? '',
            template: session.template,
            templateDisplayName: template?.displayName,
            templateLogoUrl: template?.logoUrl,
            running: true,
            session
        },
        now
    );
}

export type DeleteResult = { ok: true } | { ok: false; retry: boolean };

/** Deleting a workspace stops its running session first, so one call is enough. Without a workspace the session goes. */
export async function deleteRow(client: Api['client'], row: Row): Promise<DeleteResult> {
    try {
        const { response, error } = row.workspaceName
            ? await client.DELETE('/workspaces/{name}', { params: { path: { name: row.workspaceName } } })
            : await client.DELETE('/sessions/{name}', { params: { path: { name: row.sessionName ?? '' } } });
        if (response.ok || response.status === 404) {
            return { ok: true };
        }
        return { ok: false, retry: error?.code === 'internal' };
    } catch {
        return { ok: false, retry: false };
    }
}
