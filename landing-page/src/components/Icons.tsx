import { Folder } from 'lucide-react';

export const Logo = ({ url, className = 'logo' }: { url?: string; className?: string }): React.JSX.Element => (
    <div className={className}>{url ? <img src={url} alt='' /> : <Folder aria-hidden />}</div>
);
