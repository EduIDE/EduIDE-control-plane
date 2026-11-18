import { describe, expect, it } from 'vitest';

import { legalHref, legalRoute, renderMarkdown } from './legal';

describe('legal', () => {
    it('recognises the routes', () => {
        expect(legalRoute('/imprint')).toBe('imprint');
        expect(legalRoute('/privacy/')).toBe('privacy');
        expect(legalRoute('/')).toBeUndefined();
        expect(legalRoute('/me/overview')).toBeUndefined();
    });

    it('links external pages directly and hosted ones through the route', () => {
        expect(legalHref('imprint', { url: 'https://example.org/i' })).toEqual({ href: 'https://example.org/i', external: true });
        expect(legalHref('privacy', { path: '/legal/privacy.md' })).toEqual({ href: '/privacy', external: false });
    });

    it('renders Markdown and strips active content', async () => {
        const html = await renderMarkdown('# Imprint\n\nSome *text* <script>alert(1)</script> <img src=x onerror=alert(1)>');
        expect(html).toContain('<h1>Imprint</h1>');
        expect(html).toContain('<em>text</em>');
        expect(html).not.toContain('<script');
        expect(html).not.toContain('onerror');
    });
});
