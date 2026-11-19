import { describe, expect, it } from 'vitest';

import { detectLang, hasMessage, makeText, translate } from './i18n';

describe('i18n', () => {
    it('picks German for de* browsers and English otherwise, and the stored choice wins', () => {
        expect(detectLang('de-AT', undefined)).toBe('de');
        expect(detectLang('fr-FR', undefined)).toBe('en');
        expect(detectLang(undefined, undefined)).toBe('en');
        expect(detectLang('de', 'en')).toBe('en');
        expect(detectLang('en-US', 'de')).toBe('de');
        expect(detectLang('en-US', 'xx')).toBe('en');
    });
    it('fills placeholders', () => {
        expect(translate('en', 'greeting.back', { name: 'Alex' })).toBe('Welcome back, Alex');
        expect(translate('de', 'limit.title', { n: 3 })).toBe('Du hast bereits 3 Umgebungen gestartet');
        expect(hasMessage('error.capacity-exhausted')).toBe(true);
        expect(hasMessage('error.nope')).toBe(false);
    });
});

describe('makeText', () => {
    it('shows the raw last part for a key a newer server added instead of crashing', () => {
        const text = makeText<string>({ 'a.b': 'B {x}' }, { 'a.b': 'B de {x}' });
        expect(text('en', 'a.b', { x: 1 })).toBe('B 1');
        expect(text('de', 'a.b', { x: 1 })).toBe('B de 1');
        expect(text('en', 'phase.Paused')).toBe('Paused');
        expect(text('de', 'plain')).toBe('plain');
    });
});
