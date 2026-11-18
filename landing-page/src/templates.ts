import type { Template } from './api-client';

export type PersistenceMode = Template['defaultPersistence'];
export type StartEstimate = NonNullable<NonNullable<Template['startEstimate']>[PersistenceMode]>;

export const isSavedByDefault = (template: Template): boolean => template.defaultPersistence === 'volume';

export const estimateFor = (template: Template | undefined, mode: PersistenceMode): StartEstimate | undefined =>
    template?.startEstimate?.[mode];

export type StartLine = { kind: 'instant' } | { kind: 'about'; seconds: number };

/** Warm starts and very short cold starts read as instant. No data means no promise. */
export function startLine(estimate: StartEstimate | undefined): StartLine | undefined {
    if (!estimate) {
        return undefined;
    }
    return estimate.strategy === 'warm' || estimate.seconds < 2
        ? { kind: 'instant' }
        : { kind: 'about', seconds: Math.round(estimate.seconds) };
}

export interface LaunchInfo {
    title: string;
    templateName: string;
    logoUrl?: string;
    /** Set when the start time is worth announcing (not instant, and known). */
    seconds?: number;
}

/** What the launch view shows about an environment, from the template when known and the caller's values otherwise. */
export function buildLaunchInfo(
    template: Template | undefined,
    request: { template: string; persistence?: PersistenceMode; workspace?: string; display?: { title?: string } },
    known: { title?: string; templateName?: string; logoUrl?: string } = {}
): LaunchInfo {
    const templateName = known.templateName ?? template?.displayName ?? request.template;
    const mode = request.persistence ?? (request.workspace ? 'volume' : (template?.defaultPersistence ?? 'none'));
    const line = startLine(estimateFor(template, mode));
    return {
        title: known.title ?? request.display?.title ?? templateName,
        templateName,
        logoUrl: known.logoUrl ?? template?.logoUrl,
        seconds: line?.kind === 'about' ? line.seconds : undefined
    };
}
