import { Link2, Unlink, X } from 'lucide-react';
import { useState } from 'react';

import type { Api } from '../api';
import type { GitProvider } from '../apiTypes';
import { disconnectProvider, startConnect } from '../git';
import { useI18n } from '../i18n';
import { Dialog } from './Dialog';
import { gitErrorText } from './gitError';

interface GitAccountsProps {
    api: Api;
    providers: GitProvider[];
    /** The list changed on the server (a disconnect): load it again. */
    onChange: () => void;
}

/** One row per provider: connect, or the account login with disconnect. Shown only when the deployment has providers. */
export const GitAccounts = ({ api, providers, onChange }: GitAccountsProps): React.JSX.Element | undefined => {
    const { t } = useI18n();
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<string>();
    const [disconnecting, setDisconnecting] = useState<GitProvider>();
    if (providers.length === 0) {
        return undefined;
    }

    const connect = async (p: GitProvider): Promise<void> => {
        setBusy(true);
        setError(undefined);
        const code = await startConnect(api, p.name);
        // On success the browser leaves for the Git host, so only a failure gets here.
        if (code) {
            setError(gitErrorText(t, code));
            setBusy(false);
        }
    };
    const confirmDisconnect = async (p: GitProvider): Promise<void> => {
        setBusy(true);
        const ok = await disconnectProvider(api, p.name);
        setBusy(false);
        setDisconnecting(undefined);
        setError(ok ? undefined : t('git.error.generic'));
        onChange();
    };

    return (
        <section aria-labelledby='git-accounts'>
            <h2 id='git-accounts'>{t('git.title')}</h2>
            <p className='sub'>{t('git.intro')}</p>
            <p className='sub'>{t('git.logged')}</p>
            {error && (
                <div className='alert danger' role='alert'>
                    {error}
                </div>
            )}
            <ul className='git-rows'>
                {providers.map(p => (
                    <li key={p.name} className='git-row' data-testid={`git-provider-${p.name}`}>
                        <div className='meta'>
                            <div className='title'>{p.displayName}</div>
                            <div className='sub'>
                                {p.connected ? t('git.connectedAs', { login: p.login ?? '' }) : t('git.notConnected')}
                            </div>
                        </div>
                        {p.connected ? (
                            <button
                                className='btn'
                                aria-label={t('git.disconnectLabel', { name: p.displayName })}
                                onClick={() => setDisconnecting(p)}
                            >
                                <Unlink aria-hidden />
                                {t('git.disconnect')}
                            </button>
                        ) : (
                            <button
                                className='btn primary'
                                disabled={busy}
                                aria-label={t('git.connectLabel', { name: p.displayName })}
                                onClick={() => connect(p).catch(console.error)}
                            >
                                <Link2 aria-hidden />
                                {t('git.connect')}
                            </button>
                        )}
                    </li>
                ))}
            </ul>
            {disconnecting && (
                <Dialog label={t('git.disconnectTitle', { name: disconnecting.displayName })} onClose={() => setDisconnecting(undefined)}>
                    <div className='dlg-head'>
                        <div>
                            <div className='title'>{t('git.disconnectTitle', { name: disconnecting.displayName })}</div>
                        </div>
                        <button className='icon-btn' aria-label={t('close')} onClick={() => setDisconnecting(undefined)}>
                            <X aria-hidden />
                        </button>
                    </div>
                    <div className='dlg-body'>
                        <p style={{ margin: 0 }}>{t('git.disconnectText')}</p>
                    </div>
                    <div className='dlg-foot'>
                        <span className='actions'>
                            <button className='btn' data-autofocus onClick={() => setDisconnecting(undefined)}>
                                {t('cancel')}
                            </button>
                            <button
                                className='btn danger'
                                disabled={busy}
                                onClick={() => confirmDisconnect(disconnecting).catch(console.error)}
                            >
                                {t('git.disconnect')}
                            </button>
                        </span>
                    </div>
                </Dialog>
            )}
        </section>
    );
};
