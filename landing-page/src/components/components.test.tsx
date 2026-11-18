import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { ThemeProvider } from '../contexts/ThemeContext';
import { useTheme } from '../contexts/useTheme';
import { AppLogo } from './AppLogo';
import { ErrorComponent } from './ErrorComponent';
import { Footer } from './Footer';
import { Header } from './Header';
import { Loading } from './Loading';
import { Spinner } from './Spinner';
import { ThemeToggle } from './ThemeToggle';

function stubLocation(): { href: string } {
    const location = { href: '' };
    vi.stubGlobal('location', location);
    return location;
}

describe('ErrorComponent', () => {
    it('shows a message, and nothing without one', () => {
        const { container, rerender } = render(<ErrorComponent />);
        expect(container.innerHTML).toBe('');
        rerender(<ErrorComponent message='kaputt' />);
        screen.getByText('ERROR: kaputt');
    });
});

describe('Footer', () => {
    it('falls back to the default links and shows no made-up version', () => {
        render(<Footer selectedAppDefinition='' />);
        expect(screen.getByRole('link', { name: /EduIDE/ }).getAttribute('href')).toBe('https://github.com/EduIDE');
        expect(screen.getByRole('link', { name: /Report a bug/ }).getAttribute('href')).toBe(
            'https://github.com/EduIDE/EduIDE-control-plane/issues'
        );
        screen.getByRole('link', { name: /Request a feature/ });
        screen.getByRole('link', { name: 'About' });
        expect(screen.queryByText('·')).toBeNull();
    });

    it('uses the configured links and version', () => {
        render(
            <Footer
                selectedAppDefinition=''
                footerLinks={{
                    attribution: { text: 'Built by us', url: 'https://us.example', version: 'v9' },
                    bugReport: { text: 'Bugs', url: 'https://bugs.example' },
                    featureRequest: { text: 'Ideas', url: 'https://ideas.example', target: '_self', rel: 'nofollow' },
                    about: { text: 'Us', url: 'https://about.example' }
                }}
            />
        );
        expect(screen.getByRole('link', { name: 'Built by us' }).getAttribute('href')).toBe('https://us.example');
        screen.getByText('v9');
        const bugs = screen.getByRole('link', { name: /Bugs/ });
        expect([bugs.getAttribute('href'), bugs.getAttribute('target'), bugs.getAttribute('rel')]).toEqual([
            'https://bugs.example',
            '_blank',
            'noreferrer'
        ]);
        const ideas = screen.getByRole('link', { name: /Ideas/ });
        expect([ideas.getAttribute('target'), ideas.getAttribute('rel')]).toEqual(['_self', 'nofollow']);
        expect(screen.getByRole('link', { name: 'Us' }).getAttribute('href')).toBe('https://about.example');
    });

    it('labels the selected app definition only when there is one', () => {
        const { rerender } = render(<Footer selectedAppDefinition='' />);
        expect(screen.queryByText('java-17')).toBeNull();
        rerender(<Footer selectedAppDefinition='java-17' />);
        screen.getByText('java-17');
    });

    it('shows Imprint and Privacy links only when configured', () => {
        const { rerender } = render(<Footer selectedAppDefinition='' />);
        expect(screen.queryByRole('link', { name: 'Imprint' })).toBeNull();
        expect(screen.queryByRole('link', { name: 'Privacy' })).toBeNull();
        rerender(<Footer selectedAppDefinition='' legal={{ privacyUrl: 'https://legal.example/privacy' }} />);
        expect(screen.queryByRole('link', { name: 'Imprint' })).toBeNull();
        expect(screen.getByRole('link', { name: 'Privacy' }).getAttribute('href')).toBe('https://legal.example/privacy');
        rerender(
            <Footer
                selectedAppDefinition=''
                legal={{ imprintUrl: 'https://legal.example/imprint', privacyUrl: 'https://legal.example/privacy' }}
            />
        );
        expect(screen.getByRole('link', { name: 'Imprint' }).getAttribute('href')).toBe('https://legal.example/imprint');
    });
});

