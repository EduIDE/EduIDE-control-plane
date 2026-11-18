import Keycloak from 'keycloak-js';

import type { AppConfig } from './config';

/** Signs the user in (redirects when needed). Call after the Artemis token left the URL fragment. */
export async function initKeycloak(config: AppConfig): Promise<Keycloak> {
    const keycloak = new Keycloak(config.keycloak);
    await keycloak.init({
        onLoad: 'login-required',
        pkceMethod: 'S256',
        checkLoginIframe: false,
        // The query stays so a launch link survives the login round trip, the fragment never reaches here.
        redirectUri: window.location.href.split('#')[0]
    });
    return keycloak;
}

export function tokenProvider(keycloak: Keycloak): () => Promise<string> {
    return async () => {
        await keycloak.updateToken(30);
        return keycloak.token ?? '';
    };
}
