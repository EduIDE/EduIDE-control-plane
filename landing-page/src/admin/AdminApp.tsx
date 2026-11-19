import './admin.css';

import {
    Activity,
    ChevronDown,
    CircleAlert,
    ExternalLink,
    Eye,
    Gauge,
    House,
    Layers,
    LayoutGrid,
    LogOut,
    Monitor,
    Save
} from 'lucide-react';
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';

import type { Api } from '../api';
import type { AdminStatus } from '../apiTypes';
import { Footer } from '../components/Footer';
import { Shell } from '../components/Shell';
import { useNow } from '../components/useNow';
import { usePolling } from '../components/usePolling';
import { useWatch } from '../components/useWatch';
import type { AppConfig } from '../config';
import { type Lang, LANGS, useI18n } from '../i18n';
import { httpHref } from '../url';
import type { UserInfo } from '../user';
import { AdminContext, type AdminContextValue } from './context';
import { adminText } from './messages';
import { ok } from './query';
import { adminPage, type AdminPageName, adminPath } from './routes';

// Every page is its own chunk, so opening the status page does not load the templates and limits code.
const PAGES: Record<AdminPageName, React.LazyExoticComponent<() => React.JSX.Element>> = {
    status: lazy(async () => ({ default: (await import('./pages/Status')).StatusPage })),
    pools: lazy(async () => ({ default: (await import('./pages/Pools')).PoolsPage })),
    sessions: lazy(async () => ({ default: (await import('./pages/Sessions')).SessionsPage })),
    workspaces: lazy(async () => ({ default: (await import('./pages/Workspaces')).WorkspacesPage })),
    problems: lazy(async () => ({ default: (await import('./pages/Problems')).ProblemsPage })),
    templates: lazy(async () => ({ default: (await import('./pages/Templates')).TemplatesPage })),
    limits: lazy(async () => ({ default: (await import('./pages/Limits')).LimitsPage }))
};

const NAV_ICONS = {
    status: Activity,
    pools: Layers,
    sessions: Monitor,
    workspaces: Save,
    problems: CircleAlert,
    templates: LayoutGrid,
    limits: Gauge
};

const LANG_LABELS: Record<Lang, string> = { en: 'EN', de: 'DE' };

interface AdminAppProps {
    config: AppConfig;
    api: Api;
    user: UserInfo;
    logoutUrl?: string;
}

type Access = 'checking' | 'granted' | 'denied' | 'failed';

/** The admin panel. The service decides who gets in, this page only reacts to its answer. */
export const AdminApp = ({ config, api, user, logoutUrl }: AdminAppProps): React.JSX.Element => {
    const { lang, setLang } = useI18n();
    const ta = useCallback<AdminContextValue['ta']>((key, params) => adminText(lang, key, params), [lang]);
    const [access, setAccess] = useState<Access>('checking');
    const [attempt, setAttempt] = useState(0);

    useEffect(() => {
        let cancelled = false;
        setAccess('checking');
        api.client
            .GET('/admin/status')
            .then(({ response }) => {
                if (!cancelled) {
                    setAccess(
                        response.status === 200 ? 'granted' : response.status === 403 || response.status === 404 ? 'denied' : 'failed'
                    );
                }
            })
            .catch(() => !cancelled && setAccess('failed'));
        return () => {
            cancelled = true;
        };
    }, [api, attempt]);

    if (access !== 'granted') {
        return (
            <Shell config={config} user={user} logoutUrl={logoutUrl}>
                {access === 'checking' && <p role='status'>{ta('checking')}</p>}
                {access === 'denied' && (
                    <div className='alert danger' role='alert'>
                        <b>{ta('noAccess.title')}</b>
                        {ta('noAccess.text')}
                        <div className='buttons'>
                            <a className='btn' href='/'>
                                {ta('noAccess.back')}
                            </a>
                        </div>
                    </div>
                )}
                {access === 'failed' && (
                    <div className='alert danger' role='alert'>
                        {ta('checkFailed')}
                        <div className='buttons'>
                            <button className='btn' onClick={() => setAttempt(a => a + 1)}>
                                {ta('retry')}
                            </button>
                        </div>
                    </div>
                )}
            </Shell>
        );
    }
    return <Panelled config={config} api={api} user={user} logoutUrl={logoutUrl} lang={lang} setLang={setLang} ta={ta} />;
};

