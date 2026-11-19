import type { Api } from '../api';
import type { Overview } from '../api-client';
import { useWatch } from './useWatch';

export function useOverview(api: Api): { overview?: Overview; lost: boolean; recovered: number } {
    const { data, lost, recovered } = useWatch<Overview>(api.watchOverview);
    return { overview: data, lost, recovered };
}
