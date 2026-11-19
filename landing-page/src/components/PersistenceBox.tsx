import { usePersistenceText } from './usePersistenceText';

export const PersistenceBox = ({ saved }: { saved: boolean }): React.JSX.Element => {
    const p = usePersistenceText(saved);
    return (
        <div className='keep'>
            {p.icon}
            <span>
                <b>{p.short}.</b> {p.long}
            </span>
        </div>
    );
};