interface PanelledProps extends AdminAppProps {
    lang: Lang;
    setLang: (lang: Lang) => void;
    ta: AdminContextValue['ta'];
}

const Panelled = ({ config, api, user, logoutUrl, lang, setLang, ta }: PanelledProps): React.JSX.Element => {
    const [path, setPath] = useState(window.location.pathname);
    const page = adminPage(path);
    const now = useNow();
    const { data: status, lost } = useWatch<AdminStatus>(api.watchAdminStatus);
    const cfg = usePolling(() => ok(api.client.GET('/admin/config'))).data;
    const templates = usePolling(() => ok(api.client.GET('/admin/templates'))).data;

    useEffect(() => {
        const onPop = (): void => setPath(window.location.pathname);
        window.addEventListener('popstate', onPop);
        return () => window.removeEventListener('popstate', onPop);
    }, []);

    const go = useCallback((to: AdminPageName): void => {
        window.history.pushState({}, '', adminPath(to));
        setPath(adminPath(to));
        window.scrollTo(0, 0);
    }, []);

    const value = useMemo<AdminContextValue>(
        () => ({ api, lang, ta, now, status, config: cfg, templates, go }),
        [api, lang, ta, now, status, cfg, templates, go]
    );

    const counts: Partial<Record<AdminPageName, { n: number; bad?: boolean }>> = {};
    if (status) {
        counts.sessions = { n: status.runningSessions + status.startingSessions };
        if (status.warnings.length > 0) {
            counts.status = { n: status.warnings.length, bad: true };
        }
    }
    if (templates) {
        counts.templates = { n: templates.length };
    }
    const Page = PAGES[page];
    const grafana = httpHref(cfg?.admin.links?.grafana);
    const jaeger = httpHref(cfg?.admin.links?.jaeger);

    return (
        <AdminContext.Provider value={value}>
            <div className='admin a-layout'>
                <nav aria-label={ta('nav.label')} className='a-nav'>
                    <a
                        className='a-brand'
                        href='/admin'
                        onClick={e => {
                            e.preventDefault();
                            go('status');
                        }}
                    >
                        <img src='/assets/logos/theia-logo.png' alt='' />
                        {ta('brand')}
                    </a>
                    {(['status', 'pools', 'sessions', 'workspaces', 'problems'] as const).map(p => (
                        <NavItem key={p} page={p} active={page} count={counts[p]} go={go} ta={ta} />
                    ))}
                    <div className='sep'>{ta('nav.config')}</div>
                    {(['templates', 'limits'] as const).map(p => (
                        <NavItem key={p} page={p} active={page} count={counts[p]} go={go} ta={ta} />
                    ))}
                    {(grafana || jaeger) && (
                        <>
                            <div className='sep'>{ta('nav.elsewhere')}</div>
                            {grafana && (
                                <a href={grafana} target='_blank' rel='noreferrer'>
                                    <ExternalLink aria-hidden />
                                    {ta('nav.grafana')}
                                </a>
                            )}
                            {jaeger && (
                                <a href={jaeger} target='_blank' rel='noreferrer'>
                                    <ExternalLink aria-hidden />
                                    {ta('nav.jaeger')}
                                </a>
                            )}
                        </>
                    )}
                </nav>
                <div className='a-body'>
                    <div className='a-top'>
                        <div className='a-live'>
                            <span className={lost ? 'lost' : undefined}>
                                <span className={`a-dot${lost ? ' lost' : ''}`} />
                                {lost ? ta('liveLost') : ta('live')}
                            </span>
                            <span className='a-ro'>
                                <Eye aria-hidden />
                                {ta('readOnly')}
                            </span>
                        </div>
                        <UserMenu user={user} logoutUrl={logoutUrl} lang={lang} setLang={setLang} ta={ta} />
                    </div>
                    <main className='a-main'>
                        <Suspense fallback={<p role='status'>{ta('loading')}</p>}>
                            <Page key={page} />
                        </Suspense>
                    </main>
                    <Footer footerLinks={config.footerLinks} legal={config.legal} />
                </div>
            </div>
        </AdminContext.Provider>
    );
};

