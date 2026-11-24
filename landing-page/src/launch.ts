import type { LaunchRequest, Problem, Session, Template } from './api-client';
import { httpHref } from './url';

export interface LaunchParams {
    appDef: string;
    gitUri?: string;
    gitRef?: string;
    artemisUrl?: string;
    artemisToken?: string;
    title?: string;
    persistence?: string;
}

export function parseLaunchParams(search: string): LaunchParams | undefined {
    const p = new URLSearchParams(search);
    const appDef = p.get('appDef') ?? p.get('appdef');
    if (!appDef) {
        return undefined;
    }
    const get = (k: string): string | undefined => p.get(k) || undefined;
    return {
        appDef,
        gitUri: get('gitUri'),
        gitRef: get('gitRef'),
        artemisUrl: get('artemisUrl'),
        title: get('title'),
        persistence: get('persistence')
    };
}

/** A link that names a repository but no environment: the page asks which environment should open it. */
export function parseRepositoryLink(search: string): LaunchParams | undefined {
    const p = new URLSearchParams(search);
    const get = (k: string): string | undefined => p.get(k) || undefined;
    const gitUri = get('gitUri');
    return gitUri ? { appDef: '', gitUri, gitRef: get('gitRef'), title: get('title'), persistence: get('persistence') } : undefined;
}

// The display source is not secret, so a token in the userinfo part of an Artemis git URL must not end up in it.
// The service refuses a display source with a query or a fragment, so both are dropped as well.
export function displaySource(gitUri: string): string {
    try {
        const u = new URL(gitUri);
        u.username = '';
        u.password = '';
        u.search = '';
        u.hash = '';
        return u.href;
    } catch {
        return '';
    }
}

/** Builds the POST /sessions body. Data keys the template does not list would be refused with 400, so they are left out. */
export function buildLaunchRequest(params: LaunchParams, template: Template): LaunchRequest {
    const candidates: Record<string, string | undefined> = {
        GIT_URI: params.gitUri,
        GIT_REF: params.gitRef,
        ARTEMIS_URL: params.artemisUrl,
        ARTEMIS_TOKEN: params.artemisToken
    };
    const data: Record<string, string> = {};
    for (const [key, value] of Object.entries(candidates)) {
        if (value && template.injectableKeys.includes(key)) {
            data[key] = value;
        }
    }
    const request: LaunchRequest = { template: template.name };
    if (Object.keys(data).length > 0) {
        request.data = data;
    }
    if (params.persistence === 'none' || params.persistence === 'volume') {
        if (template.persistence.includes(params.persistence)) {
            request.persistence = params.persistence;
        }
    }
    const title = params.title?.trim();
    const source = params.gitUri ? displaySource(params.gitUri) : '';
    if (title || source) {
        request.display = { ...(title && { title }), ...(source && { source }) };
    }
    return request;
}

/** `message` is the service's own text, shown only when the page has no translation for the code. */
export type LaunchState =
    | { kind: 'creating' }
    | { kind: 'starting'; session: Session }
    | { kind: 'ready'; url: string }
    | { kind: 'failed'; code: string; message?: string; sessions?: Session[] };

// The service refuses a repository host outside its allowedHosts with this detail; the page names the host instead.
const hostNotAllowed = /\bkey GIT_URI: host is not allowed\b/;

export function failureFromProblem(problem: Partial<Problem> | undefined): LaunchState {
    const refusedHost = problem?.code === 'invalid-input' && hostNotAllowed.test(problem.detail ?? '');
    return {
        kind: 'failed',
        code: refusedHost ? 'host-not-allowed' : (problem?.code ?? 'internal'),
        message: problem?.detail ?? problem?.title,
        sessions: problem?.sessions
    };
}

/** The state machine step: every session event maps to the next state. Ready and Failed are final. */
export function applySession(session: Session): LaunchState {
    if (session.phase === 'Ready') {
        const url = httpHref(session.url);
        return url ? { kind: 'ready', url } : { kind: 'failed', code: 'no-address' };
    }
    if (session.phase === 'Failed') {
        return { kind: 'failed', code: session.errorCode ?? 'start-failed', message: session.message };
    }
    return { kind: 'starting', session };
}

/** Launches a template, optionally with one of its project templates (only if the template accepts `TEMPLATE`). */
export function buildTileRequest(template: Template, variantId?: string): LaunchRequest {
    const request = buildLaunchRequest({ appDef: template.name }, template);
    if (variantId && template.injectableKeys.includes('TEMPLATE')) {
        request.data = { ...request.data, TEMPLATE: variantId };
        // Each project template gets its own saved workspace; its name tells them apart in "Your environments".
        const variant = template.variants?.find(v => v.id === variantId);
        if (variant) {
            request.display = { ...request.display, title: variant.name };
        }
    }
    return request;
}

export const isFinal = (s: LaunchState): boolean => s.kind === 'ready' || s.kind === 'failed';

/** The host of a git URL for messages, or '' when it does not parse. */
export function gitHost(gitUri: string | undefined): string {
    try {
        return gitUri ? new URL(gitUri).host : '';
    } catch {
        return '';
    }
}

/** W3C trace context header for the launch, so the service joins the browser's trace. Sampled flag off: no browser exporter. */
export function newTraceparent(random: (n: number) => Uint8Array = n => crypto.getRandomValues(new Uint8Array(n))): string {
    const hex = (n: number): string => Array.from(random(n), b => b.toString(16).padStart(2, '0')).join('');
    // Flag 01: the launch trace is always sampled.
    return `00-${hex(16)}-${hex(8)}-01`;
}
