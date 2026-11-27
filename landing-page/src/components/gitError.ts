import { hasMessage, type Translate } from '../i18n';

/** The text for a problem code of the Git endpoints. Unknown codes get the generic text, never the server's detail. */
export function gitErrorText(t: Translate, code: string): string {
    const key = `git.error.${code}`;
    return hasMessage(key) ? t(key) : t('git.error.generic');
}
