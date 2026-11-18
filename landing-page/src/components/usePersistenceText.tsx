import { Save, SaveOff } from 'lucide-react';

import { useI18n } from '../i18n';

/** One wording for persistence everywhere: a short label and the sentence that says what happens to the files. */
export function usePersistenceText(saved: boolean): { short: string; long: string; icon: React.JSX.Element } {
    const { t } = useI18n();
    return saved
        ? { short: t('persistence.saved'), long: t('persistence.savedLong'), icon: <Save aria-hidden /> }
        : { short: t('persistence.temporary'), long: t('persistence.temporaryLong'), icon: <SaveOff aria-hidden /> };
}
