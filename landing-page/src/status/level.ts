import type { PublicStatus } from '../apiTypes';
import type { StatusKey } from './messages';

export type Tone = 'ok' | 'warn' | 'danger';

const TONES = { ok: 'ok', slow: 'warn', down: 'danger' } as const;

/** The big message on top. The overall level is the worst part, the text names what is affected. */
export function hero(status: PublicStatus): { tone: Tone; title: StatusKey; text: StatusKey } {
    const tone = TONES[status.overall];
    if (status.overall === 'ok') {
        return { tone, title: 'hero.ok', text: 'hero.okText' };
    }
    if (status.overall === 'slow') {
        return { tone, title: 'hero.slow', text: 'hero.slowText' };
    }
    return status.parts.starting === 'down'
        ? { tone, title: 'hero.downStarting', text: 'hero.downStartingText' }
        : { tone, title: 'hero.downOther', text: 'hero.downOtherText' };
}

export const levelTone = (level: PublicStatus['overall']): Tone => TONES[level];

export type Wait = { kind: 'instant' } | { kind: 'seconds'; n: number } | { kind: 'minutes'; n: number };

export function wait(seconds: number): Wait {
    if (seconds < 2) {
        return { kind: 'instant' };
    }
    return seconds >= 60 ? { kind: 'minutes', n: Math.round(seconds / 60) } : { kind: 'seconds', n: Math.round(seconds) };
}
