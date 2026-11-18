import './App.css';

import { useEffect, useMemo, useState } from 'react';

import type { Api } from './api';
import type { LaunchRequest, Template } from './api-client';
import { readArtemisToken } from './artemisToken';
import { Home } from './components/Home';
import { Launch } from './components/Launch';
import { Shell } from './components/Shell';
import type { AppConfig } from './config';
import { useI18n } from './i18n';
import { buildLaunchRequest, parseLaunchParams } from './launch';
import type { Row } from './overview';
import { buildLaunchInfo, type LaunchInfo } from './templates';
import type { UserInfo } from './user';

interface AppProps {
    config: AppConfig;
    api: Api;
    user?: UserInfo;
    logoutUrl?: string;
}

function App({ config, api, user, logoutUrl }: AppProps): React.JSX.Element {
    const { t } = useI18n();
    const [launch, setLaunch] = useState<{ request: LaunchRequest; info: LaunchInfo }>();
    const [templates, setTemplates] = useState<Template[]>();
    const [error, setError] = useState<string>();
    const link = useMemo(() => parseLaunchParams(window.location.search), []);

    useEffect(() => {
        api.client
            .GET('/templates')
            .then(({ data, error: problem }) => {
                if (data) {
                    setTemplates(data);
                } else {
                    setError(problem?.detail ?? problem?.title ?? t('templatesFailed'));
                }
            })
            .catch(console.error);
        // The message language does not need to restart the request.
        // eslint-disable-next-line react-hooks/exhaustive-deps
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

    // A link from Artemis (?appDef=...) starts right away once the templates are known. Launches are idempotent (G14),
    // so a reload returns to the same session.
    useEffect(() => {
        if (!link || !templates) {
            return;
        }
        const template = templates.find(tpl => tpl.name === link.appDef);
        if (!template) {
            setError(t('unknownTemplate', { name: link.appDef }));
            return;
        }
        const request = buildLaunchRequest(
            { ...link, artemisToken: readArtemisToken(window.location.search, window.sessionStorage) },
            template
        );
        setLaunch({ request, info: buildLaunchInfo(template, request) });
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

    return (
        <Shell config={config} user={user} logoutUrl={logoutUrl}>
            {launch ? (
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
                />
            )}
        </Shell>
    );
}

export default App;
