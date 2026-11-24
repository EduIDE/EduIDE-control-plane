import React from 'react';
import { createRoot } from 'react-dom/client';

import { createApi } from './api';
import App from './App';
import { captureFromWindow } from './artemisToken';
import { initKeycloak, tokenProvider } from './auth';
import { GitCallback } from './components/GitCallback';
import { LegalPage } from './components/LegalPage';
import { Shell } from './components/Shell';
import { loadConfig } from './config';
import { captureGitCallbackFromWindow } from './git';
import { loadLang, translate } from './i18n';
import { I18nProvider } from './I18nProvider';
import { appRoute } from './routes';
import { userFromToken } from './user';

async function start(): Promise<void> {
    const root = createRoot(document.getElementById('root')!);
    try {
        // Order matters (G16): the fragment must be gone before Keycloak reads the URL.
        captureFromWindow();
        // The Git host's code and state leave the address bar the same way, before Keycloak sees the URL.
        captureGitCallbackFromWindow();
        const config = await loadConfig();
        document.title = config.title ?? 'EduIDE';
        // The legal pages and the footer must work without an account, so they render before Keycloak asks for a login.
        const route = appRoute(window.location.pathname);
        if (route.kind === 'legal') {
            root.render(
                <React.StrictMode>
                    <I18nProvider>
                        <Shell config={config}>
                            <LegalPage route={route.route} page={config.legal[route.route]} />
                        </Shell>
                    </I18nProvider>
                </React.StrictMode>
            );
            return;
        }
        if (route.kind === 'status') {
            // Like the legal pages, the status must stay reachable when sign-in is what is broken.
            const { StatusPage } = await import('./status/StatusPage');
            root.render(
                <React.StrictMode>
                    <I18nProvider>
                        <StatusPage config={config} />
                    </I18nProvider>
                </React.StrictMode>
            );
            return;
        }
        const keycloak = await initKeycloak(config);
        const api = createApi(config.apiBaseUrl, tokenProvider(keycloak));
        if (route.kind === 'admin') {
            // Lazy: students never download the admin panel. The service decides who may use it.
            const { AdminApp } = await import('./admin/AdminApp');
            root.render(
                <React.StrictMode>
                    <I18nProvider>
                        <AdminApp
                            config={config}
                            api={api}
                            user={userFromToken(keycloak.tokenParsed)}
                            logoutUrl={keycloak.createLogoutUrl()}
                        />
                    </I18nProvider>
                </React.StrictMode>
            );
            return;
        }
        if (route.kind === 'gitCallback') {
            root.render(
                <React.StrictMode>
                    <I18nProvider>
                        <Shell config={config} user={userFromToken(keycloak.tokenParsed)} logoutUrl={keycloak.createLogoutUrl()}>
                            <GitCallback api={api} />
                        </Shell>
                    </I18nProvider>
                </React.StrictMode>
            );
            return;
        }
        root.render(
            <React.StrictMode>
                <I18nProvider>
                    <App config={config} api={api} user={userFromToken(keycloak.tokenParsed)} logoutUrl={keycloak.createLogoutUrl()} />
                </I18nProvider>
            </React.StrictMode>
        );
    } catch (err) {
        console.error(err);
        root.render(<strong>{translate(loadLang(), 'startFailed')}</strong>);
    }
}

start().catch(console.error);
