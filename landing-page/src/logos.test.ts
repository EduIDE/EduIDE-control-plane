import { describe, expect, it } from 'vitest';

import { BRAND_LOGO, logoFor, sameOriginPath } from './logos';

describe('logoFor', () => {
    it('finds the exact template name before the language prefix', () => {
        const exact = logoFor('java-17');
        const prefix = logoFor('java-21');
        expect(exact).toMatch(/java-17-logo/);
        expect(prefix).toMatch(/java-logo/);
        expect(exact).not.toBe(prefix);
    });

    it('falls back to the language prefix', () => {
        expect(logoFor('c-templates')).toMatch(/\/c-logo/);
        expect(logoFor('python_3')).toMatch(/python-logo/);
        expect(logoFor('rust.nightly')).toMatch(/rust-logo/);
    });

    it('resolves starter projects by id', () => {
        for (const id of ['maven', 'gradle', 'make', 'bazel']) {
            expect(logoFor(id)).toMatch(new RegExp(`${id}-logo`));
        }
    });

    it('has a logo for every language of the landing page', () => {
        for (const key of ['java-17', 'java', 'c', 'python', 'rust', 'javascript', 'node', 'ocaml', 'go']) {
            expect(logoFor(key), key).toBeDefined();
        }
        expect(BRAND_LOGO).toMatch(/theia-logo/);
    });

    it('returns nothing for an unknown name, so the folder icon shows', () => {
        expect(logoFor('cobol-1')).toBeUndefined();
        expect(logoFor('zig')).toBeUndefined();
    });

    it('uses a same-origin path from the API but never an absolute address', () => {
        expect(logoFor('zig', '/assets/zig.png')).toBe('/assets/zig.png');
        expect(logoFor('zig', 'https://cdn.example.org/zig.png')).toBeUndefined();
        expect(logoFor('zig', '//cdn.example.org/zig.png')).toBeUndefined();
        expect(logoFor('zig', 'data:image/png;base64,AAAA')).toBeUndefined();
        expect(logoFor('zig', 'javascript:alert(1)')).toBeUndefined();
        expect(logoFor('java-17', 'https://cdn.example.org/j.png')).toMatch(/java-17-logo/);
    });

    it('checks the path shape on its own', () => {
        expect(sameOriginPath('/a/b.png')).toBe('/a/b.png');
        expect(sameOriginPath('/\\evil.example/x.png')).toBeUndefined();
        expect(sameOriginPath(undefined)).toBeUndefined();
    });
});
