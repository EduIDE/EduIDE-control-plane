import React from 'react';
import { createRoot } from 'react-dom/client';

import { createApi } from './api';
import App from './App';
import { captureFromWindow } from './artemisToken';
import { initKeycloak, tokenProvider } from './auth';
import { LegalPage } from './components/LegalPage';
import { Shell } from './components/Shell';
import { loadConfig } from './config';
import { loadLang, translate } from './i18n';
import { I18nProvider } from './I18nProvider';
import { legalRoute } from './legal';
import { userFromToken } from './user';

async function start(): Promise<void> {
    const root = createRoot(document.getElementById('root')!);
    try {
        // Order matters (G16): the fragment must be gone before Keycloak reads the URL.
        captureFromWindow();
        const config = await loadConfig();
        document.title = config.title ?? 'EduIDE';
        // The legal pages and the footer must work without an account, so they render before Keycloak asks for a login.
        const route = legalRoute(window.location.pathname);
        if (route) {
            root.render(
                <React.StrictMode>
                    <I18nProvider>
                        <Shell config={config}>
                            <LegalPage route={route} page={config.legal[route]} />
                        </Shell>
                    </I18nProvider>
                </React.StrictMode>
            );
            return;
        }
        const keycloak = await initKeycloak(config);
        const api = createApi(config.apiBaseUrl, tokenProvider(keycloak));
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
