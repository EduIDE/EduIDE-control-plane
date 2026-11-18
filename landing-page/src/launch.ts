import type { LaunchRequest, Problem, Session, Template } from './api-client';

export function persistenceHint(ephemeral: boolean): string {
    return ephemeral ? 'Files are lost when the session ends.' : 'Files are kept between sessions.';
}

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

// The display source is not secret, so a token in the userinfo part of an Artemis git URL must not end up in it.
export function displaySource(gitUri: string): string {
    try {
        const u = new URL(gitUri);
        u.username = '';
        u.password = '';
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

export type LaunchState =
    | { kind: 'creating' }
    | { kind: 'starting'; session: Session }
    | { kind: 'ready'; url: string }
    | { kind: 'failed'; code: string; message: string; sessions?: Session[] };

const MESSAGES: Record<string, string> = {
    'capacity-exhausted': 'The cluster is full right now. Please try again in a few minutes.',
    'session-limit-reached': 'You reached the maximum number of running sessions. Stop one of them to start another.',
    'persistence-not-allowed': 'This environment does not support the requested storage mode.',
    'template-not-found': 'This environment does not exist.',
    'start-failed': 'The session could not be started.',
    'rate-limited': 'Too many requests. Please wait a moment and try again.',
    unauthorized: 'Your login expired. Reload the page to sign in again.',
    forbidden: 'You are not allowed to start this session.'
};

export function failureFromProblem(problem: Partial<Problem> | undefined): LaunchState {
    const code = problem?.code ?? 'internal';
    return {
        kind: 'failed',
        code,
        message: MESSAGES[code] ?? problem?.detail ?? problem?.title ?? 'Something went wrong.',
        sessions: problem?.sessions
    };
}

// Only http(s) targets: the URL comes from the API, but a redirect to javascript: must never be possible.
function safeUrl(url: string | undefined): string | undefined {
    try {
        const u = new URL(url ?? '');
        return u.protocol === 'https:' || u.protocol === 'http:' ? u.href : undefined;
    } catch {
        return undefined;
    }
}

/** The state machine step: every session event maps to the next state. Ready and Failed are final. */
export function applySession(session: Session): LaunchState {
    if (session.phase === 'Ready') {
        const url = safeUrl(session.url);
        return url ? { kind: 'ready', url } : { kind: 'failed', code: 'internal', message: 'The session has no valid address.' };
    }
    if (session.phase === 'Failed') {
        const code = session.errorCode ?? 'start-failed';
        return { kind: 'failed', code, message: MESSAGES[code] ?? session.message ?? MESSAGES['start-failed'] };
    }
    return { kind: 'starting', session };
}

export const isFinal = (s: LaunchState): boolean => s.kind === 'ready' || s.kind === 'failed';

/** W3C trace context header for the launch, so the service joins the browser's trace. Sampled flag off: no browser exporter. */
export function newTraceparent(random: (n: number) => Uint8Array = n => crypto.getRandomValues(new Uint8Array(n))): string {
    const hex = (n: number): string => Array.from(random(n), b => b.toString(16).padStart(2, '0')).join('');
    // Flag 01: the launch trace is always sampled.
    return `00-${hex(16)}-${hex(8)}-01`;
}
