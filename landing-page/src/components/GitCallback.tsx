import { useEffect, useRef, useState } from 'react';

import type { Api } from '../api';
import { type CallbackParams, completeCallback, takeGitCallback } from '../git';
import { useI18n } from '../i18n';
import { gitErrorText } from './gitError';

type State = { kind: 'working' } | { kind: 'done'; login: string } | { kind: 'failed'; code: string };

/**
 * The route /git/callback. The values from the Git host were moved into sessionStorage before the login and are
 * taken out of it here, so they are used once. They go to POST /git/callback and nowhere else.
 */
export const GitCallback = ({ api, params }: { api: Api; params?: CallbackParams }): React.JSX.Element => {
    const { t } = useI18n();
    const [state, setState] = useState<State>({ kind: 'working' });
    // A strict-mode effect runs twice, and the stored values can be taken only once.
    const started = useRef(false);

    useEffect(() => {
        if (started.current) {
            return;
        }
        started.current = true;
        const p = params ?? takeGitCallback(window.sessionStorage);
        if (!p) {
            setState({ kind: 'failed', code: 'missing' });
            return;
        }
        completeCallback(api, p)
            .then(result => {
                if (!result.ok) {
                    setState({ kind: 'failed', code: result.code });
                    return;
                }
                setState({ kind: 'done', login: result.login });
                window.location.replace(result.returnTo);
            })
            .catch(console.error);
    }, [api, params]);

    return (
        <div className='launch' data-testid='git-callback'>
            {state.kind === 'working' && (
                <p role='status' className='sub'>
                    {t('git.callback.working')}
                </p>
            )}
            {state.kind === 'done' && (
                <p role='status' className='sub'>
                    {t('git.callback.success', { login: state.login })}
                </p>
            )}
            {state.kind === 'failed' && (
                <>
                    <div className='alert danger' role='alert'>
                        {state.code === 'missing' ? t('git.callback.missing') : gitErrorText(t, state.code)}
                    </div>
                    <div className='buttons'>
                        <a className='btn primary' href='/'>
                            {t('git.callback.home')}
                        </a>
                    </div>
                </>
            )}
        </div>
    );
};
