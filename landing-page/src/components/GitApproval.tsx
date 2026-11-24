import { Check, Link2, Trash2, X } from 'lucide-react';
import { useCallback, useEffect, useState } from 'react';

import type { Api } from '../api';
import type { Problem, Session } from '../api-client';
import type { GitProvider, GitRepositories } from '../apiTypes';
import { formatAgo } from '../format';
import { type ApprovalRequest, startConnect } from '../git';
import { useI18n } from '../i18n';
import { gitErrorText } from './gitError';
import { useNow } from './useNow';

interface GitApprovalProps {
    api: Api;
    request: ApprovalRequest | 'invalid';
    providers: GitProvider[];
}

type Outcome = { kind: 'approved' } | { kind: 'declined' } | { kind: 'failed'; code: string };

const codeOf = (error: unknown): string => (error as Partial<Problem> | undefined)?.code ?? 'internal';

/**
 * Approval of a repository for a running session. The link parameters are attacker controlled: they are shown as
 * text, validated before use, and nothing is approved without a click on "Allow". "Decline" only closes the request.
 */
export const GitApproval = ({ api, request, providers }: GitApprovalProps): React.JSX.Element => {
    const { t, lang } = useI18n();
    const now = useNow();
    const valid = request !== 'invalid';
    const session = valid ? request.session : '';
    const [info, setInfo] = useState<Session>();
    const [repos, setRepos] = useState<GitRepositories>();
    const [loadError, setLoadError] = useState<string>();
    const [outcome, setOutcome] = useState<Outcome>();
    const [busy, setBusy] = useState(false);

    const loadRepos = useCallback(async (): Promise<void> => {
        const { data, error } = await api.client.GET('/sessions/{name}/git/repositories', { params: { path: { name: session } } });
        if (data) {
            setRepos(data);
        } else {
            setLoadError(codeOf(error));
        }
    }, [api, session]);

    useEffect(() => {
        if (!valid) {
            return;
        }
        api.client
            .GET('/sessions/{name}', { params: { path: { name: session } } })
            .then(({ data, error }) => (data ? setInfo(data) : setLoadError(codeOf(error))))
            .catch(() => setLoadError('unreachable'));
        loadRepos().catch(() => setLoadError('unreachable'));
    }, [api, valid, session, loadRepos]);

    if (!valid) {
        return (
            <div className='launch' data-testid='git-approval'>
                <div className='alert danger' role='alert'>
                    {t('git.approve.invalid')}
                </div>
                <div className='buttons'>
                    <a className='btn' href='/'>
                        {t('git.callback.home')}
                    </a>
                </div>
            </div>
        );
    }

    const host = new URL(request.repository).host;
    const provider = providers.find(p => p.host.toLowerCase() === host);
    const allow = async (): Promise<void> => {
        setBusy(true);
        try {
            const { data, error } = await api.client.PUT('/sessions/{name}/git/repositories', {
                params: { path: { name: session } },
                body: { repository: request.repository, access: request.access }
            });
            setOutcome(data ? { kind: 'approved' } : { kind: 'failed', code: codeOf(error) });
            if (data) {
                await loadRepos();
            }
        } catch {
            setOutcome({ kind: 'failed', code: 'unreachable' });
        }
        setBusy(false);
    };
    const remove = async (repository: string): Promise<void> => {
        setBusy(true);
        try {
            const { response } = await api.client.DELETE('/sessions/{name}/git/repositories', {
                params: { path: { name: session } },
                body: { repository }
            });
            if (response.status === 204) {
                await loadRepos();
            } else {
                setOutcome({ kind: 'failed', code: 'internal' });
            }
        } catch {
            setOutcome({ kind: 'failed', code: 'unreachable' });
        }
        setBusy(false);
    };
    const title = info?.display?.title ?? info?.template ?? session;
    const failed = outcome?.kind === 'failed' ? outcome.code : undefined;
    const blocked = loadError ?? (repos?.mode === 'off' ? 'git-approval-disabled' : undefined);
    const finished = outcome?.kind === 'approved' || outcome?.kind === 'declined';

    return (
        <div className='launch' data-testid='git-approval'>
            <h1>{t('git.approve.title')}</h1>
            <dl className='facts'>
                <dt>{t('git.approve.session')}</dt>
                <dd>
                    {title}
                    {info && <span className='sub'> ({t('git.approve.started', { time: formatAgo(info.createdAt, now, lang) })})</span>}
                </dd>
                <dt>{t('git.approve.repo')}</dt>
                <dd data-testid='approve-repository'>{request.repository}</dd>
                <dt>{t('git.approve.access')}</dt>
                <dd data-testid='approve-access'>{t(request.access === 'write' ? 'git.approve.write' : 'git.approve.read')}</dd>
            </dl>
            <p className='sub'>{t('git.approve.warn')}</p>
            {(blocked ?? failed) && (
                <div className='alert danger' role='alert'>
                    {gitErrorText(t, (blocked ?? failed)!)}
                </div>
            )}
            {outcome?.kind === 'approved' && (
                <div className='alert ok' role='status'>
                    <Check aria-hidden /> {t('git.approve.done')}
                </div>
            )}
            {outcome?.kind === 'declined' && (
                <div className='alert' role='status'>
                    {t('git.approve.declined')}
                </div>
            )}
            {!finished && (
                <div className='buttons'>
                    <button className='btn primary' disabled={busy || !!blocked} onClick={() => allow().catch(console.error)}>
                        <Check aria-hidden />
                        {t('git.approve.allow')}
                    </button>
                    <button className='btn' disabled={busy} onClick={() => setOutcome({ kind: 'declined' })}>
                        <X aria-hidden />
                        {t('git.approve.decline')}
                    </button>
                    {failed === 'git-not-connected' && provider && (
                        <button className='btn' disabled={busy} onClick={() => startConnect(api, provider.name).catch(console.error)}>
                            <Link2 aria-hidden />
                            {t('git.connectLabel', { name: provider.displayName })}
                        </button>
                    )}
                </div>
            )}
            {repos && (
                <section aria-labelledby='approved-list'>
                    <h2 id='approved-list'>{t('git.approve.list')}</h2>
                    <ul className='git-rows'>
                        {repos.sessionRepository && (
                            <li className='git-row'>
                                <div className='meta'>
                                    <div className='title'>{repos.sessionRepository}</div>
                                    <div className='sub'>{t('git.approve.start')}</div>
                                </div>
                            </li>
                        )}
                        {repos.approved.map(a => (
                            <li key={a.repository} className='git-row'>
                                <div className='meta'>
                                    <div className='title'>{a.repository}</div>
                                    <div className='sub'>{t(a.access === 'write' ? 'git.approve.write' : 'git.approve.read')}</div>
                                </div>
                                <button
                                    className='btn'
                                    disabled={busy}
                                    aria-label={t('git.approve.removeLabel', { repo: a.repository })}
                                    onClick={() => remove(a.repository).catch(console.error)}
                                >
                                    <Trash2 aria-hidden />
                                    {t('git.approve.remove')}
                                </button>
                            </li>
                        ))}
                    </ul>
                    {!repos.sessionRepository && repos.approved.length === 0 && <p className='sub'>{t('git.approve.none')}</p>}
                </section>
            )}
            <div className='buttons'>
                <a className='btn' href='/'>
                    {t('git.callback.home')}
                </a>
            </div>
        </div>
    );
};
