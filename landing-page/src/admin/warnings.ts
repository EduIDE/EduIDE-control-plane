import type { Ta } from './context';
import type { AdminKey } from './messages';

/** The text of a status warning in the chosen language. Unknown codes keep the service's own message. */
export function warningText(ta: Ta, w: { code: string; pool?: string; since?: string; message: string }, now: number): string {
    const known = w.code === 'pool-empty' || w.code === 'capacity-pressure' || w.code === 'scaler-silent';
    if (!known || (w.code !== 'scaler-silent' && !w.pool)) {
        return w.message;
    }
    const minutes = w.since ? Math.max(0, Math.floor((now - Date.parse(w.since)) / 60_000)) : undefined;
    const key = `warn.${w.code}${minutes === undefined ? '' : 'Since'}` as AdminKey;
    return ta(key, { pool: w.pool ?? '', n: minutes ?? 0 });
}
