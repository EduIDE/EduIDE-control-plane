import { useEffect, useRef } from 'react';

interface DialogProps {
    label: string;
    onClose: () => void;
    children: React.ReactNode;
}

const FOCUSABLE = 'button:not(:disabled), input:not(:disabled), a[href]';

/** Modal dialog: focus moves in on open (to `data-autofocus` or the first control), Escape closes, focus returns on close. */
export const Dialog = ({ label, onClose, children }: DialogProps): React.JSX.Element => {
    const ref = useRef<HTMLDivElement | undefined>(undefined);

    useEffect(() => {
        const previous = document.activeElement as HTMLElement | undefined;
        const box = ref.current;
        (box?.querySelector<HTMLElement>('[data-autofocus]') ?? box?.querySelector<HTMLElement>(FOCUSABLE))?.focus();
        return () => previous?.focus();
    }, []);

    const onKeyDown = (e: React.KeyboardEvent): void => {
        if (e.key === 'Escape') {
            e.stopPropagation();
            onClose();
        } else if (e.key === 'Tab') {
            const items = Array.from(ref.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? []);
            const first = items[0];
            const last = items[items.length - 1];
            if (e.shiftKey && document.activeElement === first) {
                e.preventDefault();
                last?.focus();
            } else if (!e.shiftKey && document.activeElement === last) {
                e.preventDefault();
                first?.focus();
            }
        }
    };

    return (
        <div className='backdrop' onMouseDown={e => e.target === e.currentTarget && onClose()} onKeyDown={onKeyDown}>
            <div
                className='dialog'
                role='dialog'
                aria-modal='true'
                aria-label={label}
                ref={el => {
                    ref.current = el ?? undefined;
                }}
            >
                {children}
            </div>
        </div>
    );
};
