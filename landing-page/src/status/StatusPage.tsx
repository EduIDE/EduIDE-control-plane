import './status.css';

import { CircleCheck, CircleX, Clock, LogIn, Save, SaveOff, TriangleAlert } from 'lucide-react';

import type { PublicStatus, PublicTemplate, ServiceLevel } from '../apiTypes';
import { Logo } from '../components/Icons';
import { Shell } from '../components/Shell';
import { useNow } from '../components/useNow';
import { usePolling } from '../components/usePolling';
import type { AppConfig } from '../config';
import { formatAgo } from '../format';
import { useI18n } from '../i18n';
import { hero, levelTone, wait } from './level';
import { type StatusKey, statusText } from './messages';

const LEVEL_ICONS = { ok: CircleCheck, warn: TriangleAlert, danger: CircleX };

/** The public status is cached for 15 s by the service, so asking every 30 s never hits an old answer twice. */
async function loadStatus(baseUrl: string): Promise<PublicStatus> {
    const res = await fetch(`${baseUrl}/status`);
    if (!res.ok) {
        throw new Error(`status: HTTP ${res.status}`);
    }
    return (await res.json()) as PublicStatus;
}

const Parts = ({
    status,
    st
}: {
    status: PublicStatus;
    st: (key: StatusKey, params?: Record<string, string | number>) => string;
}): React.JSX.Element => {
    const rows: { key: keyof PublicStatus['parts']; icon: React.JSX.Element; title: StatusKey; text: StatusKey }[] = [
        { key: 'signIn', icon: <LogIn aria-hidden />, title: 'parts.signIn', text: 'parts.signInText' },
        { key: 'starting', icon: <Clock aria-hidden />, title: 'parts.starting', text: 'parts.startingText' },
        { key: 'workspaces', icon: <Save aria-hidden />, title: 'parts.workspaces', text: 'parts.workspacesText' }
    ];
    return (
        <section aria-labelledby='parts'>
            <h2 id='parts'>{st('parts.title')}</h2>
            <div className='list'>
                {rows.map(r => {
                    const level: ServiceLevel = status.parts[r.key];
                    const tone = levelTone(level);
                    const Icon = LEVEL_ICONS[tone];
                    return (
                        <div key={r.key} className='row'>
                            <span className='icon-box'>{r.icon}</span>
                            <div className='meta'>
                                <div className='title'>{st(r.title)}</div>
                                <div className='sub'>{st(r.text)}</div>
                            </div>
                            <span className={`status-tag ${tone}`}>
                                <Icon aria-hidden />
                                {st(`level.${level}`)}
                            </span>
                        </div>
                    );
                })}
            </div>
        </section>
    );
};

const WaitLine = ({ saved, seconds, st }: { saved: boolean; seconds: number; st: ReturnType<typeof useSt> }): React.JSX.Element => {
    const w = wait(seconds);
    const text = w.kind === 'instant' ? st('waits.instant') : st(w.kind === 'seconds' ? 'waits.seconds' : 'waits.minutes', { n: w.n });
    return (
        <span className='wait-line'>
            <span className='k'>
                {saved ? <Save aria-hidden /> : <SaveOff aria-hidden />}
                {st(saved ? 'waits.saved' : 'waits.temporary')}
            </span>
            <b className={w.kind === 'minutes' ? 'slow' : undefined}>{text}</b>
        </span>
    );
};

function useSt(): (key: StatusKey, params?: Record<string, string | number>) => string {
    const { lang } = useI18n();
    return (key, params) => statusText(lang, key, params);
}

const TemplateRow = ({ template, st }: { template: PublicTemplate; st: ReturnType<typeof useSt> }): React.JSX.Element => {
    const { none, volume } = template.startEstimate;
    return (
        <div className='row'>
            <Logo url={template.logoUrl} />
            <div className='meta'>
                <div className='title'>{template.displayName}</div>
                {/* The API says only whether one is ready right now, never how many. */}
                <div className='pool-line'>
                    {template.warm ? (
                        <>
                            <span className='ok-dot' />
                            {st('waits.ready')}
                        </>
                    ) : (
                        st('waits.prepared')
                    )}
                </div>
            </div>
            <div className='waits'>
                {none && <WaitLine saved={false} seconds={none.seconds} st={st} />}
                {volume && <WaitLine saved seconds={volume.seconds} st={st} />}
            </div>
        </div>
    );
};

export const StatusPage = ({ config }: { config: AppConfig }): React.JSX.Element => {
    const { lang } = useI18n();
    const st = useSt();
    const now = useNow();
    const { data, failed, loading } = usePolling(() => loadStatus(config.apiBaseUrl));

    let body: React.JSX.Element;
    if (failed || (!data && !loading)) {
        body = (
            <div className='hero danger' role='status'>
                <CircleX aria-hidden />
                <div>
                    <h1>{st('unavailable')}</h1>
                    <p>{st('unavailableText')}</p>
                </div>
            </div>
        );
    } else if (!data) {
        body = <p role='status'>{st('loading')}</p>;
    } else {
        const h = hero(data);
        const Icon = LEVEL_ICONS[h.tone];
        const withTemplates = data.templates.filter(t => t.startEstimate.none || t.startEstimate.volume);
        body = (
            <>
                <div className={`hero ${h.tone}`} role='status'>
                    <Icon aria-hidden />
                    <div>
                        <h1>{st(h.title)}</h1>
                        <p>{st(h.text)}</p>
                    </div>
                </div>
                <Parts status={data} st={st} />
                {withTemplates.length > 0 && (
                    <section aria-labelledby='waits'>
                        <h2 id='waits'>{st('waits.title')}</h2>
                        <div className='list'>
                            {withTemplates.map(t => (
                                <TemplateRow key={t.name} template={t} st={st} />
                            ))}
                        </div>
                        <p className='note'>{st('waits.note')}</p>
                    </section>
                )}
                <p className='note' style={{ marginTop: 24 }}>
                    {st('updated', { time: formatAgo(data.updatedAt, now, lang) })}
                </p>
            </>
        );
    }
    return (
        <Shell config={config} back>
            <div className='status'>{body}</div>
        </Shell>
    );
};
