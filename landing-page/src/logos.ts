import brand from './assets/logos/theia-logo.png';

// Every logo is part of the bundle: the page never loads an image from another origin. An admin adds a logo
// by adding `<key>-logo.png` to src/assets/logos.
const files = import.meta.glob('./assets/logos/*-logo.png', { eager: true, query: '?url', import: 'default' }) as Record<string, string>;
const LOGOS = new Map(Object.entries(files).map(([path, url]) => [path.replace(/^.*\/(.+)-logo\.png$/, '$1'), url]));

export const BRAND_LOGO: string = brand;

/** The address from the API only when it is a path on this origin (`/x`, not `//host/x` and not `https://...`). */
export function sameOriginPath(url: string | undefined): string | undefined {
    return url?.startsWith('/') && !url.startsWith('//') && !url.includes('\\') ? url : undefined;
}

/**
 * The logo of a template or starter project: the exact name (`java-17`), then its language prefix (`java`, from
 * `java-17` or `c-templates` to `c`), then a same-origin path from the API. Without one the caller shows a folder icon.
 */
export function logoFor(name: string, apiLogoUrl?: string): string | undefined {
    const prefix = name.split(/[-_.]/)[0];
    return LOGOS.get(name) ?? LOGOS.get(prefix) ?? sameOriginPath(apiLogoUrl);
}
