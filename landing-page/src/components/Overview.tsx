import { useEffect, useState } from 'react';

import type { Api } from '../api';
import type { Overview as OverviewData } from '../api-client';
import { type Row, type RowAction, toRows } from '../overview';

interface OverviewProps {
    api: Api;
    onStart: (row: Row) => void;
    onSelectEnvironment: () => void;
}

const LABELS: Record<RowAction, string> = { open: 'Open', stop: 'Stop', start: 'Start', delete: 'Delete' };

export const Overview = ({ api, onStart, onSelectEnvironment }: OverviewProps): React.JSX.Element => {
    const [data, setData] = useState<OverviewData>();
    const [error, setError] = useState<string>();

    useEffect(() => {
        const controller = new AbortController();
        let wait = 1000;
        // The stream starts with the current state; reconnect with a growing pause when it drops.
        const follow = async (): Promise<void> => {
            while (!controller.signal.aborted) {
                try {
                    await api.watchOverview(o => {
                        wait = 1000;
                        setError(undefined);
                        setData(o);
                    }, controller.signal);
                } catch {
                    if (controller.signal.aborted) {
                        return;
                    }
                    setError('Lost the connection to the service, retrying.');
                }
                await new Promise(resolve => setTimeout(resolve, wait));
                wait = Math.min(wait * 2, 15000);
            }
        };
        follow().catch(console.error);
        return () => controller.abort();
    }, [api]);

    const act = async (row: Row, action: RowAction): Promise<void> => {
        if (action === 'open' && row.item.session?.url) {
            window.location.assign(row.item.session.url);
        } else if (action === 'start') {
            onStart(row);
        } else if (action === 'stop' && row.item.session) {
            await api.client.DELETE('/sessions/{name}', { params: { path: { name: row.item.session.name } } });
        } else if (
            action === 'delete' &&
            row.item.workspace &&
            window.confirm(`Delete "${row.title}" and all its files? This cannot be undone.`)
        ) {
            await api.client.DELETE('/workspaces/{name}', { params: { path: { name: row.item.workspace.name } } });
        }
    };

    if (!data) {
        return <p role='status'>{error ?? 'Loading your environments...'}</p>;
    }
    const rows = toRows(data);
    if (rows.length === 0) {
        return (
            <div className='App__session-ended'>
                <p>You have no environments yet. Open an exercise in Artemis to start one, or pick an environment yourself.</p>
                <button className='App__try-now-button' onClick={onSelectEnvironment}>
                    Pick an environment
                </button>
            </div>
        );
    }
    return (
        <ul className='Overview__list'>
            {rows.map(row => (
                <li key={row.key} className='Overview__row'>
                    {row.logoUrl && <img src={row.logoUrl} alt='' className='Overview__logo' />}
                    <div className='Overview__text'>
                        <span className='Overview__title'>{row.title}</span>
                        <span className='Overview__subtitle'>
                            {row.subtitle} - {row.running ? 'running' : 'not running'}
                            {row.lastUsed && `, last used ${new Date(row.lastUsed).toLocaleString()}`}
                        </span>
                        {row.expiresSoon && (
                            <span role='alert' className='Overview__warning'>
                                Will be deleted on {new Date(row.expiresSoon).toLocaleDateString()}. Start it to keep it.
                            </span>
                        )}
                    </div>
                    {row.actions.map(a => (
                        <button key={a} className='App__try-now-button' onClick={() => act(row, a).catch(console.error)}>
                            {LABELS[a]}
                        </button>
                    ))}
                </li>
            ))}
        </ul>
    );
};
