export interface UserInfo {
    firstName?: string;
    initials: string;
    /** "Name (username)", for the avatar tooltip and label. */
    label: string;
}

const text = (value: unknown): string => (typeof value === 'string' ? value.trim() : '');

export function userFromToken(claims: Record<string, unknown> | undefined): UserInfo {
    const given = text(claims?.given_name);
    const name = text(claims?.name) || [given, text(claims?.family_name)].filter(Boolean).join(' ');
    const username = text(claims?.preferred_username);
    const words = name.split(/\s+/).filter(Boolean);
    const initials = words.length > 0 ? words[0][0] + (words.length > 1 ? words[words.length - 1][0] : '') : username.slice(0, 2);
    return {
        firstName: given || words[0],
        initials: initials.toUpperCase() || '?',
        label: name && username ? `${name} (${username})` : name || username
    };
}
