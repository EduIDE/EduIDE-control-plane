import { CircleCheck, CircleX, ExternalLink, Loader, Save, SaveOff, TriangleAlert } from 'lucide-react';

import { Logo } from '../components/Icons';
import { httpHref } from '../url';
import { useAdmin } from './context';
import { codeKey } from './messages';
import { type AdminPageName, adminPath } from './routes';

export type Tone = 'ok' | 'warn' | 'bad' | 'n';

/** A link inside the panel: a normal anchor (so deep links and new tabs work) that navigates without a reload. */
export const PageLink = ({ page, children }: { page: AdminPageName; children: React.ReactNode }): React.JSX.Element => {
    const { go } = useAdmin();
    return (
        <a
            href={adminPath(page)}
            onClick={e => {
                if (!e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) {
                    e.preventDefault();
                    go(page);
                }
            }}
        >
            {children}
        </a>
    );
};

export const Panel = ({
    title,
    extra,
    children
}: {
    title: React.ReactNode;
    extra?: React.ReactNode;
    children: React.ReactNode;
}): React.JSX.Element => (
    <section className='a-panel'>
        <h2>
            {title}
            {extra}
        </h2>
        {children}
    </section>
);

export const Scroll = ({ children }: { children: React.ReactNode }): React.JSX.Element => <div className='a-scroll'>{children}</div>;

export const PageHead = ({ title, sub }: { title: string; sub?: string }): React.JSX.Element => (
    <div className='a-pagehead'>
        <h1>{title}</h1>
        {sub && <p>{sub}</p>}
    </div>
);

export const Kpi = ({
    label,
    value,
    unit,
    detail,
    bad,
    good
}: {
    label: string;
    value: React.ReactNode;
    unit?: string;
    detail?: string;
    bad?: boolean;
    good?: boolean;
}): React.JSX.Element => (
    <div className='a-kpi'>
        <div className='l'>{label}</div>
        <div className={`v${bad ? ' bad-t' : ''}`}>
            {value}
            {unit && <small> {unit}</small>}
        </div>
        {detail && <div className={`d${bad ? ' bad' : good ? ' good' : ''}`}>{detail}</div>}
    </div>
);

export const Pill = ({
    tone,
    icon,
    children,
    tip
}: {
    tone: Tone;
    icon?: React.ReactNode;
    children: React.ReactNode;
    tip?: string;
}): React.JSX.Element => (
    <span
        className={`a-pill ${tone}${tip ? ' a-tip' : ''}`}
        tabIndex={tip ? 0 : undefined}
        data-tip={tip}
        aria-label={tip ? `${children}: ${tip}` : undefined}
    >
        {icon}
        {children}
    </span>
);

export const TplCell = ({ name }: { name: string }): React.JSX.Element => {
    const { templates } = useAdmin();
    const t = templates?.find(x => x.name === name);
    return (
        <span className='a-tpl'>
            <Logo name={name} url={t?.logoUrl} />
            {t?.displayName ?? name}
        </span>
    );
};

export const WsCell = ({ saved }: { saved: boolean }): React.JSX.Element => {
    const { ta } = useAdmin();
    return (
        <span className='a-ws'>
            {saved ? <Save aria-hidden /> : <SaveOff aria-hidden />}
            {ta(saved ? 'saved' : 'temp')}
        </span>
    );
};

export const PhasePill = ({ phase }: { phase: 'Pending' | 'Starting' | 'Ready' | 'Failed' }): React.JSX.Element => {
    const { ta } = useAdmin();
    const look = {
        Ready: { tone: 'ok', icon: <CircleCheck aria-hidden /> },
        Starting: { tone: 'n', icon: <Loader aria-hidden /> },
        Pending: { tone: 'n', icon: <Loader aria-hidden /> },
        Failed: { tone: 'bad', icon: <CircleX aria-hidden /> }
    } as const;
    // A phase a newer server adds is shown neutrally instead of crashing the table.
    const fallback = (look as Record<string, { tone: Tone; icon: React.JSX.Element }>)[phase] ?? look.Pending;
    return (
        <Pill tone={fallback.tone} icon={fallback.icon}>
            {ta(`phase.${phase}`)}
        </Pill>
    );
};

const BAD_CODES = new Set(['capacity-exhausted', 'start-failed', 'internal', 'workspace-not-found']);

/** The meaning of an error code is a tooltip on hover or focus, there is no separate legend. */
export const CodePill = ({ code }: { code: string }): React.JSX.Element => {
    const { ta } = useAdmin();
    return (
        <Pill tone={BAD_CODES.has(code) ? 'bad' : 'warn'} tip={codeKey(code) ? ta(codeKey(code)!) : undefined}>
            {code}
        </Pill>
    );
};

export const TraceLink = ({ url, label }: { url?: string; label?: string }): React.JSX.Element | undefined => {
    const { ta } = useAdmin();
    const href = httpHref(url);
    return href ? (
        <a className='a-trace' href={href} target='_blank' rel='noreferrer'>
            {label ?? ta('trace')}
            <ExternalLink aria-hidden />
        </a>
    ) : undefined;
};

export const Kv = ({ rows }: { rows: [React.ReactNode, React.ReactNode][] }): React.JSX.Element => (
    <dl className='a-kv'>
        {rows.map(([k, v], i) => (
            <div key={i} className='a-kv-row'>
                <dt>{k}</dt>
                <dd>{v}</dd>
            </div>
        ))}
    </dl>
);

export const Mono = ({ children }: { children: React.ReactNode }): React.JSX.Element => <span className='mono'>{children}</span>;

/** Shown instead of charts and figures that need Prometheus. Neutral, nothing is broken. */
export const NoPrometheus = (): React.JSX.Element => {
    const { ta, config } = useAdmin();
    const grafana = httpHref(config?.admin.links?.grafana);
    return (
        <p className='a-note'>
            {ta('noProm.text')}{' '}
            {grafana && (
                <a href={grafana} target='_blank' rel='noreferrer'>
                    {ta('noProm.grafana')}
                </a>
            )}
        </p>
    );
};

export const LoadError = (): React.JSX.Element => {
    const { ta } = useAdmin();
    return (
        <div className='a-alert warn' role='alert'>
            <TriangleAlert aria-hidden />
            <div>{ta('loadFailed')}</div>
        </div>
    );
};

export const Empty = ({ children }: { children: React.ReactNode }): React.JSX.Element => <div className='a-empty'>{children}</div>;

/** One hint for every page: a poll failed, the data on screen is older. */
export const UpdateFailed = ({ show }: { show: boolean }): React.JSX.Element | undefined => {
    const { ta } = useAdmin();
    return show ? (
        <p className='a-stale' role='status'>
            <TriangleAlert aria-hidden />
            {ta('updateFailed')}
        </p>
    ) : undefined;
};
