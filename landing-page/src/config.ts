export interface FooterLinksConfig {
    attribution?: { text?: string; url?: string; version?: string };
    bugReport?: { text: string; url: string; target?: string; rel?: string };
    featureRequest?: { text: string; url: string; target?: string; rel?: string };
    about?: { text: string; url: string; target?: string; rel?: string };
}

/** Runtime config served as /config.json by the landing page server. */
export interface AppConfig {
    apiBaseUrl: string;
    keycloak: { url: string; realm: string; clientId: string };
    title?: string;
    /** Reserved: the deployment still runs the pre control plane API. Not used by this page. */
    legacy?: boolean;
    footerLinks?: FooterLinksConfig;
    /** Links to the operator's legal pages. A footer link shows only when its URL is set. */
    legal?: { imprintUrl?: string; privacyUrl?: string };
}

function isHttpUrl(value: unknown): boolean {
    try {
        return typeof value === 'string' && /^https?:$/.test(new URL(value).protocol);
    } catch {
        return false;
    }
}

export function parseConfig(raw: unknown): AppConfig {
    const c = raw as Partial<AppConfig> | undefined;
    if (!c || typeof c.apiBaseUrl !== 'string' || !c.keycloak?.url || !c.keycloak.realm || !c.keycloak.clientId) {
        throw new Error('config.json needs apiBaseUrl and keycloak.url, keycloak.realm, keycloak.clientId');
    }
    for (const key of ['imprintUrl', 'privacyUrl'] as const) {
        const url = c.legal?.[key];
        if (url !== undefined && !isHttpUrl(url)) {
            throw new Error(`config.json: legal.${key} must be an http(s) URL`);
        }
    }
    return c as AppConfig;
}

export async function loadConfig(): Promise<AppConfig> {
    const res = await fetch('/config.json', { cache: 'no-store' });
    if (!res.ok) {
        throw new Error(`config.json: HTTP ${res.status}`);
    }
    return parseConfig(await res.json());
}
