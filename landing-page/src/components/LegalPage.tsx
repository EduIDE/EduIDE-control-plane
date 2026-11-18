import { useEffect, useState } from 'react';

import type { LegalPage as LegalConfig } from '../config';
import { useI18n } from '../i18n';
import { type LegalRoute, renderMarkdown } from '../legal';

/** Shows a hosted Markdown page, or sends the browser on to the operator's own page. Needs no login. */
export const LegalPage = ({ route, page }: { route: LegalRoute; page: LegalConfig }): React.JSX.Element => {
    const { t } = useI18n();
    const [html, setHtml] = useState<string>();
    const [failed, setFailed] = useState(false);
    const path = 'path' in page ? page.path : undefined;
    const url = 'url' in page ? page.url : undefined;

    useEffect(() => {
        if (url) {
            window.location.replace(url);
            return;
        }
        const controller = new AbortController();
        fetch(path ?? '', { signal: controller.signal })
            .then(async res => {
                if (!res.ok) {
                    throw new Error(`HTTP ${res.status}`);
                }
                setHtml(await renderMarkdown(await res.text()));
            })
            .catch(() => !controller.signal.aborted && setFailed(true));
        return () => controller.abort();
    }, [path, url]);

    return (
        <div className='legal'>
            <h1>{t(route === 'imprint' ? 'legal.imprint' : 'legal.privacy')}</h1>
            {html !== undefined ? (
                <div className='prose' dangerouslySetInnerHTML={{ __html: html }} />
            ) : (
                <p role='status'>{failed ? t('legal.failed') : t('legal.loading')}</p>
            )}
            <p>
                <a className='link' href='/'>
                    {t('legal.home')}
                </a>
            </p>
        </div>
    );
};
