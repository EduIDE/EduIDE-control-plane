import { ArrowLeft, Check, Languages, LogOut, ShieldCheck } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';

import { LANGS, useI18n } from '../i18n';
import { BRAND_LOGO } from '../logos';
import type { UserInfo } from '../user';

interface HeaderProps {
    title?: string;
    user?: UserInfo;
    logoutUrl?: string;
    /** Show the entry to the admin panel (only when the service granted access). */
    admin?: boolean;
    /** A link back to the landing page, for pages that are reachable without being on it. */
    back?: boolean;
    /** Called each time the menu opens, so a page can look up the admin entry lazily. */
    onMenuOpen?: () => void;
}

const LANG_NAMES = { en: 'English', de: 'Deutsch' };

export const Header = ({ title = 'EduIDE', user, logoutUrl, admin, back, onMenuOpen }: HeaderProps): React.JSX.Element => {
    const { t, lang, setLang } = useI18n();
    const [open, setOpen] = useState(false);
    const box = useRef<HTMLDivElement | undefined>(undefined);
    const avatar = useRef<HTMLButtonElement | undefined>(undefined);

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
                avatar.current?.focus();
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
        <header>
            <div className='bar'>
                <a className='brand' href='/' style={{ color: 'inherit', textDecoration: 'none' }}>
                    <img src={BRAND_LOGO} alt={t('brand.logoAlt')} />
                    {title}
                </a>
                <div className='bar-end'>
                    {back && (
                        <a className='link' href='/'>
                            <ArrowLeft aria-hidden />
                            {t('legal.home')}
                        </a>
                    )}
                    <div
                        className='account'
                        ref={el => {
                            box.current = el ?? undefined;
                        }}
                    >
                        <button
                            className='avatar'
                            data-testid='account-button'
                            ref={el => {
                                avatar.current = el ?? undefined;
                            }}
                            aria-expanded={open}
                            aria-label={user ? t('account', { label: user.label }) : t('language')}
                            title={user?.label}
                            onClick={() => {
                                if (!open) {
                                    onMenuOpen?.();
                                }
                                setOpen(o => !o);
                            }}
                        >
                            {user ? user.initials : <Languages aria-hidden />}
                        </button>
                        {open && (
                            <div className='menu'>
                                <div className='menu-label'>{t('language')}</div>
                                {LANGS.map(l => (
                                    <button
                                        key={l}
                                        aria-pressed={l === lang}
                                        onClick={() => {
                                            setLang(l);
                                            setOpen(false);
                                        }}
                                    >
                                        {LANG_NAMES[l]}
                                        {l === lang && <Check className='check' aria-hidden />}
                                    </button>
                                ))}
                                {admin && (
                                    <a href='/admin'>
                                        <ShieldCheck aria-hidden />
                                        {t('menu.admin')}
                                    </a>
                                )}
                                {logoutUrl && (
                                    <a href={logoutUrl} data-testid='logoutButton'>
                                        <LogOut aria-hidden />
                                        {t('signOut')}
                                    </a>
                                )}
                            </div>
                        )}
                    </div>
                </div>
            </div>
        </header>
    );
};
