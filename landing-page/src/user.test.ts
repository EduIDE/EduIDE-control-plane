import { describe, expect, it } from 'vitest';

import { userFromToken } from './user';

describe('userFromToken', () => {
    it('prefers given_name for the greeting and builds initials and label from the name', () => {
        expect(userFromToken({ given_name: 'Alex', family_name: 'Schmidt', preferred_username: 'ga58roj' })).toEqual({
            firstName: 'Alex',
            initials: 'AS',
            label: 'Alex Schmidt (ga58roj)'
        });
    });
    it('falls back to the first word of name, then to no name', () => {
        expect(userFromToken({ name: 'Ada Maria Lovelace', preferred_username: 'ada' })).toMatchObject({
            firstName: 'Ada',
            initials: 'AL'
        });
        expect(userFromToken({ preferred_username: 'ga58roj' })).toEqual({ firstName: undefined, initials: 'GA', label: 'ga58roj' });
        expect(userFromToken(undefined)).toMatchObject({ firstName: undefined, initials: '?' });
    });
});
