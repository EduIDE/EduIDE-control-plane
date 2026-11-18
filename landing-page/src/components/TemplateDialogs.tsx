import { Folder, Info, Play, X } from 'lucide-react';
import { useState } from 'react';

import type { Template } from '../api-client';
import { useI18n } from '../i18n';
import { isSavedByDefault } from '../templates';
import { Dialog } from './Dialog';
import { Logo } from './Icons';
import { PersistenceBox } from './PersistenceBox';

interface InfoDialogProps {
    template: Template;
    onClose: () => void;
    onStart: (template: Template) => void;
}

export const InfoDialog = ({ template, onClose, onStart }: InfoDialogProps): React.JSX.Element => {
    const { t } = useI18n();
    const variants = template.variants ?? [];
    return (
        <Dialog label={t('info.title')} onClose={onClose}>
            <div className='dlg-head'>
                <Logo url={template.logoUrl} />
                <div>
                    <div className='title'>{template.displayName}</div>
                    <div className='sub'>{t('info.title')}</div>
                </div>
                <button className='icon-btn' aria-label={t('close')} onClick={onClose}>
                    <X aria-hidden />
                </button>
            </div>
            <div className='dlg-body'>
                <PersistenceBox saved={isSavedByDefault(template)} />
                {(template.includes ?? []).map(g => (
                    <div key={g.group}>
                        <div className='inc-h'>{g.group}</div>
                        <div className='inc-items'>
                            {g.items.map(i => (
                                <span key={i.name} className='chip'>
                                    {i.version ? `${i.name} ${i.version}` : i.name}
                                </span>
                            ))}
                        </div>
                    </div>
                ))}
                {variants.length > 0 && (
                    <div>
                        <div className='inc-h'>{t('info.variants')}</div>
                        <div className='inc-items'>
                            {variants.map(v => (
                                <span key={v.id} className='chip'>
                                    {v.name}
                                </span>
                            ))}
                        </div>
                    </div>
                )}
            </div>
            <div className='dlg-foot'>
                <span className='sub'>{template.imageVersion}</span>
                <button className='btn primary' data-autofocus onClick={() => onStart(template)}>
                    <Play aria-hidden />
                    {t('start')}
                </button>
            </div>
        </Dialog>
    );
};

interface PickDialogProps {
    template: Template;
    onClose: () => void;
    onInfo: (template: Template) => void;
    /** Empty id: no project template. */
    onStart: (template: Template, variantId: string) => void;
}

export const PickDialog = ({ template, onClose, onInfo, onStart }: PickDialogProps): React.JSX.Element => {
    const { t } = useI18n();
    const options = [
        ...(template.variants ?? []).map(v => ({ id: v.id, name: v.name, description: v.description, logoUrl: v.logoUrl })),
        { id: '', name: t('pick.empty'), description: t('pick.emptyDesc'), logoUrl: undefined }
    ];
    const [chosen, setChosen] = useState(options[0].id);
    const hasIncludes = (template.includes ?? []).length > 0;
    return (
        <Dialog label={t('pick.title', { name: template.displayName })} onClose={onClose}>
            <div className='dlg-head'>
                <Logo url={template.logoUrl} />
                <div>
                    <div className='title'>{t('pick.title', { name: template.displayName })}</div>
                    <div className='sub'>{t('pick.subtitle')}</div>
                </div>
                <button className='icon-btn' aria-label={t('close')} onClick={onClose}>
                    <X aria-hidden />
                </button>
            </div>
            <fieldset className='dlg-body opts'>
                <legend className='sr-only'>{t('pick.legend')}</legend>
                {options.map((o, i) => (
                    <label key={o.id} className='opt'>
                        <input
                            type='radio'
                            name='variant'
                            value={o.id}
                            checked={chosen === o.id}
                            data-autofocus={i === 0 ? true : undefined}
                            onChange={() => setChosen(o.id)}
                        />
                        <span className='opt-logo'>{o.logoUrl ? <img src={o.logoUrl} alt='' /> : <Folder aria-hidden />}</span>
                        <span>
                            <span className='opt-t'>{o.name}</span>
                            {o.description && <span className='sub'>{o.description}</span>}
                        </span>
                    </label>
                ))}
                <PersistenceBox saved={isSavedByDefault(template)} />
            </fieldset>
            <div className='dlg-foot'>
                {hasIncludes && (
                    <button className='link' onClick={() => onInfo(template)}>
                        <Info aria-hidden />
                        {t('info.whatsIncluded')}
                    </button>
                )}
                <span className='actions'>
                    <button className='btn' onClick={onClose}>
                        {t('cancel')}
                    </button>
                    <button className='btn primary' onClick={() => onStart(template, chosen)}>
                        <Play aria-hidden />
                        {t('start')}
                    </button>
                </span>
            </div>
        </Dialog>
    );
};
