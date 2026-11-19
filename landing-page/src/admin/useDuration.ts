import { useAdmin } from './context';
import { duration } from './format';
import type { AdminKey } from './messages';

/** "50 min", "14 days": a number with a translated unit. */
export function useDuration(): (seconds: number) => string {
    const { ta } = useAdmin();
    return seconds => {
        const { n, unit } = duration(seconds);
        const key = unit === 'days' && n === 1 ? 'unit.day' : (`unit.${unit}` as AdminKey);
        return `${n} ${ta(key)}`;
    };
}
