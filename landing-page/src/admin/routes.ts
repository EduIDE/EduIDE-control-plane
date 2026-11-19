export const ADMIN_PAGES = ['status', 'pools', 'sessions', 'workspaces', 'problems', 'templates', 'limits'] as const;
export type AdminPageName = (typeof ADMIN_PAGES)[number];

/** `/admin` is the status page, `/admin/<page>` the others. Unknown subpaths show the status page. */
export function adminPage(pathname: string): AdminPageName {
    const sub = pathname.replace(/\/+$/, '').replace(/^\/admin\/?/, '');
    return ADMIN_PAGES.find(p => p === sub) ?? 'status';
}

export const adminPath = (page: AdminPageName): string => (page === 'status' ? '/admin' : `/admin/${page}`);
