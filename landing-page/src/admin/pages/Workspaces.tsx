import { CircleCheck, Clock, Lock, TriangleAlert } from 'lucide-react';
import { useEffect, useState } from 'react';

import type { AdminWorkspace } from '../../apiTypes';
import { usePolling } from '../../components/usePolling';
import { formatAgo, formatDate } from '../../format';
import { useAdmin } from '../context';
import { formatBytes, WORKSPACE_SOON_MS } from '../format';
import { ok } from '../query';
import { Empty, Kpi, LoadError, Mono, PageHead, Pill, Scroll, type Tone, TplCell } from '../ui';
import { useDuration } from '../useDuration';

const STATES = ['active', 'inUse', 'expiring', 'expired'] as const;
const STATE_LOOK: Record<AdminWorkspace['state'], { tone: Tone; icon: React.JSX.Element }> = {
    active: { tone: 'ok', icon: <CircleCheck aria-hidden /> },
    inUse: { tone: 'n', icon: <Lock aria-hidden /> },
    expiring: { tone: 'warn', icon: <TriangleAlert aria-hidden /> },
    expired: { tone: 'warn', icon: <Clock aria-hidden /> }
};

export const WorkspacesPage = (): React.JSX.Element => {
    const { api, ta, lang, now, templates, config } = useAdmin();
    const fd = useDuration();
    const [search, setSearch] = useState('');
    const [template, setTemplate] = useState('');
    const [state, setState] = useState<'' | AdminWorkspace['state']>('');
    const [q, setQ] = useState('');
    useEffect(() => {
        const id = setTimeout(() => setQ(search.trim()), 300);
        return () => clearTimeout(id);
    }, [search]);

    const { data, failed } = usePolling(
        () =>
            ok(
                api.client.GET('/admin/workspaces', {
                    params: { query: { q: q || undefined, template: template || undefined, state: state || undefined } }
                })
            ),
        30_000,
        [q, template, state]
    );
    const s = data?.summary;
    return (
        <>
            <PageHead title={ta('workspaces.title')} sub={ta('workspaces.sub')} />
            {failed && !data && <LoadError />}
            {s && (
                <div className='a-kpis'>
                    <Kpi label={ta('workspaces.total')} value={s.total} detail={ta('workspaces.inUse', { n: s.inUse })} />
                    <Kpi
                        label={ta('workspaces.storage')}
                        value={formatBytes(s.requestedBytes)}
                        detail={
                            s.quota
                                ? ta('workspaces.quota', { used: formatBytes(s.quota.usedBytes), hard: formatBytes(s.quota.hardBytes) })
                                : undefined
                        }
                    />
                    <Kpi
                        label={ta('workspaces.soon')}
                        value={s.expiringIn3Days}
                        bad={s.expiringIn3Days > 0}
                        detail={ta('workspaces.soonDetail')}
                    />
                    <Kpi label={ta('workspaces.expired')} value={s.expired} />
                </div>
            )}
            {config && <p className='a-note'>{ta('workspaces.retention', { n: fd(config.workspaces.retentionSeconds) })}</p>}
            <section className='a-panel'>
                <div className='a-filters'>
                    <input
                        type='search'
                        placeholder={ta('workspaces.search')}
                        aria-label={ta('workspaces.search')}
                        value={search}
                        onChange={e => setSearch(e.target.value)}
                    />
                    <select aria-label={ta('sessions.template')} value={template} onChange={e => setTemplate(e.target.value)}>
                        <option value=''>{ta('sessions.allTemplates')}</option>
                        {templates?.map(x => (
                            <option key={x.name} value={x.name}>
                                {x.displayName}
                            </option>
                        ))}
                    </select>
                    <select aria-label={ta('state')} value={state} onChange={e => setState(e.target.value as typeof state)}>
                        <option value=''>{ta('workspaces.allStates')}</option>
                        {STATES.map(x => (
                            <option key={x} value={x}>
                                {ta(`workspaces.state.${x}`)}
                            </option>
                        ))}
                    </select>
                </div>
                {data && data.items.length === 0 ? (
                    <Empty>{ta('sessions.empty')}</Empty>
                ) : (
                    <Scroll>
                        <table>
                            <thead>
                                <tr>
                                    <th>{ta('workspaces.name')}</th>
                                    <th>{ta('workspaces.titleCol')}</th>
                                    <th>{ta('sessions.template')}</th>
                                    <th>{ta('user')}</th>
                                    <th>{ta('state')}</th>
                                    <th>{ta('workspaces.size')}</th>
                                    <th>{ta('workspaces.lastUsed')}</th>
                                    <th>{ta('workspaces.deletesOn')}</th>
                                </tr>
                            </thead>
                            <tbody>
                                {data?.items.map(w => {
                                    const soon = w.deletesAt !== undefined && Date.parse(w.deletesAt) - now <= WORKSPACE_SOON_MS;
                                    return (
                                        <tr key={w.name}>
                                            <td>
                                                <Mono>{w.name}</Mono>
                                            </td>
                                            <td>{w.title}</td>
                                            <td>
                                                <TplCell name={w.template} />
                                            </td>
                                            <td>
                                                <Mono>{w.ownerHash}</Mono>
                                            </td>
                                            <td>
                                                <Pill tone={STATE_LOOK[w.state].tone} icon={STATE_LOOK[w.state].icon}>
                                                    {ta(`workspaces.state.${w.state}`)}
                                                </Pill>
                                            </td>
                                            <td>{formatBytes(w.requestedBytes)}</td>
                                            <td>{w.lastUsed ? formatAgo(w.lastUsed, now, lang) : ''}</td>
                                            <td className={soon ? 'bad-t' : undefined}>
                                                {w.deletesAt ? formatDate(w.deletesAt, lang) : ''}
                                            </td>
                                        </tr>
                                    );
                                })}
                            </tbody>
                        </table>
                    </Scroll>
                )}
                {data && (
                    <div className='a-pad a-foot'>
                        <span className='small'>{ta('workspaces.count', { shown: data.items.length, total: data.total })}</span>
                    </div>
                )}
            </section>
        </>
    );
};
