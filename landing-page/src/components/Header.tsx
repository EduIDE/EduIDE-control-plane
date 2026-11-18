import { Check, Languages, LogOut } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';

import { LANGS, useI18n } from '../i18n';
import type { UserInfo } from '../user';

interface HeaderProps {
    title?: string;
    user?: UserInfo;
    logoutUrl?: string;
}

const LANG_NAMES = { en: 'English', de: 'Deutsch' };

export const Header = ({ title = 'EduIDE', user, logoutUrl }: HeaderProps): React.JSX.Element => {
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
                    <img src='/assets/logos/theia-logo.png' alt={t('brand.logoAlt')} />
                    {title}
                </a>
                <div
                    className='account'
                    ref={el => {
                        box.current = el ?? undefined;
                    }}
                >
                    <button
                        className='avatar'
                        ref={el => {
                            avatar.current = el ?? undefined;
                        }}
                        aria-expanded={open}
                        aria-label={user ? t('account', { label: user.label }) : t('language')}
                        title={user?.label}
                        onClick={() => setOpen(o => !o)}
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
                            {logoutUrl && (
                                <a href={logoutUrl}>
                                    <LogOut aria-hidden />
                                    {t('signOut')}
                                </a>
                            )}
                        </div>
                    )}
                </div>
            </div>
        </header>
    );
};
