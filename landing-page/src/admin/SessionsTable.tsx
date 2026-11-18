import type { AdminSession } from '../apiTypes';
import { formatAgo, formatClock } from '../format';
import { useAdmin } from './context';
import { STOP_SOON_MS } from './format';
import { Empty, Mono, PhasePill, Scroll, TplCell, WsCell } from './ui';

interface SessionsTableProps {
    items: AdminSession[];
    selected?: string;
    onSelect?: (name: string) => void;
}

export const SessionsTable = ({ items, selected, onSelect }: SessionsTableProps): React.JSX.Element => {
    const { ta, lang, now } = useAdmin();
    if (items.length === 0) {
        return <Empty>{ta('sessions.empty')}</Empty>;
    }
    return (
        <Scroll>
            <table>
                <thead>
                    <tr>
                        <th>{ta('sessions.session')}</th>
                        <th>{ta('sessions.template')}</th>
                        <th>{ta('sessions.workspace')}</th>
                        <th>{ta('sessions.phase')}</th>
                        <th>{ta('sessions.start')}</th>
                        <th className='num'>{ta('sessions.age')}</th>
                        <th>{ta('sessions.stops')}</th>
                        <th>{ta('sessions.user')}</th>
                    </tr>
                </thead>
                <tbody>
                    {items.map(s => {
                        const soon = s.persistence === 'none' && s.stopsAt !== undefined && Date.parse(s.stopsAt) - now <= STOP_SOON_MS;
                        const select = (): void => onSelect?.(s.name);
                        return (
                            <tr
                                key={s.name}
                                className={`${onSelect ? 'click' : ''}${selected === s.name ? ' sel' : ''}`}
                                tabIndex={onSelect ? 0 : undefined}
                                aria-current={selected === s.name ? 'true' : undefined}
                                onClick={select}
                                onKeyDown={e => {
                                    if (onSelect && (e.key === 'Enter' || e.key === ' ')) {
                                        e.preventDefault();
                                        select();
                                    }
                                }}
                            >
                                <td>
                                    <Mono>{s.name}</Mono>
                                </td>
                                <td>
                                    <TplCell name={s.template} />
                                </td>
                                <td>
                                    <WsCell saved={s.persistence === 'volume'} />
                                </td>
                                <td>
                                    <PhasePill phase={s.phase} />
                                </td>
                                <td className='small'>
                                    {s.startStrategy === 'warm-ready' || s.startStrategy === 'warm-starting' || s.startStrategy === 'cold'
                                        ? ta(`strategy.${s.startStrategy}`)
                                        : (s.startStrategy ?? '')}
                                </td>
                                <td className='num'>{formatAgo(s.createdAt, now, lang)}</td>
                                <td className={soon ? 'bad-t' : undefined}>
                                    {s.stopsAt && (
                                        <>
                                            {formatClock(s.stopsAt, lang)}{' '}
                                            {s.stopReason === 'idle' || s.stopReason === 'max' ? (
                                                <span className='small'>{ta(`stop.${s.stopReason}`)}</span>
                                            ) : undefined}
                                        </>
                                    )}
                                </td>
                                <td>
                                    <Mono>{s.ownerHash}</Mono>
                                </td>
                            </tr>
                        );
                    })}
                </tbody>
            </table>
        </Scroll>
    );
};
