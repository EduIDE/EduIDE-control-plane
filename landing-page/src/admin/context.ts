import { createContext, useContext } from 'react';

import type { Api } from '../api';
import type { AdminConfig, AdminStatus, AdminTemplate } from '../apiTypes';
import type { Lang } from '../i18n';
import type { AdminKey } from './messages';
import type { AdminPageName } from './routes';

export type Ta = (key: AdminKey, params?: Record<string, string | number>) => string;

export interface AdminContextValue {
    api: Api;
    lang: Lang;
    ta: Ta;
    now: number;
    /** From the status event stream. */
    status?: AdminStatus;
    config?: AdminConfig;
    templates?: AdminTemplate[];
    go: (page: AdminPageName) => void;
}

export const AdminContext = createContext<AdminContextValue | undefined>(undefined);

export function useAdmin(): AdminContextValue {
    const value = useContext(AdminContext);
    if (!value) {
        throw new Error('useAdmin must be used within the admin panel');
    }
    return value;
}
