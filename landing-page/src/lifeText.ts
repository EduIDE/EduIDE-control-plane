import { formatClock, formatDate } from './format';
import type { I18n } from './i18n';
import type { Row } from './overview';

export function lifeText(row: Row, { t, lang }: I18n): string {
    const life = row.life;
    if (!life) {
        return '';
    }
    if (life.kind === 'stops') {
        const time = formatClock(life.at, lang);
        return t(life.reason === 'max' ? 'life.stopsMax' : 'life.stopsIdle', { time }) + (row.saved ? '' : t('life.filesDeleted'));
    }
    return t('life.deletesOn', { date: formatDate(life.at, lang) }) + (life.soon ? t('life.unlessStart') : '');
}
