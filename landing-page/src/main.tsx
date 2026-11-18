import './index.css';

import React from 'react';
import { createRoot } from 'react-dom/client';

import { createApi } from './api';
import App from './App';
import { captureFromWindow } from './artemisToken';
import { initKeycloak, tokenProvider } from './auth';
import { loadConfig } from './config';
import { ThemeProvider } from './contexts/ThemeContext';

async function start(): Promise<void> {
    const root = createRoot(document.getElementById('root')!);
    try {
        // Order matters (G16): the fragment must be gone before Keycloak reads the URL.
        captureFromWindow();
        const config = await loadConfig();
        document.title = config.title ?? 'EduIDE';
        const keycloak = await initKeycloak(config);
        const api = createApi(config.apiBaseUrl, tokenProvider(keycloak));
        const user = keycloak.tokenParsed?.preferred_username as string | undefined;
        root.render(
            <React.StrictMode>
                <ThemeProvider>
                    <App config={config} api={api} user={user} logoutUrl={keycloak.createLogoutUrl()} />
                </ThemeProvider>
            </React.StrictMode>
        );
    } catch (err) {
        console.error(err);
        root.render(<strong>The page could not be started. Please reload.</strong>);
    }
}

start().catch(console.error);
