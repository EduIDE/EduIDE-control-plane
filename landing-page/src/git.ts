import type { Api } from './api';
import type { Problem } from './api-client';
import type { GitAccess, GitPreflightResponse, GitProvider } from './apiTypes';
import { httpHref } from './url';

// Git accounts (ADR 0023). The token never reaches this page: it only starts the OAuth flow, passes code and state to
// the service and asks questions. Codes, state and repository URLs of the user are never logged.

const CALLBACK_KEY = 'eduide.gitCallback';
const MAX_VALUE = 2048;
const MAX_RETURN_TO = 512;

const hasControl = (v: string): boolean => Array.from(v).some(c => c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f);

/** A path on this page: one leading slash, no second slash or backslash, no scheme, no host, no control characters. */
export function safeReturnTo(value: string | undefined | null): string {
    if (!value || value.length > MAX_RETURN_TO || !/^\/(?![/\\])/.test(value) || value.includes('\\') || hasControl(value)) {
        return '/';
    }
    return value;
}

/** Where the browser comes back to after the Git host: this page including a pending start link. */
export function currentReturnTo(loc: { pathname: string; search: string } = window.location): string {
    return safeReturnTo(loc.pathname + loc.search);
}

export interface CallbackParams {
    state: string;
    code?: string;
    error?: string;
}

/**
 * The Git host redirects to /git/callback?code=...&state=.... The values move into sessionStorage before Keycloak
 * starts, like the Artemis token: otherwise they travel to Keycloak in the redirect URI and stay in the history.
 * Returns the address without the query, or undefined when the path is not the callback.
 */
export function captureGitCallback(loc: { pathname: string; search: string }, store: Storage): string | undefined {
    if (loc.pathname.replace(/\/+$/, '') !== '/git/callback') {
        return undefined;
    }
    const q = new URLSearchParams(loc.search);
    const state = q.get('state') ?? '';
    const code = q.get('code') ?? '';
    const error = q.get('error') ?? '';
    if (state && state.length <= MAX_VALUE && code.length <= MAX_VALUE && error.length <= 256 && (code || error)) {
        try {
            store.setItem(CALLBACK_KEY, JSON.stringify({ state, ...(code && { code }), ...(error && { error }) }));
        } catch {
            // Blocked storage: the page then says the connection could not be completed.
        }
    }
    return loc.search ? '/git/callback' : undefined;
}

export function captureGitCallbackFromWindow(): void {
    const cleaned = captureGitCallback(window.location, window.sessionStorage);
    if (cleaned !== undefined) {
        window.history.replaceState(window.history.state, '', cleaned);
    }
}

/** Reads the stored callback values and deletes them: they are used once. */
export function takeGitCallback(store: Storage): CallbackParams | undefined {
    try {
        const raw = store.getItem(CALLBACK_KEY);
        store.removeItem(CALLBACK_KEY);
        const p = raw ? (JSON.parse(raw) as Partial<CallbackParams>) : undefined;
        return typeof p?.state === 'string' && p.state ? { state: p.state, code: p.code, error: p.error } : undefined;
    } catch {
        return undefined;
    }
}

export type CallbackResult = { ok: true; provider: string; login: string; returnTo: string } | { ok: false; code: string };

/** POST /git/callback with the bearer token of the page. */
export async function completeCallback(api: Api, p: CallbackParams): Promise<CallbackResult> {
    try {
        const { data, error } = await api.client.POST('/git/callback', {
            body: p.error ? { state: p.state, error: p.error } : { state: p.state, code: p.code }
        });
        if (data) {
            return { ok: true, provider: data.provider, login: data.login, returnTo: safeReturnTo(data.returnTo) };
        }
        return { ok: false, code: (error as Partial<Problem> | undefined)?.code ?? 'internal' };
    } catch {
        return { ok: false, code: 'unreachable' };
    }
}

/** Providers of the deployment; empty when the feature is off or the service is too old. */
export async function loadProviders(api: Api): Promise<GitProvider[]> {
    try {
        const { data } = await api.client.GET('/git/providers');
        // Only entries with the shape of a provider count, so a foreign answer never draws a row.
        return Array.isArray(data)
            ? data.filter(p => typeof p?.name === 'string' && typeof p.host === 'string' && typeof p.connected === 'boolean')
            : [];
    } catch {
        return [];
    }
}

/** Starts the connect flow and navigates to the Git host. Returns a problem code when it fails. */
export async function startConnect(
    api: Api,
    provider: string,
    returnTo: string = currentReturnTo(),
    navigate: (url: string) => void = url => window.location.assign(url)
): Promise<string | undefined> {
    try {
        const { data, error } = await api.client.POST('/git/providers/{name}/connect', {
            params: { path: { name: provider } },
            body: { returnTo: safeReturnTo(returnTo) }
        });
        const url = httpHref(data?.authorizeUrl);
        if (!url) {
            return (error as Partial<Problem> | undefined)?.code ?? 'internal';
        }
        navigate(url);
        return undefined;
    } catch {
        return 'unreachable';
    }
}

export async function disconnectProvider(api: Api, provider: string): Promise<boolean> {
    try {
        const { response } = await api.client.DELETE('/git/providers/{name}', { params: { path: { name: provider } } });
        return response.status === 204;
    } catch {
        return false;
    }
}

/** The check before a launch with a repository. Any failure (network, 404 of an older service, rate limit) returns undefined: launch as today. */
export async function preflight(api: Api, gitUri: string): Promise<GitPreflightResponse | undefined> {
    try {
        const { data } = await api.client.POST('/git/preflight', { body: { gitUri } });
        return data;
    } catch {
        return undefined;
    }
}

// ---- repository approval -------------------------------------------------------------------------------

export interface ApprovalRequest {
    session: string;
    /** The repository as the service sees it: host in lower case, no trailing `.git` or slash. */
    repository: string;
    access: GitAccess;
}

const SESSION_NAME = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;

/**
 * Parses `?session=...&approveRepo=...&access=...`. The parameters come from a link and are attacker controlled:
 * `undefined` means no approval link, `'invalid'` a link that must not be acted on.
 */
export function parseApproval(search: string): ApprovalRequest | 'invalid' | undefined {
    const q = new URLSearchParams(search);
    if (!q.has('approveRepo')) {
        return undefined;
    }
    const session = q.get('session') ?? '';
    const access = q.get('access');
    const repository = normalizeRepository(q.get('approveRepo') ?? '');
    if (!SESSION_NAME.test(session) || (access !== 'read' && access !== 'write') || !repository) {
        return 'invalid';
    }
    return { session, repository, access };
}

/** https, no credentials, query, fragment or port (the rules of `GIT_URI`), then host and path the way the service compares them. */
export function normalizeRepository(value: string): string | undefined {
    try {
        const u = new URL(value);
        if (
            u.protocol !== 'https:' ||
            u.username ||
            u.password ||
            u.search ||
            u.hash ||
            u.port ||
            hasControl(value) ||
            /[\\\s]/.test(value)
        ) {
            return undefined;
        }
        const path = u.pathname.replace(/\/+$/, '').replace(/\.git$/, '');
        return path && path !== '/' ? `https://${u.hostname}${path}` : undefined;
    } catch {
        return undefined;
    }
}
