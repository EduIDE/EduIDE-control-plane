export interface FooterLinksConfig {
    attribution?: { text?: string; url?: string; version?: string };
    github?: { url: string };
    bugReport?: { text: string; url: string; target?: string; rel?: string };
    featureRequest?: { text: string; url: string; target?: string; rel?: string };
    about?: { text: string; url: string; target?: string; rel?: string };
}

/** A legal page is either an external page (`url`) or a Markdown file served by the landing page (`path`). */
export type LegalPage = { url: string } | { path: string };

/** Runtime config served as /config.json by the landing page server. */
export interface AppConfig {
    apiBaseUrl: string;
    keycloak: { url: string; realm: string; clientId: string };
    title?: string;
    /** Reserved: the deployment still runs the pre control plane API. Not used by this page. */
    legacy?: boolean;
    footerLinks?: FooterLinksConfig;
    legal: { imprint: LegalPage; privacy: LegalPage };
}

function isHttpUrl(value: unknown): boolean {
    try {
        return typeof value === 'string' && /^https?:$/.test(new URL(value).protocol);
    } catch {
        return false;
    }
}

function parseLegalPage(name: string, raw: unknown): LegalPage {
    const entry = raw as { url?: unknown; path?: unknown } | undefined;
    const hasUrl = entry?.url !== undefined;
    const hasPath = entry?.path !== undefined;
    if (hasUrl === hasPath) {
        throw new Error(`config.json: legal.${name} needs exactly one of url or path`);
    }
    if (hasUrl) {
        if (!isHttpUrl(entry?.url)) {
            throw new Error(`config.json: legal.${name}.url must be an http(s) URL`);
        }
        return { url: entry?.url as string };
    }
    // Only the two files the server knows, so a config can never point the page at another path.
    if (entry?.path !== `/legal/${name}.md`) {
        throw new Error(`config.json: legal.${name}.path must be /legal/${name}.md`);
    }
    return { path: entry.path };
}

export function parseConfig(raw: unknown): AppConfig {
    const c = raw as Partial<AppConfig> | undefined;
    if (!c || typeof c.apiBaseUrl !== 'string' || !c.keycloak?.url || !c.keycloak.realm || !c.keycloak.clientId) {
        throw new Error('config.json needs apiBaseUrl and keycloak.url, keycloak.realm, keycloak.clientId');
    }
    for (const key of ['github', 'bugReport', 'featureRequest', 'about'] as const) {
        const url = c.footerLinks?.[key]?.url;
        if (url !== undefined && !isHttpUrl(url)) {
            throw new Error(`config.json: footerLinks.${key}.url must be an http(s) URL`);
        }
    }
    const legal = { imprint: parseLegalPage('imprint', c.legal?.imprint), privacy: parseLegalPage('privacy', c.legal?.privacy) };
    return { ...c, legal } as AppConfig;
}

export async function loadConfig(): Promise<AppConfig> {
    const res = await fetch('/config.json', { cache: 'no-store' });
    if (!res.ok) {
        throw new Error(`config.json: HTTP ${res.status}`);
    }
    return parseConfig(await res.json());
}
