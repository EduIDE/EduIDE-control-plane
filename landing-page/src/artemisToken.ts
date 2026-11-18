// G16: the Artemis token travels in the URL fragment. It is moved into sessionStorage before Keycloak starts,
// because keycloak-js uses the fragment itself and rejects fragments in redirect URIs.

const KEY = 'eduide.artemisToken';

export interface Location {
    hash: string;
    search: string;
    pathname: string;
}

/** Reads `#artemisToken=...`, stores it and returns the URL without the fragment (undefined if there was none). */
export function captureArtemisToken(loc: Location, store: Storage): string | undefined {
    const fragment = new URLSearchParams(loc.hash.replace(/^#/, ''));
    const token = fragment.get('artemisToken');
    if (!token) {
        return undefined;
    }
    store.setItem(KEY, token);
    fragment.delete('artemisToken');
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

export function clearArtemisToken(store: Storage): void {
    store.removeItem(KEY);
}
