import type { AppConfig } from '../config';
import type { UserInfo } from '../user';
import { Footer } from './Footer';
import { Header } from './Header';

interface ShellProps {
    config: AppConfig;
    user?: UserInfo;
    logoutUrl?: string;
    children: React.ReactNode;
}

/** Header, content and footer of every page. The footer stays at the bottom of the viewport. */
export const Shell = ({ config, user, logoutUrl, children }: ShellProps): React.JSX.Element => (
    <div className='app'>
        <Header title={config.title} user={user} logoutUrl={logoutUrl} />
        <main>{children}</main>
        <Footer footerLinks={config.footerLinks} legal={config.legal} />
    </div>
);