interface NavItemProps {
    page: AdminPageName;
    active: AdminPageName;
    count?: { n: number; bad?: boolean };
    go: (page: AdminPageName) => void;
    ta: AdminContextValue['ta'];
}

const NavItem = ({ page, active, count, go, ta }: NavItemProps): React.JSX.Element => {
    const Icon = NAV_ICONS[page];
    return (
        <a
            href={adminPath(page)}
            aria-current={active === page ? 'page' : undefined}
            className={active === page ? 'on' : undefined}
            onClick={e => {
                if (!e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) {
                    e.preventDefault();
                    go(page);
                }
            }}
        >
            <Icon aria-hidden />
            {ta(`nav.${page}`)}
            {count && <span className={`count${count.bad ? ' bad' : ''}`}>{count.n}</span>}
        </a>
    );
};

interface UserMenuProps {
    user: UserInfo;
    logoutUrl?: string;
    lang: Lang;
    setLang: (lang: Lang) => void;
    ta: AdminContextValue['ta'];
}

const UserMenu = ({ user, logoutUrl, lang, setLang, ta }: UserMenuProps): React.JSX.Element => {
    const [open, setOpen] = useState(false);
    const box = useRef<HTMLDivElement | undefined>(undefined);
    const button = useRef<HTMLButtonElement | undefined>(undefined);

    useEffect(() => {
        if (!open) {
            return;
        }
        const onDown = (e: MouseEvent): void => {
            if (!box.current?.contains(e.target as Node)) {
                setOpen(false);
            }
        };
        const onKey = (e: KeyboardEvent): void => {
            if (e.key === 'Escape') {
                setOpen(false);
                button.current?.focus();
            }
        };
        document.addEventListener('mousedown', onDown);
        document.addEventListener('keydown', onKey);
        return () => {
            document.removeEventListener('mousedown', onDown);
            document.removeEventListener('keydown', onKey);
        };
    }, [open]);

    return (
        <div
            className='a-user'
            ref={el => {
                box.current = el ?? undefined;
            }}
        >
            <button
                className='a-user-btn'
                aria-expanded={open}
                aria-label={ta('user.menu', { label: user.label })}
                ref={el => {
                    button.current = el ?? undefined;
                }}
                onClick={() => setOpen(o => !o)}
            >
                <span className='a-avatar'>{user.initials}</span>
                <span className='uname'>{user.label}</span>
                <ChevronDown aria-hidden />
            </button>
            {open && (
                <div className='a-umenu'>
                    <div className='uhead'>
                        <b>{user.label}</b>
                        <span className='small'>{ta('user.role')}</span>
                    </div>
                    <div className='urow'>
                        <span>{ta('user.language')}</span>
                        <span className='a-seg' role='group' aria-label={ta('user.language')}>
                            {LANGS.map(l => (
                                <button key={l} aria-pressed={l === lang} onClick={() => setLang(l)}>
                                    {LANG_LABELS[l]}
                                </button>
                            ))}
                        </span>
                    </div>
                    <div className='urow small'>{ta('user.theme')}</div>
                    <a className='uitem' href='/'>
                        <House aria-hidden />
                        {ta('user.landing')}
                    </a>
                    {logoutUrl && (
                        <a className='uitem' href={logoutUrl}>
                            <LogOut aria-hidden />
                            {ta('user.signOut')}
                        </a>
                    )}
                </div>
            )}
        </div>
    );
};
