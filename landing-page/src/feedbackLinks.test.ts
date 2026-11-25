import { describe, expect, it } from 'vitest';

import { issueFormUrl } from './feedbackLinks';

describe('issueFormUrl', () => {
    it('builds the bug report link with installation and version', () => {
        expect(issueFormUrl('bug_report.yml', 'ide.example.org', 'v1.2.3')).toBe(
            'https://github.com/EduIDE/EduIDE/issues/new?template=bug_report.yml&installation=ide.example.org&version=v1.2.3'
        );
    });

    it('uses the feature request template', () => {
        expect(issueFormUrl('feature_request.yml', 'ide.example.org', '1')).toContain('template=feature_request.yml');
    });

    it('omits the version when unknown or empty', () => {
        expect(issueFormUrl('bug_report.yml', 'ide.example.org')).toBe(
            'https://github.com/EduIDE/EduIDE/issues/new?template=bug_report.yml&installation=ide.example.org'
        );
        expect(issueFormUrl('bug_report.yml', 'ide.example.org', '')).not.toContain('version');
    });

    it('encodes values', () => {
        const url = new URL(issueFormUrl('bug_report.yml', 'a b&c', 'v1 &x=y'));
        expect(url.searchParams.get('installation')).toBe('a b&c');
        expect(url.searchParams.get('version')).toBe('v1 &x=y');
        expect([...url.searchParams.keys()]).toEqual(['template', 'installation', 'version']);
    });
});
