// G16: the Artemis token travels in the URL fragment, and so does the repository URL when it carries VCS credentials
// (Artemis#14702). Both move into sessionStorage before Keycloak starts, because keycloak-js uses the fragment itself
// and rejects fragments in redirect URIs; this also keeps the credentials out of the address bar and the history.

const KEY = 'eduide.artemisToken';
const GIT_URI_KEY = 'eduide.gitUri';
const FRAGMENT_KEYS: Record<string, string> = { artemisToken: KEY, gitUri: GIT_URI_KEY };

export interface Location {
    hash: string;
    search: string;
    pathname: string;
}

/**
 * Reads `#artemisToken=...` and `#gitUri=...`, stores them and returns the URL without them (undefined if there were
 * none). Other fragment parameters stay.
 */
export function captureArtemisToken(loc: Location, store: Storage): string | undefined {
    const fragment = new URLSearchParams(loc.hash.replace(/^#/, ''));
    let found = false;
    for (const [param, key] of Object.entries(FRAGMENT_KEYS)) {
        const value = fragment.get(param);
        if (value) {
            found = true;
            try {
                store.setItem(key, value);
            } catch {
                // Blocked or full storage: the launch goes without this value, but it still leaves the address bar.
            }
        }
        fragment.delete(param);
    }
    if (!found) {
        return undefined;
    }
    const rest = fragment.toString();
    return loc.pathname + loc.search + (rest ? `#${rest}` : '');
}

/** Moves the token from the fragment to sessionStorage and strips it from the address bar. Call before Keycloak init. */
export function captureFromWindow(): void {
    try {
        const cleaned = captureArtemisToken(window.location, window.sessionStorage);
        if (cleaned !== undefined) {
            window.history.replaceState(window.history.state, '', cleaned);
        }
    } catch {
        // Blocked storage: the launch then starts without a token.
    }
}

/** Stored token, else the deprecated `?artemisToken` query parameter. */
export function readArtemisToken(search: string, store: Storage, warn: (m: string) => void = console.warn): string | undefined {
    const stored = store.getItem(KEY);
    if (stored) {
        return stored;
    }
    const legacy = new URLSearchParams(search).get('artemisToken');
    if (legacy) {
        warn('?artemisToken is deprecated, Artemis must pass #artemisToken=... in the URL fragment');
    }
    return legacy ?? undefined;
}

/**
 * Stored repository URL (from the fragment), else `?gitUri`. A query value with credentials is deprecated: it ends up
 * in the browser history and in server logs.
 */
export function readGitUri(search: string, store: Storage, warn: (m: string) => void = console.warn): string | undefined {
    const stored = store.getItem(GIT_URI_KEY);
    if (stored) {
        return stored;
    }
    const query = new URLSearchParams(search).get('gitUri') ?? undefined;
    if (query && hasCredentials(query)) {
        warn('?gitUri with credentials is deprecated, Artemis must pass #gitUri=... in the URL fragment');
    }
    return query;
}

function hasCredentials(uri: string): boolean {
    try {
        const u = new URL(uri);
        return u.username !== '' || u.password !== '';
    } catch {
        return false;
    }
}

/** Removes the stored token and repository URL once the launch is done or abandoned. */
export function clearArtemisToken(store: Storage): void {
    store.removeItem(KEY);
    store.removeItem(GIT_URI_KEY);
}
