import type { Api } from '../api';
import type { Overview } from '../api-client';
import { useWatch } from './useWatch';

export function useOverview(api: Api): { overview?: Overview; lost: boolean } {
    const { data, lost } = useWatch<Overview>(api.watchOverview);
    return { overview: data, lost };
}
