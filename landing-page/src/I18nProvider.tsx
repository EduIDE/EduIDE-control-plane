import { useCallback, useEffect, useMemo, useState } from 'react';

import { I18nContext, type Lang, loadLang, saveLang, type Translate, translate } from './i18n';

export const I18nProvider = ({ children, initial }: { children: React.ReactNode; initial?: Lang }): React.JSX.Element => {
    const [lang, setLangState] = useState<Lang>(() => initial ?? loadLang());
    const setLang = useCallback((next: Lang): void => {
        saveLang(next);
        setLangState(next);
    }, []);
    const value = useMemo(() => {
        const t: Translate = (key, params) => translate(lang, key, params);
        return { lang, setLang, t };
    }, [lang, setLang]);
    useEffect(() => {
        document.documentElement.lang = lang;
    }, [lang]);
    return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
};
