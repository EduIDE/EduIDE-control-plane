import { ArrowLeft, Link2 } from 'lucide-react';
import { useState } from 'react';

import type { Api } from '../api';
import { disconnectProvider, startConnect } from '../git';
import { useI18n } from '../i18n';
import { gitErrorText } from './gitError';

export type Gate =
    | { kind: 'checking' }
    | { kind: 'connect'; provider: { name: string; displayName: string } }
    | { kind: 'no-access'; provider: { name: string; displayName: string }; login?: string };

interface GitGateProps {
    api: Api;
    gate: Gate;
    onBack: () => void;
}

/**
 * What the page shows before a launch with a repository when the preflight says the launch would fail. A connect
 * returns to this same link, so the launch continues without the user opening the link again. "Start anyway" is not
 * offered: the clone would fail.
 */
export const GitGate = ({ api, gate, onBack }: GitGateProps): React.JSX.Element => {
    const { t } = useI18n();
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<string>();

    if (gate.kind === 'checking') {
        return (
            <div className='launch'>
                <p role='status' className='sub'>
                    {t('git.gate.checking')}
                </p>
            </div>
        );
    }
    const { provider } = gate;
    const connect = async (switchAccount: boolean): Promise<void> => {
        setBusy(true);
        setError(undefined);
        if (switchAccount && !(await disconnectProvider(api, provider.name))) {
            setError(t('git.error.generic'));
            setBusy(false);
            return;
        }
        const code = await startConnect(api, provider.name);
        if (code) {
            setError(gitErrorText(t, code));
            setBusy(false);
        }
    };
    const connecting = gate.kind === 'connect';
    return (
        <div className='launch' data-testid={`git-gate-${gate.kind}`}>
            <div className='alert danger' role='alert'>
                <b>{connecting ? t('git.gate.connectTitle', { name: provider.displayName }) : t('git.gate.noAccessTitle')}</b>
                {connecting
                    ? t('git.gate.connectText')
                    : gate.login
                      ? t('git.gate.noAccessLogin', { name: provider.displayName, login: gate.login })
                      : t('git.gate.noAccess', { name: provider.displayName })}
            </div>
            {error && (
                <div className='alert danger' role='alert'>
                    {error}
                </div>
            )}
            <div className='buttons'>
                <button className='btn' onClick={onBack}>
                    <ArrowLeft aria-hidden />
                    {t('back')}
                </button>
                <button className='btn primary' disabled={busy} onClick={() => connect(!connecting).catch(console.error)}>
                    <Link2 aria-hidden />
                    {connecting ? t('git.connect') : t('git.gate.switch')}
                </button>
            </div>
        </div>
    );
};
