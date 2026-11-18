import type { Overview } from './api-client';

type Item = Overview['items'][number];

export type RowAction = 'open' | 'stop' | 'start' | 'delete';

export interface Row {
    key: string;
    title: string;
    subtitle: string;
    logoUrl?: string;
    running: boolean;
    lastUsed?: string;
    /** Set when the workspace expires within the warning window. */
    expiresSoon?: string;
    actions: RowAction[];
    item: Item;
}

export const EXPIRY_WARNING_MS = 3 * 24 * 60 * 60 * 1000;

export function toRow(item: Item, now: number): Row {
    const name = item.session?.name ?? item.workspace?.name ?? item.title;
    const expires = item.expiresAt ? Date.parse(item.expiresAt) : NaN;
    const soon = !item.running && expires - now <= EXPIRY_WARNING_MS;
    return {
        key: `${item.kind}:${name}`,
        title: item.title || item.templateDisplayName || item.template,
        subtitle: item.templateDisplayName || item.template,
        logoUrl: item.templateLogoUrl,
        running: item.running,
        lastUsed: item.lastUsed,
        expiresSoon: soon && !Number.isNaN(expires) ? item.expiresAt : undefined,
        // A running item is a session (with or without workspace): only open and stop. Without a running
        // session only a workspace remains, which can be started again or deleted.
        actions: item.running ? ['open', 'stop'] : ['start', 'delete'],
        item
    };
}

export function toRows(overview: Overview, now: number = Date.now()): Row[] {
    return overview.items
        .map(i => toRow(i, now))
        .sort((a, b) => Number(b.running) - Number(a.running) || (b.lastUsed ?? '').localeCompare(a.lastUsed ?? ''));
}
