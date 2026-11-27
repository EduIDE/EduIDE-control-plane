import { type LegalRoute, legalRoute } from './legal';

export type AppRoute =
    { kind: 'legal'; route: LegalRoute } | { kind: 'status' } | { kind: 'admin' } | { kind: 'gitCallback' } | { kind: 'home' };

/** Decides what the page shows. Legal pages and status need no login, the admin panel does. */
export function appRoute(pathname: string): AppRoute {
    const legal = legalRoute(pathname);
    if (legal) {
        return { kind: 'legal', route: legal };
    }
    const path = pathname.replace(/\/+$/, '');
    if (path === '/status') {
        return { kind: 'status' };
    }
    if (path === '/admin' || path.startsWith('/admin/')) {
        return { kind: 'admin' };
    }
    if (path === '/git/callback') {
        return { kind: 'gitCallback' };
    }
    return { kind: 'home' };
}
