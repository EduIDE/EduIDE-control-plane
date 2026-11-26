import { useEffect, useRef, useState } from 'react';

import type { Api } from '../api';
import type { LaunchRequest, Template } from '../api-client';
import { useI18n } from '../i18n';
import { buildTileRequest } from '../launch';
import { type Row, toRows } from '../overview';
import { EnvironmentRows } from './EnvironmentRows';
import { InfoDialog, PickDialog } from './TemplateDialogs';
import { TemplateTile } from './TemplateTile';
import { useNow } from './useNow';
import { useOverview } from './useOverview';

interface HomeProps {
    api: Api;
    templates?: Template[];
    error?: string;
    firstName?: string;
    onLaunch: (request: LaunchRequest, template: Template) => void;
    onStartRow: (row: Row) => void;
    /** The overview stream came back after a drop: a good moment to ask for the templates again. */
    onReconnect?: () => void;
}

type Dialogs = { kind: 'info' | 'pick'; template: Template } | undefined;

/** Your sessions on top, new ones below. Both `/` and `/me/overview` show this page. */
export const Home = ({ api, templates, error, firstName, onLaunch, onStartRow, onReconnect }: HomeProps): React.JSX.Element => {
    const { t } = useI18n();
    const { overview, lost, recovered } = useOverview(api);
    const reconnect = useRef(onReconnect);
    reconnect.current = onReconnect;
    useEffect(() => {
        if (recovered > 0) {
            reconnect.current?.();
        }
    }, [recovered]);
    const [dialog, setDialog] = useState<Dialogs>();
    const now = useNow();
    const rows = overview ? toRows(overview, now) : undefined;
    const returning = (rows?.length ?? 0) > 0;
    const heading = firstName
        ? t(returning ? 'greeting.back' : 'greeting.new', { name: firstName })
        : t(returning ? 'greeting.backAnon' : 'greeting.newAnon');

    const start = (template: Template, variantId?: string): void => {
        setDialog(undefined);
        onLaunch(buildTileRequest(template, variantId), template);
    };
    const pick = (template: Template): void =>
        template.variants && template.variants.length > 0 && template.injectableKeys.includes('TEMPLATE')
            ? setDialog({ kind: 'pick', template })
            : start(template);

    const listed = templates?.filter(template => template.listed);
    return (
        <>
            <h1>{heading}</h1>
            <p className='lead'>{t(returning ? 'lead.back' : 'lead.new')}</p>
            {(error || lost) && (
                <div className='alert danger' role='alert'>
                    {error ?? t('connectionLost')}
                </div>
            )}
            <section aria-labelledby='mine'>
                <h2 id='mine'>
                    {t('section.mine')} {returning && <small>{rows?.length}</small>}
                </h2>
                {!rows ? (
                    // The alert above already says what is wrong, so the loading text would contradict it.
                    !error &&
                    !lost && (
                        <p role='status' className='sub'>
                            {t('loading.mine')}
                        </p>
                    )
                ) : returning ? (
                    <EnvironmentRows rows={rows} api={api} onStart={onStartRow} now={now} />
                ) : (
                    <div className='empty'>{t('empty.mine')}</div>
                )}
            </section>
            {/* Unlisted templates still launch by link and show in the sessions above. */}
            {(!listed || listed.length > 0) && (
                <section aria-labelledby='new'>
                    <h2 id='new'>{t('section.new')}</h2>
                    <div className='grid'>
                        {listed?.map(template => (
                            <TemplateTile
                                key={template.name}
                                template={template}
                                onPick={pick}
                                onInfo={tpl => setDialog({ kind: 'info', template: tpl })}
                            />
                        ))}
                    </div>
                </section>
            )}
            {dialog?.kind === 'info' && <InfoDialog template={dialog.template} onClose={() => setDialog(undefined)} onStart={pick} />}
            {dialog?.kind === 'pick' && (
                <PickDialog
                    template={dialog.template}
                    onClose={() => setDialog(undefined)}
                    onInfo={template => setDialog({ kind: 'info', template })}
                    onStart={(template, id) => start(template, id)}
                />
            )}
        </>
    );
};