describe('Header', () => {
    const renderHeader = (props: React.ComponentProps<typeof Header>): void => {
        render(
            <ThemeProvider>
                <Header {...props} />
            </ThemeProvider>
        );
    };

    it('shows neither login nor user without Keycloak', () => {
        renderHeader({});
        screen.getByRole('heading', { name: 'EduIDE Cloud' });
        expect(screen.queryByRole('button', { name: 'Login' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Logout' })).toBeNull();
    });

    it('offers login until the user is known', () => {
        const authenticate = vi.fn();
        renderHeader({ authenticate });
        fireEvent.click(screen.getByRole('button', { name: 'Login' }));
        expect(authenticate).toHaveBeenCalledOnce();
    });

    it('shows the user instead of login, and logs out through the logout URL', () => {
        const location = stubLocation();
        renderHeader({ email: 'ada@example.org', authenticate: vi.fn(), logoutUrl: 'https://keycloak.example/logout' });
        screen.getByText('ada@example.org');
        expect(screen.queryByRole('button', { name: 'Login' })).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: 'Logout' }));
        expect(location.href).toBe('https://keycloak.example/logout');
    });
});

describe('Loading and Spinner', () => {
    it('shows the default or the configured text with a spinner', () => {
        const { container, rerender } = render(<Loading />);
        screen.getByText(/hang in tight/);
        expect(container.querySelector('.circular-spinner')).not.toBeNull();
        rerender(<Loading text='Please wait' />);
        screen.getByText('Please wait');
    });

    it('Spinner renders on its own', () => {
        const { container } = render(<Spinner />);
        expect(container.querySelector('.circular-spinner')).not.toBeNull();
    });
});

describe('AppLogo', () => {
    it('uses the configured file extension', () => {
        const { container } = render(<AppLogo fileExtension='png' />);
        expect(container.querySelector('img')?.getAttribute('src')).toBe('/images/logos/theia-logo.png');
    });
});

describe('ThemeToggle', () => {
    const renderToggle = (): void => {
        render(
            <ThemeProvider>
                <ThemeToggle />
            </ThemeProvider>
        );
    };

    beforeEach(() => {
        localStorage.clear();
        document.documentElement.removeAttribute('data-theme');
    });

    it('starts light and toggles back and forth, remembering the choice', () => {
        renderToggle();
        expect(document.documentElement.getAttribute('data-theme')).toBe('light');

        fireEvent.click(screen.getByRole('button', { name: 'Switch to dark mode' }));
        expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
        expect(localStorage.getItem('theme')).toBe('dark');

        fireEvent.click(screen.getByRole('button', { name: 'Switch to light mode' }));
        expect(document.documentElement.getAttribute('data-theme')).toBe('light');
        expect(localStorage.getItem('theme')).toBe('light');
    });

    it('still works when the browser blocks storage', () => {
        const blocked = (): never => {
            throw new DOMException('blocked', 'SecurityError');
        };
        vi.stubGlobal('localStorage', { getItem: blocked, setItem: blocked, clear: () => undefined });
        renderToggle();
        fireEvent.click(screen.getByRole('button', { name: 'Switch to dark mode' }));
        expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
        vi.unstubAllGlobals();
    });

    it('ignores a saved theme it does not know', () => {
        localStorage.setItem('theme', 'neon');
        renderToggle();
        expect(document.documentElement.getAttribute('data-theme')).toBe('light');
    });

    it('follows the system preference without a saved choice', () => {
        vi.stubGlobal('matchMedia', () => ({ matches: true }));
        renderToggle();
        screen.getByRole('button', { name: 'Switch to light mode' });
    });

    it('prefers the saved choice over the system preference', () => {
        localStorage.setItem('theme', 'light');
        vi.stubGlobal('matchMedia', () => ({ matches: true }));
        renderToggle();
        screen.getByRole('button', { name: 'Switch to dark mode' });
    });

    it('useTheme refuses to work outside the provider', () => {
        vi.spyOn(console, 'error').mockImplementation(() => undefined);
        const Consumer = (): React.JSX.Element => <span>{useTheme().theme}</span>;
        expect(() => render(<Consumer />)).toThrow('useTheme must be used within a ThemeProvider');
    });
});
