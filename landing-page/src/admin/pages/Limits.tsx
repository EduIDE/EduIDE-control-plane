// The Kv rows are static pairs: Kv keys them by position.
/* eslint-disable react/jsx-key */
import { useAdmin } from '../context';
import { formatBytes } from '../format';
import { Kv, Mono, PageHead, Panel } from '../ui';
import { useDuration } from '../useDuration';

export const LimitsPage = (): React.JSX.Element => {
    const { ta, config: c } = useAdmin();
    const fd = useDuration();
    if (!c) {
        return (
            <>
                <PageHead title={ta('limits.title')} sub={ta('limits.sub')} />
                <p role='status'>{ta('loading')}</p>
            </>
        );
    }
    const none = <span className='small'>{ta('none')}</span>;
    return (
        <>
            <PageHead title={ta('limits.title')} sub={ta('limits.sub')} />
            <div className='a-grid2'>
                <Panel title={ta('limits.sessions')}>
                    <Kv
                        rows={[
                            [ta('limits.perUser'), c.sessionsPerUser],
                            [ta('limits.launchRate'), ta('limits.perMinute', { n: c.rateLimit.launchesPerMinute })],
                            [ta('limits.adminRate'), ta('limits.perMinuteOnly', { n: c.rateLimit.adminPerMinute })],
                            [ta('limits.dataSize'), formatBytes(c.launchDataMaxBytes)],
                            [ta('limits.hosts'), c.allowedHosts.length > 0 ? <Mono>{c.allowedHosts.join(', ')}</Mono> : none],
                            [ta('limits.capacityWait'), fd(c.capacity.exhaustedAfterSeconds)],
                            [ta('limits.titleStrip'), c.titleStripPattern ? <Mono>{c.titleStripPattern}</Mono> : none]
                        ]}
                    />
                </Panel>
                <Panel title={ta('limits.workspaces')}>
                    <Kv
                        rows={[
                            [ta('limits.retention'), fd(c.workspaces.retentionSeconds)],
                            [ta('limits.grace'), fd(c.workspaces.graceSeconds)],
                            [ta('limits.size'), formatBytes(c.workspaces.defaultSizeBytes)],
                            [ta('limits.storageClass'), c.workspaces.storageClassName ? <Mono>{c.workspaces.storageClassName}</Mono> : none]
                        ]}
                    />
                </Panel>
                <Panel title={ta('limits.signin')}>
                    <Kv
                        rows={[
                            [ta('limits.issuer'), <Mono>{c.oidc.issuerURL}</Mono>],
                            [ta('limits.clients'), <Mono>{c.oidc.clientIDs.join(', ')}</Mono>],
                            [ta('limits.userClaim'), <Mono>{c.oidc.userClaim}</Mono>],
                            [ta('limits.adminRole'), <Mono>{`${c.oidc.adminRolePath}: ${c.oidc.adminRole}`}</Mono>],
                            [ta('limits.otlp'), c.telemetry.otlpEndpoint ? <Mono>{c.telemetry.otlpEndpoint}</Mono> : none],
                            [ta('limits.hashed'), c.telemetry.userIdsHashed ? ta('limits.hashedValue') : none],
                            [
                                ta('limits.prometheus'),
                                c.admin.prometheusConfigured ? (
                                    <>{c.admin.prometheusURL ? <Mono>{c.admin.prometheusURL}</Mono> : ta('limits.promOn')}</>
                                ) : (
                                    <span className='small'>{ta('limits.promOff')}</span>
                                )
                            ]
                        ]}
                    />
                </Panel>
                <Panel title={ta('limits.deployment')}>
                    <Kv
                        rows={[
                            [ta('limits.name'), <Mono>{c.deployment}</Mono>],
                            [ta('limits.namespace'), <Mono>{c.namespace}</Mono>],
                            [ta('limits.hostsTitle'), <Mono>{[c.hosts.service, c.hosts.instance, c.hosts.webview].join(', ')}</Mono>]
                        ]}
                    />
                </Panel>
            </div>
            <p className='a-note'>{ta('limits.source')}</p>
        </>
    );
};
