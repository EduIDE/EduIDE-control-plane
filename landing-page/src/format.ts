import type { Lang } from './i18n';

const FIVE_MINUTES = 5 * 60 * 1000;

/** The service can stop a session up to five minutes before `stopsAt`, so a shown stop time rounds down. */
export function formatClock(iso: string, lang: Lang): string {
    const floored = new Date(Math.floor(Date.parse(iso) / FIVE_MINUTES) * FIVE_MINUTES);
    return new Intl.DateTimeFormat(lang, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(floored);
}

export function formatDate(iso: string, lang: Lang): string {
    return new Intl.DateTimeFormat(lang, { day: 'numeric', month: 'short' }).format(new Date(iso));
}

export function formatAgo(iso: string, now: number, lang: Lang): string {
    const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' });
    const seconds = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
    if (seconds < 60) {
        return rtf.format(0, 'second');
    }
    const steps: [number, Intl.RelativeTimeFormatUnit][] = [
        [60, 'minute'],
        [60 * 60, 'hour'],
        [24 * 60 * 60, 'day']
    ];
    let [size, unit] = steps[0];
    for (const step of steps) {
        if (seconds >= step[0]) {
            [size, unit] = step;
        }
    }
    return rtf.format(-Math.floor(seconds / size), unit);
}

/** The wall clock time to the minute, for "showing the state from 14:02". */
export function formatTime(iso: string, lang: Lang): string {
    return new Intl.DateTimeFormat(lang, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(new Date(iso));
}
