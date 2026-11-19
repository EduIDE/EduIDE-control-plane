/** The address only when it is http(s). Links from the API or the config must never become `javascript:` or `data:` hrefs. */
export function httpHref(url: string | undefined): string | undefined {
    try {
        const u = new URL(url ?? '');
        return u.protocol === 'https:' || u.protocol === 'http:' ? u.href : undefined;
    } catch {
        return undefined;
    }
}
