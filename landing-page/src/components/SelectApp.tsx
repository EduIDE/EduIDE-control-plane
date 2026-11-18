import React from 'react';

import type { Template } from '../api-client';
import { PersistenceBadge } from './PersistenceBadge';

interface SelectAppProps {
    templates: Template[];
    onSelect: (template: Template) => void;
}

export const SelectApp: React.FC<SelectAppProps> = ({ templates, onSelect }: SelectAppProps) => (
    <div className='App__grid'>
        {templates.map(t => (
            <button key={t.name} className='App__grid-item' onClick={() => onSelect(t)} data-testid={`launch-app-${t.name}`}>
                {t.logoUrl && <img src={t.logoUrl} alt={`${t.displayName} logo`} className='App__grid-item-logo' />}
                <div className='App__grid-item-launch'>Select</div>
                <div className='App__grid-item-text'>{t.displayName}</div>
                <PersistenceBadge ephemeral={t.defaultPersistence === 'none'} tooltipId={`persistence-${t.name}`} />
            </button>
        ))}
    </div>
);
