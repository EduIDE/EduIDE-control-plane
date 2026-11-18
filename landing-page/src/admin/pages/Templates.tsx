// The Kv rows are static pairs: Kv keys them by position.
/* eslint-disable react/jsx-key */
import { Logo } from '../../components/Icons';
import { useAdmin } from '../context';
import { Empty, Kv, Mono, PageHead, PageLink, Panel, WsCell } from '../ui';
import { useDuration } from '../useDuration';

export const TemplatesPage = (): React.JSX.Element => {
    const { ta, templates } = useAdmin();
    const fd = useDuration();
    return (
        <>
            <PageHead title={ta('templates.title')} sub={ta('templates.sub')} />
            {!templates ? (
                <p role='status'>{ta('loading')}</p>
            ) : templates.length === 0 ? (
                <Empty>{ta('none')}</Empty>
            ) : (
                <div className='a-grid2'>
                    {templates.map(t => {
                        const est = t.startEstimate;
                        return (
                            <Panel
                                key={t.name}
                                title={
                                    <span className='a-tpl big'>
                                        <Logo url={t.logoUrl} />
                                        <span>
                                            <b>{t.displayName}</b>
                                            <Mono>{t.name}</Mono>
                                        </span>
                                    </span>
                                }
                            >
                                <Kv
                                    rows={[
                                        [
                                            ta('templates.image'),
                                            <>
                                                <Mono>{t.imageVersion ?? t.image}</Mono>{' '}
                                                {t.imageDigest && <span className='small'>{t.imageDigest.slice(0, 19)}</span>}
                                            </>
                                        ],
                                        [
                                            ta('templates.modes'),
                                            <div className='a-chips'>
                                                {t.persistence.map(m => (
                                                    <span key={m} className='a-chip'>
                                                        <WsCell saved={m === 'volume'} />
                                                    </span>
                                                ))}
                                            </div>
                                        ],
                                        [
                                            ta('templates.variants'),
                                            t.variants && t.variants.length > 0 ? (
                                                t.variants.map(v => v.name).join(', ')
                                            ) : (
                                                <span className='small'>{ta('templates.none')}</span>
                                            )
                                        ],
                                        [
                                            ta('templates.estimate'),
                                            est && (est.none || est.volume) ? (
                                                <>
                                                    {est.none && (
                                                        <div>
                                                            {ta('temp')}: {est.none.seconds.toFixed(1)} {ta('unit.s')}{' '}
                                                            <span className='small'>
                                                                {ta(
                                                                    est.none.strategy === 'warm'
                                                                        ? 'templates.estimateWarm'
                                                                        : 'templates.estimateCold'
                                                                )}
                                                            </span>
                                                        </div>
                                                    )}
                                                    {est.volume && (
                                                        <div>
                                                            {ta('saved')}: {est.volume.seconds.toFixed(1)} {ta('unit.s')}{' '}
                                                            <span className='small'>{ta('templates.estimateCold')}</span>
                                                        </div>
                                                    )}
                                                </>
                                            ) : (
                                                <span className='small'>{ta('templates.none')}</span>
                                            )
                                        ],
                                        [
                                            ta('templates.timeouts'),
                                            ta('templates.timeoutsValue', {
                                                idle: fd(t.timeouts.idleSeconds),
                                                warning: fd(t.timeouts.warningSeconds),
                                                max: fd(t.timeouts.maxSeconds)
                                            })
                                        ],
                                        [
                                            ta('templates.retention'),
                                            t.retentionSeconds !== undefined ? (
                                                ta('templates.retentionOverride', { n: fd(t.retentionSeconds) })
                                            ) : (
                                                <span className='small'>{ta('templates.retentionDefault')}</span>
                                            )
                                        ],
                                        [
                                            ta('templates.keys'),
                                            <span className='mono'>
                                                {t.injectableKeys
                                                    .map(k => k.name + (k.required ? ` (${ta('templates.keyRequired')})` : ''))
                                                    .join(', ')}
                                            </span>
                                        ],
                                        [
                                            ta('templates.pool'),
                                            t.pool ? (
                                                <PageLink page='pools'>{t.pool.name}</PageLink>
                                            ) : (
                                                <span className='small'>{ta('templates.noPool')}</span>
                                            )
                                        ]
                                    ]}
                                />
                                {(t.includes ?? []).length > 0 && (
                                    <div className='a-inc'>
                                        {t.includes?.map(g => (
                                            <div key={g.group}>
                                                <div className='h'>{g.group}</div>
                                                <div className='a-chips'>
                                                    {g.items.map(i => (
                                                        <span key={i.name} className='a-chip'>
                                                            {i.version ? `${i.name} ${i.version}` : i.name}
                                                        </span>
                                                    ))}
                                                </div>
                                            </div>
                                        ))}
                                    </div>
                                )}
                            </Panel>
                        );
                    })}
                </div>
            )}
        </>
    );
};
