import './App.css';

import { useCallback, useEffect, useMemo, useState } from 'react';

import type { Api } from './api';
import type { LaunchRequest, Template } from './api-client';
import { readArtemisToken } from './artemisToken';
import { ErrorComponent } from './components/ErrorComponent';
import { Footer } from './components/Footer';
import { Header } from './components/Header';
import { Imprint } from './components/Imprint';
import { Launch } from './components/Launch';
import { Overview } from './components/Overview';
import { Privacy } from './components/Privacy';
import { SelectApp } from './components/SelectApp';
import type { AppConfig } from './config';
import { buildLaunchRequest, parseLaunchParams } from './launch';

type Page = 'home' | 'imprint' | 'privacy' | 'overview';

const PATHS: Record<Page, string> = { home: '/', imprint: '/imprint', privacy: '/privacy', overview: '/me/overview' };

function pageFromPath(pathname: string): Page {
    const path = pathname.replace(/\/+$/, '');
    return (Object.keys(PATHS) as Page[]).find(p => PATHS[p] === path && p !== 'home') ?? 'home';
}

interface AppProps {
    config: AppConfig;
    api: Api;
    user?: string;
    logoutUrl?: string;
}

function App({ config, api, user, logoutUrl }: AppProps): React.JSX.Element {
    const [page, setPage] = useState<Page>(() => pageFromPath(window.location.pathname));
    const [request, setRequest] = useState<LaunchRequest>();
    const [templates, setTemplates] = useState<Template[]>();
    const [error, setError] = useState<string>();
    const link = useMemo(() => parseLaunchParams(window.location.search), []);

    useEffect(() => {
        const onPop = (): void => setPage(pageFromPath(window.location.pathname));
        window.addEventListener('popstate', onPop);
        return () => window.removeEventListener('popstate', onPop);
    }, []);

    const navigate = useCallback((to: Page): void => {
        window.history.pushState({}, '', PATHS[to]);
        setPage(to);
        setRequest(undefined);
    }, []);

    useEffect(() => {
        api.client.GET('/templates').then(({ data, error: problem }) => {
            if (data) {
                setTemplates(data);
            } else {
                setError(problem?.detail ?? problem?.title ?? 'Could not load the environments.');
            }
        }).catch(console.error);
    }, [api]);

    // A link from Artemis (?appDef=...) starts right away once the templates are known. Launches are idempotent (G14),
    // so a reload returns to the same session.
    useEffect(() => {
        if (!link || !templates || page !== 'home') {
            return;
        }
        const template = templates.find(t => t.name === link.appDef);
        if (!template) {
            setError(`Unknown environment: ${link.appDef}`);
            return;
        }
        setRequest(
            buildLaunchRequest({ ...link, artemisToken: readArtemisToken(window.location.search, window.sessionStorage) }, template)
        );
    }, [link, templates, page]);

    const back = (): void => {
        // Drop the launch link, otherwise leaving the launch screen would start it again.
        window.history.replaceState({}, '', PATHS.home);
        window.location.reload();
    };

    let content: React.JSX.Element;
    if (page === 'imprint') {
        content = <Imprint onNavigate={navigate} />;
    } else if (page === 'privacy') {
        content = <Privacy onNavigate={navigate} privacy={config.privacy} />;
    } else if (request) {
        content = <Launch api={api} request={request} onBack={back} />;
    } else if (page === 'overview') {
        content = (
            <Overview
                api={api}
                onSelectEnvironment={() => navigate('home')}
                onStart={row => row.item.workspace && setRequest({ template: row.item.template, workspace: row.item.workspace.name })}
            />
        );
    } else {
        content = (
            <>
                <ErrorComponent message={error} />
                {templates && <SelectApp templates={templates} onSelect={t => setRequest(buildLaunchRequest({ appDef: t.name }, t))} />}
                <p>
                    <button className='App__link-button' onClick={() => navigate('overview')}>
                        My environments
                    </button>
                </p>
            </>
        );
    }

    return (
        <div className='App'>
            <Header email={user} logoutUrl={logoutUrl} title={config.title} />
            <div className='App__content'>{content}</div>
            <Footer selectedAppDefinition='' onNavigate={navigate} footerLinks={config.footerLinks} />
        </div>
    );
}

export default App;
