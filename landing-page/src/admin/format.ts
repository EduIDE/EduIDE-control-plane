export type Unit = 's' | 'min' | 'h' | 'days';

/** A whole number and a unit, so the caller can translate the unit. */
export function duration(seconds: number): { n: number; unit: Unit } {
    if (seconds < 120) {
        return { n: Math.round(seconds), unit: 's' };
    }
    if (seconds < 2 * 3600) {
        return { n: Math.round(seconds / 60), unit: 'min' };
    }
    if (seconds < 2 * 86400) {
        return { n: Math.round(seconds / 3600), unit: 'h' };
    }
    return { n: Math.round(seconds / 86400), unit: 'days' };
}

export function formatBytes(bytes: number): string {
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
    let value = bytes;
    let i = 0;
    while (value >= 1024 && i < units.length - 1) {
        value /= 1024;
        i += 1;
    }
    return `${i === 0 || value >= 10 ? Math.round(value) : value.toFixed(1)} ${units[i]}`;
}

export const WORKSPACE_SOON_MS = 3 * 24 * 60 * 60 * 1000;
export const STOP_SOON_MS = 10 * 60 * 1000;
