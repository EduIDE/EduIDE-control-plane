import { Info } from 'lucide-react';

import type { Template } from '../api-client';
import { useI18n } from '../i18n';
import { estimateFor, isSavedByDefault, startLine } from '../templates';
import { Logo } from './Icons';
import { usePersistenceText } from './usePersistenceText';

interface TemplateTileProps {
    template: Template;
    onPick: (template: Template) => void;
    onInfo: (template: Template) => void;
}

export const TemplateTile = ({ template, onPick, onInfo }: TemplateTileProps): React.JSX.Element => {
    const { t } = useI18n();
    const saved = isSavedByDefault(template);
    const p = usePersistenceText(saved);
    const line = startLine(estimateFor(template, template.defaultPersistence));
    return (
        <div className='card'>
            <button className='card-main' aria-label={t('tile.start', { name: template.displayName })} onClick={() => onPick(template)} />
            {template.includes && template.includes.length > 0 && (
                <button
                    className='info-btn'
                    aria-label={t('tile.includes', { name: template.displayName })}
                    title={t('info.whatsIncluded')}
                    onClick={() => onInfo(template)}
                >
                    <Info aria-hidden />
                </button>
            )}
            <Logo url={template.logoUrl} />
            <div>
                <div className='name'>{template.displayName}</div>
                {line && (
                    <span className={`speed${line.kind === 'instant' ? ' fast' : ''}`}>
                        {line.kind === 'instant' ? t('ready.instantly') : t('ready.about', { n: line.seconds })}
                    </span>
                )}
            </div>
            <div className='tags'>
                <span className='tag tip' tabIndex={0} data-tip={p.long} aria-label={`${p.short}. ${p.long}`}>
                    {p.icon}
                    {p.short}
                </span>
            </div>
        </div>
    );
};
