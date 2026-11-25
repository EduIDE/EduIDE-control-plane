import type { AppConfig } from '../config';
import { issueFormUrl } from '../feedbackLinks';
import { useI18n } from '../i18n';
import { legalHref, type LegalRoute } from '../legal';
import { GithubMark } from './Icons';

interface FooterProps {
    footerLinks?: AppConfig['footerLinks'];
    legal: AppConfig['legal'];
}

const GITHUB_URL = 'https://github.com/EduIDE/EduIDE-control-plane';

interface ConfiguredLink {
    text: string;
    url: string;
    target?: string;
    rel?: string;
}

const External = ({
    link,
    children
}: {
    link: { url: string; target?: string; rel?: string };
    children: React.ReactNode;
}): React.JSX.Element => (
    <a href={link.url} target={link.target || '_blank'} rel={link.rel || 'noreferrer'}>
        {children}
    </a>
);

export const Footer = ({ footerLinks, legal }: FooterProps): React.JSX.Element => {
    const { t } = useI18n();
    const version = footerLinks?.attribution?.version;
    const host = window.location.hostname;
    const bug: ConfiguredLink = footerLinks?.bugReport ?? { text: t('footer.bug'), url: issueFormUrl('bug_report.yml', host, version) };
    const feature: ConfiguredLink = footerLinks?.featureRequest ?? {
        text: t('footer.feature'),
        url: issueFormUrl('feature_request.yml', host, version)
    };
    const optional = [feature, footerLinks?.about].filter((l): l is ConfiguredLink => l !== undefined);
    const legalLink = (route: LegalRoute): React.JSX.Element => {
        const { href, external } = legalHref(route, legal[route]);
        return (
            <a href={href} {...(external && { target: '_blank', rel: 'noreferrer' })}>
                {t(route === 'imprint' ? 'footer.imprint' : 'footer.privacy')}
            </a>
        );
    };
    return (
        <footer>
            <div className='bar'>
                <span>{version ? t('footer.version', { version }) : 'EduIDE'}</span>
                <span className='flinks'>
                    <External link={{ url: footerLinks?.github?.url ?? GITHUB_URL }}>
                        <span className='gh'>
                            <GithubMark size={15} />
                            {t('footer.github')}
                        </span>
                    </External>
                    <External link={bug}>{bug.text}</External>
                    {optional.map(l => (
                        <External key={l.url} link={l}>
                            {l.text}
                        </External>
                    ))}
                    <a href='/status'>{t('footer.status')}</a>
                    {legalLink('imprint')}
                    {legalLink('privacy')}
                </span>
            </div>
        </footer>
    );
};
