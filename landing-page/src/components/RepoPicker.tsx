import { ArrowLeft } from 'lucide-react';

import type { Template } from '../api-client';
import { useI18n } from '../i18n';
import { TemplateTile } from './TemplateTile';

interface RepoPickerProps {
    /** The repository as text, without credentials. */
    repository: string;
    /** Name from the link that matches no environment, if the link had one. */
    unknownName?: string;
    templates: Template[];
    onPick: (template: Template) => void;
    onBack: () => void;
}

/** A link with a repository but no usable environment: the user picks one that takes a repository. */
export const RepoPicker = ({ repository, unknownName, templates, onPick, onBack }: RepoPickerProps): React.JSX.Element => {
    const { t } = useI18n();
    return (
        <div className='launch wide' data-testid='repo-picker'>
            <h1>{t('git.pick.title', { repo: repository })}</h1>
            {unknownName && (
                <div className='alert danger' role='alert'>
                    {t('git.pick.unknown', { name: unknownName })}
                </div>
            )}
            {templates.length === 0 ? (
                <div className='alert danger' role='alert'>
                    {t('git.pick.none')}
                </div>
            ) : (
                <>
                    <p className='lead'>{t('git.pick.text')}</p>
                    <div className='grid'>
                        {templates.map(template => (
                            // The info dialog is the home page's: the tile here only starts.
                            <TemplateTile
                                key={template.name}
                                template={{ ...template, includes: undefined }}
                                onPick={() => onPick(template)}
                                onInfo={() => undefined}
                            />
                        ))}
                    </div>
                </>
            )}
            <div className='buttons'>
                <button className='btn' onClick={onBack}>
                    <ArrowLeft aria-hidden />
                    {t('back')}
                </button>
            </div>
        </div>
    );
};
