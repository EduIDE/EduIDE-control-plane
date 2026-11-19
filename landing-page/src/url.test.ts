import { describe, expect, it } from 'vitest';

import { httpHref } from './url';

describe('httpHref', () => {
    it.each([
        ['https://jaeger.example.org/trace/1', 'https://jaeger.example.org/trace/1'],
        ['http://localhost:3000', 'http://localhost:3000/']
    ])('keeps %s', (input, out) => {
        expect(httpHref(input)).toBe(out);
    });

    it.each(['javascript:alert(1)', 'data:text/html,x', '/relative', 'not a url', '', undefined])('refuses %s', input => {
        expect(httpHref(input)).toBeUndefined();
    });
});
