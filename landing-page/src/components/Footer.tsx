import type { AppConfig, FooterLinksConfig } from '../config';

interface FooterProps {
    selectedAppDefinition: string;
    footerLinks?: FooterLinksConfig;
    legal?: AppConfig['legal'];
}

const ISSUES_URL = 'https://github.com/EduIDE/EduIDE-control-plane/issues';

interface ConfiguredLinkProps {
    link?: { text: string; url: string; target?: string; rel?: string };
    fallback: { text: string; url: string };
    icon?: string;
    className?: string;
}

const ConfiguredLink = ({ link, fallback, icon = '', className }: ConfiguredLinkProps): React.JSX.Element => (
    <a href={(link ?? fallback).url} target={link?.target || '_blank'} rel={link?.rel || 'noreferrer'} className={className}>
        {icon}
        {(link ?? fallback).text}
    </a>
);

export const Footer = ({ selectedAppDefinition, footerLinks, legal }: FooterProps): React.JSX.Element => (
        <div className='App__footer'>
            {selectedAppDefinition !== '' && (
                <p>
                    <label htmlFor='selectapp'> {selectedAppDefinition} </label>
                </p>
            )}

            <div className='App__footer__content'>
                <div className='App__footer__attribution'>
                    <a href={footerLinks?.attribution?.url || 'https://github.com/EduIDE'} target='_blank' rel='noreferrer'>
                        {footerLinks?.attribution?.text || 'EduIDE'}
                    </a>
                    {footerLinks?.attribution?.version && (
                        <>
                            <span className='App__footer__separator'> · </span>
                            <span>{footerLinks.attribution.version}</span>
                        </>
                    )}
                </div>

                <div className='App__footer__issues'>
                    <ConfiguredLink link={footerLinks?.bugReport} fallback={{ text: 'Report a bug', url: ISSUES_URL }} icon='🐞 ' />
                    {' or '}
                    <ConfiguredLink
                        link={footerLinks?.featureRequest}
                        fallback={{ text: 'Request a feature', url: ISSUES_URL }}
                        icon='💡 '
                    />
                </div>

                <div className='App__footer__legal'>
                    <ConfiguredLink
                        link={footerLinks?.about}
                        fallback={{ text: 'About', url: 'https://github.com/EduIDE' }}
                        className='App__footer__link'
                    />
                    {legal?.imprintUrl && (
                        <>
                            <span className='App__footer__separator'>|</span>
                            <a href={legal.imprintUrl} className='App__footer__link'>
                                Imprint
                            </a>
                        </>
                    )}
                    {legal?.privacyUrl && (
                        <>
                            <span className='App__footer__separator'>|</span>
                            <a href={legal.privacyUrl} className='App__footer__link'>
                                Privacy
                            </a>
                        </>
                    )}
                </div>
            </div>
        </div>
    );
