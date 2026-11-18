// The Kv rows are static pairs: Kv keys them by position.
/* eslint-disable react/jsx-key */
import { usePolling } from '../../components/usePolling';
import { formatAgo, formatClock } from '../../format';
import { useAdmin } from '../context';
import { ok } from '../query';
import { Empty, Kv, LoadError, PageHead, Panel, Pill, TplCell } from '../ui';

export const PoolsPage = (): React.JSX.Element => {
    const { api, ta, lang, now, status } = useAdmin();
    const { data, failed } = usePolling(() => ok(api.client.GET('/admin/pools')));
    const free = status?.stats?.poolFree;
    return (
        <>
            <PageHead title={ta('pools.title')} sub={ta('pools.sub')} />
            {failed && !data && <LoadError />}
            {data && data.pools.length === 0 && <Empty>{ta('none')}</Empty>}
            <div className='a-grid2'>
                {data?.pools.map(p => {
                    const series = free?.find(f => f.pool === p.name)?.points ?? [];
                    const top = Math.max(1, ...series.map(s => s.value));
                    return (
                        <Panel key={p.name} title={<TplCell name={p.template} />}>
                            <Kv
                                rows={[
                                    [ta('pools.state'), <Pill tone={p.state === 'ok' ? 'ok' : 'bad'}>{ta(`pools.state.${p.state}`)}</Pill>],
                                    [ta('pools.idleStarting'), `${p.idleReady} / ${p.idleStarting}`],
                                    [
                                        ta('pools.targetRange'),
                                        <>
                                            {p.targetIdle}{' '}
                                            <span className='small'>
                                                ({p.minIdle} - {p.maxInstances})
                                            </span>
                                        </>
                                    ],
                                    [ta('pools.claimed'), p.claimed],
                                    [ta('pools.scaler'), p.scalerMode ?? <span className='small'>{ta('pools.noScaler')}</span>],
                                    ...(p.emptySince
                                        ? [[ta('pools.emptySince'), formatAgo(p.emptySince, now, lang)] as [string, string]]
                                        : []),
                                    ...(series.length > 0
                                        ? [
                                              [
                                                  ta('pools.free6h'),
                                                  <div className='a-spark' role='img' aria-label={ta('pools.free6h')}>
                                                      {series.map(s => (
                                                          <i
                                                              key={s.time}
                                                              className={s.value === 0 ? 'z' : undefined}
                                                              style={{ height: `${Math.max((s.value / top) * 100, 6)}%` }}
                                                          />
                                                      ))}
                                                  </div>
                                              ] as [string, React.JSX.Element]
                                          ]
                                        : [])
                                ]}
                            />
                        </Panel>
                    );
                })}
            </div>
            {data && (
                <Panel title={ta('pools.decisions')} extra={<span className='small normal'>{ta('pools.decisionsNote')}</span>}>
                    {data.decisions.length === 0 ? (
                        <Empty>{ta('pools.noDecisions')}</Empty>
                    ) : (
                        <ul className='a-log'>
                            {data.decisions.map((d, i) => (
                                <li key={`${d.time}-${i}`}>
                                    <span className='when'>{formatClock(d.time, lang)}</span>
                                    <span>
                                        <b>{d.pool}</b>: {ta(`pools.reason.${d.reason}`)}
                                        <br />
                                        <span className='small'>{d.message}</span>
                                    </span>
                                </li>
                            ))}
                        </ul>
                    )}
                </Panel>
            )}
        </>
    );
};
