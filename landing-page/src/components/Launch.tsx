import { ArrowLeft, Check } from 'lucide-react';
import { useEffect, useState } from 'react';

import type { Api } from '../api';
import type { LaunchRequest, Template } from '../api-client';
import { clearArtemisToken } from '../artemisToken';
import { hasMessage, type MessageKey, type Translate, useI18n } from '../i18n';
import type { LaunchState } from '../launch';
import { type Row, sessionToRow } from '../overview';
import { runLaunch } from '../runLaunch';
import type { LaunchInfo } from '../templates';
import { EnvironmentRows } from './EnvironmentRows';
import { Logo } from './Icons';

interface LaunchProps {
    api: Api;
    request: LaunchRequest;
    info: LaunchInfo;
    templates: Template[];
    onBack: () => void;
    onStartRow: (row: Row) => void;
}

const STEPS: MessageKey[] = ['launch.accepted', 'launch.starting', 'launch.opening'];

// The steps follow the API phases: the answer to the request, Pending and Starting, then Ready (which redirects).
const activeStep = (state: LaunchState): number => (state.kind === 'creating' ? 0 : state.kind === 'starting' ? 1 : 2);

function failureText(t: Translate, code: string, message?: string): string {
    const key = `error.${code}`;
    return hasMessage(key) ? t(key) : (message ?? t('error.generic'));
}

/** Starts the environment, shows progress and redirects when it is ready. */
export const Launch = ({ api, request, info, templates, onBack, onStartRow }: LaunchProps): React.JSX.Element => {
    const { t } = useI18n();
    const [state, setState] = useState<LaunchState>({ kind: 'creating' });
    const [attempt, setAttempt] = useState(0);
    const [removed, setRemoved] = useState<string[]>([]);

    useEffect(() => {
        const controller = new AbortController();
        runLaunch(api.launchDeps, request, setState, controller.signal).catch(console.error);
        return () => controller.abort();
    }, [api, request, attempt]);

    useEffect(() => {
        if (state.kind === 'ready') {
            try {
                clearArtemisToken(window.sessionStorage);
            } catch {
                // Blocked storage holds no token either.
            }
            window.location.replace(state.url);
        }
    }, [state]);

    const backButton = (
        <button className='btn' onClick={onBack}>
            <ArrowLeft aria-hidden />
            {t('back')}
        </button>
    );

    if (state.kind === 'failed' && state.code === 'session-limit-reached' && state.sessions) {
        const rows = state.sessions.map(s => sessionToRow(s, templates)).filter(r => !removed.includes(r.key));
        return (
            <div className='launch wide'>
                <div className='alert danger' role='alert'>
                    <b>{t('limit.title', { n: rows.length })}</b>
                    {t('limit.text')}
                </div>
                <EnvironmentRows rows={rows} api={api} onStart={onStartRow} onDeleted={row => setRemoved(r => [...r, row.key])} />
                <div className='buttons'>{backButton}</div>
            </div>
        );
    }
    if (state.kind === 'failed') {
        return (
            <div className='launch'>
                <div className='alert danger' role='alert'>
                    {failureText(t, state.code, state.message)}
                </div>
                <div className='buttons'>
                    {backButton}
                    <button className='btn primary' onClick={() => setAttempt(a => a + 1)}>
                        {t('tryAgain')}
                    </button>
                </div>
            </div>
        );
    }
    const active = activeStep(state);
    return (
        <div className='launch'>
            <div className='head'>
                <Logo url={info.logoUrl} />
                <div>
                    <div className='title'>{info.title}</div>
                    <div className='sub'>{info.templateName}</div>
                </div>
            </div>
            <ol className='steps' aria-label={t('launch.steps')} role='status'>
                {STEPS.map((step, i) => (
                    <li key={step} className={i < active ? 'done' : i === active ? 'now' : undefined}>
                        <span className='dot'>{i < active && <Check aria-hidden />}</span>
                        {t(step)}
                    </li>
                ))}
            </ol>
            {info.seconds !== undefined && <p className='hint'>{t('launch.estimate', { n: info.seconds })}</p>}
        </div>
    );
};
