import { Clock, Play, Trash2, X } from 'lucide-react';
import { useState } from 'react';

import type { Api } from '../api';
import { formatAgo } from '../format';
import { useI18n } from '../i18n';
import { lifeText } from '../lifeText';
import { deleteRow, type Row } from '../overview';
import { Dialog } from './Dialog';
import { Logo } from './Icons';

interface EnvironmentRowsProps {
    rows: Row[];
    api: Api;
    onStart: (row: Row) => void;
    onDeleted?: (row: Row) => void;
}

export const EnvironmentRows = ({ rows, api, onStart, onDeleted }: EnvironmentRowsProps): React.JSX.Element => {
    const i18n = useI18n();
    const { t, lang } = i18n;
    const [deleting, setDeleting] = useState<Row>();
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<string>();
    const now = Date.now();

    const close = (): void => {
        setDeleting(undefined);
        setError(undefined);
    };

    const confirm = async (row: Row): Promise<void> => {
        setBusy(true);
        const result = await deleteRow(api.client, row);
        setBusy(false);
        if (result.ok) {
            close();
            onDeleted?.(row);
        } else {
            setError(t(result.timeout ? 'delete.timeout' : 'delete.failed'));
        }
    };

    return (
        <>
            <div className='list'>
                {rows.map(row => {
                    const life = lifeText(row, i18n);
                    return (
                        <div key={row.key} className='row'>
                            <Logo url={row.logoUrl} />
                            <div className='meta'>
                                <div className='title'>{row.title}</div>
                                <div className='sub'>
                                    {[
                                        row.templateName,
                                        t(row.saved ? 'row.saved' : 'row.temporary'),
                                        row.since &&
                                            t(row.since.kind === 'started' ? 'row.startedAgo' : 'row.lastUsedAgo', {
                                                time: formatAgo(row.since.at, now, lang)
                                            })
                                    ]
                                        .filter(Boolean)
                                        .join(' · ')}
                                </div>
                                {life && (
                                    <div className={`life${row.danger ? ' danger' : ''}`}>
                                        <Clock aria-hidden />
                                        {life}
                                    </div>
                                )}
                            </div>
                            <div className='actions'>
                                <button className='btn icon' aria-label={t('delete')} title={t('delete')} onClick={() => setDeleting(row)}>
                                    <Trash2 aria-hidden />
                                </button>
                                <button
                                    className='btn primary'
                                    disabled={row.running && !row.openUrl && !row.workspaceName}
                                    onClick={() => onStart(row)}
                                >
                                    <Play aria-hidden />
                                    {t('start')}
                                </button>
                            </div>
                        </div>
                    );
                })}
            </div>
            {deleting && (
                <Dialog label={t('delete.title', { title: deleting.title })} onClose={close}>
                    <div className='dlg-head'>
                        <Logo url={deleting.logoUrl} />
                        <div>
                            <div className='title'>{t('delete.title', { title: deleting.title })}</div>
                        </div>
                        <button className='icon-btn' aria-label={t('close')} onClick={close}>
                            <X aria-hidden />
                        </button>
                    </div>
                    <div className='dlg-body'>
                        <p style={{ margin: 0 }}>
                            {t('delete.text')}
                            {deleting.running && ` ${t('delete.running')}`}
                        </p>
                        {error && (
                            <div className='alert danger' role='alert' style={{ margin: 0 }}>
                                {error}
                            </div>
                        )}
                    </div>
                    <div className='dlg-foot'>
                        <span className='actions'>
                            <button className='btn' data-autofocus onClick={close}>
                                {t('cancel')}
                            </button>
                            <button className='btn danger' disabled={busy} onClick={() => confirm(deleting).catch(console.error)}>
                                <Trash2 aria-hidden />
                                {t('delete')}
                            </button>
                        </span>
                    </div>
                </Dialog>
            )}
        </>
    );
};
