import './App.css';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import type { Api } from './api';
import type { LaunchRequest, Template } from './api-client';
import type { GitProvider } from './apiTypes';
import { clearArtemisToken, hasCredentials, readArtemisToken, readGitUri } from './artemisToken';
import { GitApproval } from './components/GitApproval';
import { Gate, GitGate } from './components/GitGate';
import { Home } from './components/Home';
import { Launch } from './components/Launch';
import { RepoPicker } from './components/RepoPicker';
import { Shell } from './components/Shell';
import { useTemplates } from './components/useTemplates';
import type { AppConfig } from './config';
import { loadProviders, parseApproval, preflight } from './git';
import { useI18n } from './i18n';
import { buildLaunchRequest, displaySource, type LaunchParams, parseLaunchParams, parseRepositoryLink } from './launch';
import type { Row } from './overview';
import { buildLaunchInfo, type LaunchInfo } from './templates';
import type { UserInfo } from './user';

interface AppProps {
    config: AppConfig;
    api: Api;
    user?: UserInfo;
    logoutUrl?: string;
}

function forgetLaunchData(): void {
    try {
        clearArtemisToken(window.sessionStorage);
    } catch {
        // Blocked storage holds nothing.
    }
}

function App({ config, api, user, logoutUrl }: AppProps): React.JSX.Element {
    const { t } = useI18n();
    const [launch, setLaunch] = useState<{ request: LaunchRequest; info: LaunchInfo }>();
    const { templates, error: templatesError, reload } = useTemplates(api, t('templatesFailed'));
    const [linkError, setLinkError] = useState<string>();
    const error = linkError ?? templatesError;
    const [admin, setAdmin] = useState(false);
    // An approval link never starts anything, whatever else it carries.
    const approval = useMemo(() => parseApproval(window.location.search), []);
    const link = useMemo(
        () => (approval ? undefined : (parseLaunchParams(window.location.search) ?? parseRepositoryLink(window.location.search))),
        [approval]
    );
    const [gate, setGate] = useState<Gate>();
    const [picker, setPicker] = useState<{ params: LaunchParams; repository: string; unknownName?: string; candidates: Template[] }>();
    const linkStarted = useRef(false);
    const [providers, setProviders] = useState<GitProvider[]>([]);
    // After the templates request, so a failed first request still belongs to the templates.
    const reloadProviders = useCallback((): void => {
        loadProviders(api).then(setProviders).catch(console.error);
    }, [api]);
    useEffect(reloadProviders, [reloadProviders]);

    // The menu entry is a convenience: the service checks the role again on every admin request. The check runs the
    // first time the menu opens, so students do not cause a 403 on every page load.
    const adminChecked = useRef(false);
    const checkAdmin = useCallback((): void => {
        if (adminChecked.current) {
            return;
        }
        adminChecked.current = true;
        api.client
            .GET('/admin/status')
            .then(({ response }) => setAdmin(response.status === 200))
            .catch(() => undefined);
    }, [api]);

    const begin = (request: LaunchRequest, known?: Parameters<typeof buildLaunchInfo>[2]): void =>
        setLaunch({
            request,
            info: buildLaunchInfo(
                templates?.find(tpl => tpl.name === request.template),
                request,
                known
            )
        });

    // A launch with a repository first asks the service whether the account can reach it. The check never blocks: when
    // it fails or says nothing useful, the launch goes ahead as before. Repository URLs with credentials (Artemis) are
    // not sent to it, they come with their own token.
    const startChecked = (request: LaunchRequest, template: Template): void => {
        const gitUri = request.data?.GIT_URI;
        const go = (): void => {
            setGate(undefined);
            setLaunch({ request, info: buildLaunchInfo(template, request) });
        };
        if (!gitUri || hasCredentials(gitUri)) {
            go();
            return;
        }
        setGate({ kind: 'checking' });
        preflight(api, gitUri)
            .then(result => {
                const provider = result?.provider;
                if (result?.result === 'connect' && provider) {
                    setGate({ kind: 'connect', provider });
                } else if (result?.result === 'no-access' && provider) {
                    loadProviders(api)
                        .then(list => setGate({ kind: 'no-access', provider, login: list.find(p => p.name === provider.name)?.login }))
                        .catch(go);
                } else {
                    go();
                }
            })
            .catch(go);
    };

    // A link from Artemis (?appDef=...) starts right away once the templates are known. Launches are idempotent (G14),
    // so a reload returns to the same session. A link with a repository but without a usable environment asks for one.
    useEffect(() => {
        if (!link) {
            // No launch link: launch data a former link left behind must not end up in a later launch.
            forgetLaunchData();
            return;
        }
        if (!templates || linkStarted.current) {
            return;
        }
        linkStarted.current = true;
        const template = templates.find(tpl => tpl.name === link.appDef);
        const gitUri = readGitUri(window.location.search, window.sessionStorage);
        if (!template && link.gitUri) {
            // The picker never forwards the Artemis token or URL: a link without a known environment is not an Artemis launch.
            forgetLaunchData();
            setPicker({
                params: { ...link, artemisUrl: undefined },
                repository: displaySource(link.gitUri).replace(/^https:\/\//, ''),
                unknownName: link.appDef || undefined,
                candidates: templates.filter(tpl => tpl.listed && tpl.injectableKeys.includes('GIT_URI'))
            });
            return;
        }
        if (!template) {
            forgetLaunchData();
            setLinkError(t('unknownTemplate', { name: link.appDef }));
            return;
        }
        const request = buildLaunchRequest(
            {
                ...link,
                gitUri,
                artemisToken: readArtemisToken(window.location.search, window.sessionStorage)
            },
            template
        );
        startChecked(request, template);
        // Only the first load of the templates starts the link.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [link, templates]);

    const startRow = (row: Row): void => {
        if (row.openUrl) {
            window.location.assign(row.openUrl);
        } else if (row.workspaceName) {
            begin(
                { template: row.template, workspace: row.workspaceName },
                { title: row.title, templateName: row.templateName, logoUrl: row.logoUrl }
            );
        }
    };

    const back = (): void => {
        // Drop the launch link, otherwise leaving the launch screen would start it again.
        window.history.replaceState({}, '', '/');
        window.location.reload();
    };

    const pickTemplate = (template: Template): void => {
        if (picker) {
            startChecked(buildLaunchRequest(picker.params, template), template);
        }
    };

    return (
        <Shell config={config} user={user} logoutUrl={logoutUrl} admin={admin} onMenuOpen={checkAdmin}>
            {approval ? (
                <GitApproval api={api} request={approval} providers={providers} />
            ) : gate && !launch ? (
                <GitGate api={api} gate={gate} onBack={back} />
            ) : picker && !launch ? (
                <RepoPicker
                    repository={picker.repository}
                    unknownName={picker.unknownName}
                    templates={picker.candidates}
                    onPick={pickTemplate}
                    onBack={back}
                />
            ) : launch ? (
                <Launch
                    api={api}
                    request={launch.request}
                    info={launch.info}
                    templates={templates ?? []}
                    onBack={back}
                    onStartRow={startRow}
                />
            ) : (
                <Home
                    api={api}
                    templates={templates}
                    error={error}
                    firstName={user?.firstName}
                    onLaunch={request => begin(request)}
                    onStartRow={startRow}
                    onReconnect={reload}
                    providers={providers}
                    onProvidersChange={reloadProviders}
                />
            )}
        </Shell>
    );
}

export default App;
