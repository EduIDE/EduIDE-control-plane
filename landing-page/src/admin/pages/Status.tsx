import { ExternalLink, TriangleAlert } from 'lucide-react';

import type { AdminSessionList } from '../../apiTypes';
import { usePolling } from '../../components/usePolling';
import { useWatch } from '../../components/useWatch';
import { formatAgo, formatClock } from '../../format';
import { useAdmin } from '../context';
import { formatBytes as bytesText } from '../format';
import { ok } from '../query';
import { SessionsTable } from '../SessionsTable';
import { CodePill, Empty, Kpi, LoadError, Mono, NoPrometheus, PageHead, PageLink, Panel, Pill, Scroll, TplCell, TraceLink } from '../ui';
import { useDuration } from '../useDuration';

export const StatusPage = (): React.JSX.Element => {
    const { api, ta, lang, now, status, config, templates } = useAdmin();
    const fd = useDuration();
    const pools = usePolling(() => ok(api.client.GET('/admin/pools')));
    const problems = usePolling(() => ok(api.client.GET('/admin/problems')));
    const workspaces = usePolling(() => ok(api.client.GET('/admin/workspaces', { params: { query: { limit: 1 } } })));
    const sessions = useWatch<AdminSessionList>(api.watchAdminSessions).data;
    const stats = status?.stats;
    const grafana = config?.admin.links?.grafana;
    const slowest = [...(stats?.startP95 ?? [])].sort((a, b) => b.seconds - a.seconds)[0];
    const slowestName = slowest ? (templates?.find(t => t.name === slowest.template)?.displayName ?? slowest.template) : '';

    const poolRows = pools.data?.pools ?? [];
    // One scale for all bars, so a big pool does not make a small one look full.
    const scale = Math.max(1, ...poolRows.map(p => Math.max(p.idleReady + p.idleStarting, p.targetIdle)));
    const buckets = stats?.startsPerHour ?? [];
    const peak = Math.max(1, ...buckets.map(b => b.warm + b.cold + b.failed));
    const p95 = stats?.startP95 ?? [];
    const p95Max = Math.max(1, ...p95.map(p => p.seconds));
    const summary = workspaces.data?.summary;
    const retention = config ? fd(config.workspaces.retentionSeconds) : undefined;

    return (
        <>
            <PageHead title={ta('status.title')} />
            {status?.warnings.map(w => (
                <div key={`${w.code}-${w.pool ?? ''}`} className='a-alert warn' role='alert'>
                    <TriangleAlert aria-hidden />
                    <div>{w.message}</div>
                </div>
            ))}
            {!status ? (
                <p role='status'>{ta('loading')}</p>
            ) : (
                <div className='a-kpis'>
                    <Kpi
                        label={ta('kpi.running')}
                        value={status.runningSessions}
                        detail={ta('kpi.runningDetail', { n: status.startingSessions })}
                    />
                    <Kpi
                        label={ta('kpi.idle')}
                        value={status.idleReady}
                        unit={`/ ${status.idleTarget} ${ta('kpi.idleTarget')}`}
                        detail={status.idleReady < status.idleTarget ? ta('kpi.idleBelow') : ta('kpi.idleOk')}
                        bad={status.idleReady < status.idleTarget}
                        good={status.idleReady >= status.idleTarget}
                    />
                    {slowest && (
                        <Kpi
                            label={ta('kpi.p95')}
                            value={slowest.seconds.toFixed(1)}
                            unit={ta('unit.s')}
                            detail={ta('kpi.p95Detail', { name: slowestName })}
                        />
                    )}
                    {stats?.failuresLastHour !== undefined && (
                        <Kpi
                            label={ta('kpi.failed')}
                            value={stats.failuresLastHour}
                            bad={stats.failuresLastHour > 0}
                            detail={stats.startsLastHour !== undefined ? ta('kpi.failedDetail', { n: stats.startsLastHour }) : undefined}
                        />
                    )}
                    {stats?.cpuRequestsPercent !== undefined && (
                        <Kpi
                            label={ta('kpi.capacity')}
                            value={Math.round(stats.cpuRequestsPercent)}
                            unit='%'
                            detail={stats.nodeCount !== undefined ? ta('kpi.capacityDetail', { n: stats.nodeCount }) : undefined}
                        />
                    )}
                </div>
            )}
            {status && !stats && <NoPrometheus />}

            <Panel title={ta('pools.title')} extra={<PageLink page='pools'>{ta('status.allPools')}</PageLink>}>
                {pools.failed && !pools.data ? (
                    <LoadError />
                ) : poolRows.length === 0 ? (
                    <Empty>{ta('none')}</Empty>
                ) : (
                    <Scroll>
                        <table>
                            <thead>
                                <tr>
                                    <th>{ta('pools.template')}</th>
                                    <th className='wide'>{ta('pools.bar')}</th>
                                    <th className='num'>{ta('pools.claimed')}</th>
                                    <th>{ta('pools.scaler')}</th>
                                    <th>{ta('pools.last')}</th>
                                    <th>{ta('pools.state')}</th>
                                </tr>
                            </thead>
                            <tbody>
                                {poolRows.map(p => (
                                    <tr key={p.name}>
                                        <td>
                                            <TplCell name={p.template} />
                                        </td>
                                        <td>
                                            <div
                                                className='a-stack'
                                                role='img'
                                                aria-label={`${p.idleReady} / ${p.idleStarting} / ${p.targetIdle}`}
                                            >
                                                <i style={{ width: `${(p.idleReady / scale) * 100}%`, background: 'var(--bar-idle)' }} />
                                                <i
                                                    style={{ width: `${(p.idleStarting / scale) * 100}%`, background: 'var(--bar-start)' }}
                                                />
                                                {p.targetIdle > 0 && (
                                                    <span className='target' style={{ left: `${(p.targetIdle / scale) * 100}%` }} />
                                                )}
                                            </div>
                                            <span className='small'>
                                                {p.idleReady} / {p.idleStarting} / {p.targetIdle}
                                            </span>
                                        </td>
                                        <td className='num'>{p.claimed}</td>
                                        <td>{p.scalerMode ?? ''}</td>
                                        <td className='small'>
                                            {p.lastDecision
                                                ? `${ta(`pools.reason.${p.lastDecision.reason}`)}, ${formatAgo(p.lastDecision.time, now, lang)}`
                                                : ''}
                                        </td>
                                        <td>
                                            <Pill tone={p.state === 'ok' ? 'ok' : 'bad'}>{ta(`pools.state.${p.state}`)}</Pill>
                                        </td>
                                    </tr>
                                ))}
                            </tbody>
                        </table>
                    </Scroll>
                )}
                <div className='a-pad a-legend'>
                    <span>
                        <i style={{ background: 'var(--bar-idle)' }} />
                        {ta('pools.idleReady')}
                    </span>
                    <span>
                        <i style={{ background: 'var(--bar-start)' }} />
                        {ta('pools.starting')}
                    </span>
                    <span>
                        <i style={{ background: 'var(--text)', width: 2 }} />
                        {ta('pools.target')}
                    </span>
                </div>
            </Panel>

            {(buckets.length > 0 || p95.length > 0) && (
                <div className='a-cols'>
                    {buckets.length > 0 && (
                        <Panel
                            title={ta('launches.title')}
                            extra={
                                grafana && (
                                    <a href={grafana} target='_blank' rel='noreferrer'>
                                        {ta('launches.grafana')}
                                        <ExternalLink aria-hidden />
                                    </a>
                                )
                            }
                        >
                            <div className='a-pad'>
                                <div className='a-chart' role='img' aria-label={ta('launches.title')}>
                                    {buckets.map(b => (
                                        <div
                                            key={b.time}
                                            className='c'
                                            style={{ height: `${((b.warm + b.cold + b.failed) / peak) * 100}%` }}
                                            title={`${formatClock(b.time, lang)}: ${b.warm} ${ta('launches.warm')}, ${b.cold} ${ta('launches.cold')}, ${b.failed} ${ta('launches.failed')}`}
                                        >
                                            <i style={{ flex: b.warm, background: 'var(--bar-warm)' }} />
                                            <i style={{ flex: b.cold, background: 'var(--bar-cold)' }} />
                                            <i style={{ flex: b.failed, background: 'var(--danger)' }} />
                                        </div>
                                    ))}
                                </div>
                                <div className='a-axis'>
                                    <span>{formatClock(buckets[0].time, lang)}</span>
                                    <span>{ta('launches.now')}</span>
                                </div>
                                <div className='a-legend'>
                                    <span>
                                        <i style={{ background: 'var(--bar-warm)' }} />
                                        {ta('launches.warm')}
                                    </span>
                                    <span>
                                        <i style={{ background: 'var(--bar-cold)' }} />
                                        {ta('launches.cold')}
                                    </span>
                                    <span>
                                        <i style={{ background: 'var(--danger)' }} />
                                        {ta('launches.failed')}
                                    </span>
                                </div>
                            </div>
                        </Panel>
                    )}
                    {p95.length > 0 && (
                        <Panel title={ta('latency.title')}>
                            <div className='a-pad'>
                                {p95.map(p => (
                                    <div key={`${p.template}-${p.persistence}`} className='a-lat'>
                                        <span className='name'>
                                            <TplCell name={p.template} />
                                            <span className='small'>{ta(p.persistence === 'volume' ? 'saved' : 'temp')}</span>
                                        </span>
                                        <div className='track'>
                                            <span
                                                style={{
                                                    left: 0,
                                                    width: `${Math.max((p.seconds / p95Max) * 100, 1.5)}%`,
                                                    background: p.persistence === 'volume' ? 'var(--bar-cold)' : 'var(--bar-warm)'
                                                }}
                                            />
                                        </div>
                                        <span className='num small'>
                                            {p.seconds.toFixed(1)} {ta('unit.s')}
                                        </span>
                                    </div>
                                ))}
                                <div className='small'>{ta('latency.note')}</div>
                            </div>
                        </Panel>
                    )}
                </div>
            )}

            <div className='a-cols'>
                <Panel title={ta('problems.recent')} extra={<PageLink page='problems'>{ta('status.allProblems')}</PageLink>}>
                    {problems.failed && !problems.data ? (
                        <LoadError />
                    ) : (problems.data?.events.length ?? 0) === 0 ? (
                        <Empty>{ta('problems.none')}</Empty>
                    ) : (
                        <Scroll>
                            <table>
                                <thead>
                                    <tr>
                                        <th>{ta('time')}</th>
                                        <th>{ta('code')}</th>
                                        <th>{ta('sessions.template')}</th>
                                        <th>{ta('user')}</th>
                                        <th />
                                    </tr>
                                </thead>
                                <tbody>
                                    {problems.data?.events.slice(0, 5).map((e, i) => (
                                        <tr key={`${e.time}-${i}`}>
                                            <td className='mono'>{formatClock(e.time, lang)}</td>
                                            <td>{e.code ? <CodePill code={e.code} /> : <Pill tone='n'>{e.reason}</Pill>}</td>
                                            <td>{e.template && <TplCell name={e.template} />}</td>
                                            <td>{e.ownerHash && <Mono>{e.ownerHash}</Mono>}</td>
                                            <td>
                                                <TraceLink url={e.traceUrl} />
                                            </td>
                                        </tr>
                                    ))}
                                </tbody>
                            </table>
                        </Scroll>
                    )}
                </Panel>
                <Panel title={ta('workspaces.title')}>
                    <div className='a-pad'>
                        {workspaces.failed && !summary ? (
                            <LoadError />
                        ) : (
                            summary && (
                                <>
                                    <div className='a-lat flat'>
                                        <span>{ta('workspaces.total')}</span>
                                        <span className='num'>{summary.total}</span>
                                    </div>
                                    <div className='a-lat flat'>
                                        <span>{ta('workspaces.storage')}</span>
                                        <span className='num'>{bytesText(summary.requestedBytes)}</span>
                                    </div>
                                    <div className={`a-lat flat${summary.expiringIn3Days > 0 ? ' bad-t' : ''}`}>
                                        <span>{ta('workspaces.soon')}</span>
                                        <span className='num'>{summary.expiringIn3Days}</span>
                                    </div>
                                    <div className='a-lat flat'>
                                        <span>{ta('workspaces.expired')}</span>
                                        <span className='num'>{summary.expired}</span>
                                    </div>
                                    {retention && <div className='small'>{ta('workspaces.retention', { n: retention })}</div>}
                                </>
                            )
                        )}
                    </div>
                </Panel>
            </div>

            <Panel title={ta('sessions.title')} extra={<PageLink page='sessions'>{ta('status.allSessions')}</PageLink>}>
                {sessions ? (
                    <SessionsTable items={sessions.items} />
                ) : (
                    <p className='a-pad' role='status'>
                        {ta('loading')}
                    </p>
                )}
            </Panel>
        </>
    );
};
