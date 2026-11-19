import { usePolling } from '../../components/usePolling';
import { formatClock } from '../../format';
import { useAdmin } from '../context';
import { ok } from '../query';
import { CodePill, Empty, LoadError, Mono, NoPrometheus, PageHead, Panel, Pill, Scroll, TplCell, TraceLink, UpdateFailed } from '../ui';

export const ProblemsPage = (): React.JSX.Element => {
    const { api, ta, lang } = useAdmin();
    const { data, failed } = usePolling(() => ok(api.client.GET('/admin/problems')));
    const codes = [...(data?.codes ?? [])].sort((a, b) => b.count - a.count);
    const top = Math.max(1, ...codes.map(c => c.count));
    return (
        <>
            <PageHead title={ta('problems.title')} sub={ta('problems.sub')} />
            {failed && !data && <LoadError />}
            <UpdateFailed show={failed && data !== undefined} />
            {data &&
                (data.codes ? (
                    <Panel title={ta('problems.byCode')}>
                        {codes.length === 0 ? (
                            <Empty>{ta('problems.none')}</Empty>
                        ) : (
                            <div className='a-bars'>
                                {codes.map(c => (
                                    <div key={c.code} className='a-bar-row'>
                                        <CodePill code={c.code} />
                                        <div className='track'>
                                            <span style={{ width: `${(c.count / top) * 100}%` }} />
                                        </div>
                                        <span className='num'>{c.count}</span>
                                    </div>
                                ))}
                            </div>
                        )}
                    </Panel>
                ) : (
                    <NoPrometheus />
                ))}
            {data && (
                <Panel title={ta('problems.lastHour')} extra={<span className='small normal'>{ta('problems.keeps')}</span>}>
                    {data.events.length === 0 ? (
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
                                        <th>{ta('problems.session')}</th>
                                        <th>{ta('detail')}</th>
                                        <th />
                                    </tr>
                                </thead>
                                <tbody>
                                    {data.events.map((e, i) => (
                                        <tr key={`${e.time}-${i}`}>
                                            <td className='mono'>{formatClock(e.time, lang)}</td>
                                            <td>{e.code ? <CodePill code={e.code} /> : <Pill tone='n'>{e.reason}</Pill>}</td>
                                            <td>{e.template && <TplCell name={e.template} />}</td>
                                            <td>{e.ownerHash && <Mono>{e.ownerHash}</Mono>}</td>
                                            <td>{e.session && <Mono>{e.session}</Mono>}</td>
                                            <td className='wrap'>{e.message}</td>
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
            )}
        </>
    );
};
