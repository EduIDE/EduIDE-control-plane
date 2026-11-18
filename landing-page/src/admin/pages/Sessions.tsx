// The Kv rows are static pairs: Kv keys them by position.
/* eslint-disable react/jsx-key */
import { useEffect, useState } from 'react';

import type { AdminSessionList } from '../../apiTypes';
import { usePolling } from '../../components/usePolling';
import { useWatch } from '../../components/useWatch';
import { formatClock } from '../../format';
import { useAdmin } from '../context';
import { ok } from '../query';
import { SessionsTable } from '../SessionsTable';
import { Empty, Kv, LoadError, Mono, PageHead, Panel, PhasePill, TplCell, TraceLink, WsCell } from '../ui';

const PAGE = 100;

function useDebounced<T>(value: T, ms: number): T {
    const [v, setV] = useState(value);
    useEffect(() => {
        const id = setTimeout(() => setV(value), ms);
        return () => clearTimeout(id);
    }, [value, ms]);
    return v;
}

export const SessionsPage = (): React.JSX.Element => {
    const { api, ta, lang, templates } = useAdmin();
    const [search, setSearch] = useState('');
    const [template, setTemplate] = useState('');
    const [kind, setKind] = useState<'' | 'none' | 'volume'>('');
    const [phase, setPhase] = useState<'' | 'Pending' | 'Starting' | 'Ready' | 'Failed'>('');
    const [limit, setLimit] = useState(PAGE);
    const [selected, setSelected] = useState<string>();
    const q = useDebounced(search.trim(), 300);

    const list = usePolling(
        () =>
            ok(
                api.client.GET('/admin/sessions', {
                    params: {
                        query: {
                            q: q || undefined,
                            template: template || undefined,
                            persistence: kind || undefined,
                            phase: phase || undefined,
                            limit
                        }
                    }
                })
            ),
        30_000,
        [q, template, kind, phase, limit]
    );
    // The stream carries the unfiltered first page, so it keeps the unfiltered view live between the 30 s reloads.
    const live = useWatch<AdminSessionList>(api.watchAdminSessions).data;
    const unfiltered = !q && !template && !kind && !phase && limit === PAGE;
    const shown = unfiltered ? (live ?? list.data) : list.data;

    const detail = usePolling(
        () =>
            selected ? ok(api.client.GET('/admin/sessions/{name}', { params: { path: { name: selected } } })) : Promise.resolve(undefined),
        30_000,
        [selected]
    );
    const d = selected ? detail.data : undefined;
    const t = d?.timestamps;
    const timeline: [string, string | undefined, boolean?][] = d
        ? [
              [ta('sessions.created'), d.createdAt],
              [ta('sessions.claimed'), t?.claimed],
              [ta('sessions.podReady'), t?.podReady],
              [ta('sessions.agentReady'), t?.agentReady],
              [ta('sessions.ready'), t?.ready ?? d.readyAt],
              [ta('sessions.lastActivity'), d.lastActivity],
              [ta('sessions.stopsRow'), d.stopsAt]
          ]
        : [];

    return (
        <>
            <PageHead title={ta('sessions.title')} sub={ta('sessions.sub')} />
            <div className='a-split'>
                <section className='a-panel'>
                    <div className='a-filters'>
                        <input
                            type='search'
                            placeholder={ta('sessions.search')}
                            aria-label={ta('sessions.search')}
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
                        <select aria-label={ta('sessions.workspace')} value={kind} onChange={e => setKind(e.target.value as typeof kind)}>
                            <option value=''>{ta('sessions.allKinds')}</option>
                            <option value='volume'>{ta('saved')}</option>
                            <option value='none'>{ta('temp')}</option>
                        </select>
                        <select aria-label={ta('sessions.phase')} value={phase} onChange={e => setPhase(e.target.value as typeof phase)}>
                            <option value=''>{ta('sessions.allPhases')}</option>
                            {(['Pending', 'Starting', 'Ready', 'Failed'] as const).map(p => (
                                <option key={p} value={p}>
                                    {ta(`phase.${p}`)}
                                </option>
                            ))}
                        </select>
                    </div>
                    {list.failed && !shown ? (
                        <LoadError />
                    ) : shown ? (
                        <>
                            <SessionsTable items={shown.items} selected={selected} onSelect={setSelected} />
                            <div className='a-pad a-foot'>
                                <span className='small'>{ta('sessions.total', { shown: shown.items.length, total: shown.total })}</span>
                                {shown.next && limit < 500 && (
                                    <button className='a-btn' onClick={() => setLimit(l => Math.min(l + PAGE, 500))}>
                                        {ta('sessions.more')}
                                    </button>
                                )}
                            </div>
                        </>
                    ) : (
                        <p className='a-pad' role='status'>
                            {ta('loading')}
                        </p>
                    )}
                </section>
                <Panel title={d ? <Mono>{d.name}</Mono> : ta('sessions.title')}>
                    {!d ? (
                        <Empty>{ta('sessions.select')}</Empty>
                    ) : (
                        <>
                            <Kv
                                rows={[
                                    [ta('sessions.template'), <TplCell name={d.template} />],
                                    [
                                        ta('sessions.workspace'),
                                        <>
                                            <WsCell saved={d.persistence === 'volume'} />
                                            {d.workspace && (
                                                <>
                                                    {' '}
                                                    <Mono>{d.workspace}</Mono>
                                                </>
                                            )}
                                        </>
                                    ],
                                    [ta('sessions.phase'), <PhasePill phase={d.phase} />],
                                    ...(d.title ? [[ta('sessions.title2'), d.title] as [string, string]] : []),
                                    [ta('sessions.user'), <Mono>{d.ownerHash}</Mono>],
                                    ...(d.errorCode
                                        ? [[ta('sessions.error'), `${d.errorCode}${d.message ? `: ${d.message}` : ''}`] as [string, string]]
                                        : []),
                                    [
                                        ta('sessions.launchData'),
                                        <span className='small'>
                                            {d.launchDataKeys.length > 0 ? d.launchDataKeys.join(', ') : ta('none')} (
                                            {ta('sessions.keysOnly')})
                                        </span>
                                    ]
                                ]}
                            />
                            <div className='a-pad a-sep'>
                                <b>{ta('sessions.timeline')}</b>
                            </div>
                            <ul className='a-tl'>
                                {timeline
                                    .filter(([, at]) => at)
                                    .map(([label, at]) => (
                                        <li key={label}>
                                            <span className='dot' />
                                            {label}
                                            <span className='mono'>{formatClock(at!, lang)}</span>
                                        </li>
                                    ))}
                            </ul>
                            {d.traceUrl && (
                                <div className='a-pad a-sep'>
                                    <TraceLink url={d.traceUrl} label={ta('sessions.traceLink')} />
                                </div>
                            )}
                        </>
                    )}
                </Panel>
            </div>
        </>
    );
};
