const NEW_ISSUE_URL = 'https://github.com/EduIDE/EduIDE/issues/new';

/**
 * Issue form link for the single EduIDE tracker. Only the hostname goes in: the full page URL can carry
 * tokens (Artemis links), so neither the path nor the query string is ever read.
 */
export function issueFormUrl(template: 'bug_report.yml' | 'feature_request.yml', hostname: string, version?: string): string {
    const params = new URLSearchParams({ template, installation: hostname });
    if (version) {
        params.set('version', version);
    }
    return `${NEW_ISSUE_URL}?${params.toString()}`;
}
