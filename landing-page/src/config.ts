export interface PrivacyConfig {
    workspacePersistent?: boolean;
    workspaceGarbageCollected?: boolean;
    workspaceRetentionSeconds?: number;
    sessionMaxMinutes?: number;
    sessionIdleMinutes?: number;
    scientificUse?: boolean;
    controller?: { organisation?: string; representative?: string; address?: string; email?: string };
    dataProtectionOfficer?: { name?: string; email?: string };
}

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
    privacy?: PrivacyConfig;
}

export function parseConfig(raw: unknown): AppConfig {
    const c = raw as Partial<AppConfig> | undefined;
    if (!c || typeof c.apiBaseUrl !== 'string' || !c.keycloak?.url || !c.keycloak.realm || !c.keycloak.clientId) {
        throw new Error('config.json needs apiBaseUrl and keycloak.url, keycloak.realm, keycloak.clientId');
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
