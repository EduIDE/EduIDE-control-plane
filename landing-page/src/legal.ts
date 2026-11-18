import DOMPurify from 'dompurify';
import { marked } from 'marked';

import type { LegalPage } from './config';

export type LegalRoute = 'imprint' | 'privacy';

export function legalRoute(pathname: string): LegalRoute | undefined {
    const path = pathname.replace(/\/+$/, '');
    return path === '/imprint' ? 'imprint' : path === '/privacy' ? 'privacy' : undefined;
}

/** Operator-written Markdown still goes through the sanitizer: the text comes from a ConfigMap, not from this repo. */
export function renderMarkdown(source: string): string {
    return DOMPurify.sanitize(marked.parse(source, { async: false }));
}

/** An external page is linked directly, a hosted Markdown page lives on the route of the landing page. */
export function legalHref(route: LegalRoute, page: LegalPage): { href: string; external: boolean } {
    return 'url' in page ? { href: page.url, external: true } : { href: `/${route}`, external: false };
}
