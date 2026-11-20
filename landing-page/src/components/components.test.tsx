import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { Api } from '../api';
import type { Overview, Session, Template } from '../api-client';
import type { AppConfig } from '../config';
import { I18nProvider } from '../I18nProvider';
import { toRow } from '../overview';
import { Dialog } from './Dialog';
import { EnvironmentRows } from './EnvironmentRows';
import { Footer } from './Footer';
import { Header } from './Header';
import { Home } from './Home';
import { Launch } from './Launch';
import { LegalPage } from './LegalPage';
import { InfoDialog, PickDialog } from './TemplateDialogs';
import { TemplateTile } from './TemplateTile';

const wrap = (ui: React.ReactNode): React.JSX.Element => <I18nProvider initial='en'>{ui}</I18nProvider>;
const legal: AppConfig['legal'] = { imprint: { url: 'https://example.org/imprint' }, privacy: { path: '/legal/privacy.md' } };

const java: Template = {
    name: 'java-17',
    displayName: 'Java 17',
    listed: true,
    persistence: ['none', 'volume'],
    defaultPersistence: 'volume',
    injectableKeys: ['TEMPLATE'],
    imageVersion: 'EduIDE 2026.11.2',
    startEstimate: { volume: { seconds: 25, strategy: 'cold', samples: 3 } },
    variants: [
        { id: 'maven', name: 'Maven', description: 'pom.xml' },
        { id: 'gradle', name: 'Gradle' }
    ],
    includes: [{ group: 'Language', items: [{ name: 'OpenJDK', version: '17' }, { name: 'git' }] }]
};
const python: Template = {
    name: 'python',
    displayName: 'Python',
    listed: true,
    persistence: ['none'],
    defaultPersistence: 'none',
    injectableKeys: [],
    startEstimate: { none: { seconds: 5, strategy: 'warm', samples: 3 } }
};

describe('Footer', () => {
    it('shows the same links everywhere: GitHub, bug report, imprint, privacy, and no feature request or about by default', () => {
        render(wrap(<Footer legal={legal} />));
        expect(screen.getByRole('link', { name: 'GitHub' }).getAttribute('href')).toBe('https://github.com/EduIDE/EduIDE-control-plane');
        expect(screen.getByRole('link', { name: 'Report a bug' }).getAttribute('href')).toBe(
            'https://github.com/EduIDE/EduIDE-control-plane/issues'
        );
        expect(screen.getByRole('link', { name: 'Status' }).getAttribute('href')).toBe('/status');
        expect(screen.getByRole('link', { name: 'Imprint' }).getAttribute('href')).toBe('https://example.org/imprint');
        expect(screen.getByRole('link', { name: 'Privacy' }).getAttribute('href')).toBe('/privacy');
        expect(screen.queryByText(/feature/i)).toBeNull();
        expect(screen.queryByRole('link', { name: 'About' })).toBeNull();
    });

    it('uses configured links and the version', () => {
        render(
            wrap(
                <Footer
                    legal={legal}
                    footerLinks={{
                        attribution: { version: 'v9' },
                        github: { url: 'https://git.example.org/x' },
                        bugReport: { text: 'Bugs', url: 'https://bugs.example' },
                        about: { text: 'Us', url: 'https://about.example' }
                    }}
                />
            )
        );
        screen.getByText('EduIDE v9');
        expect(screen.getByRole('link', { name: 'GitHub' }).getAttribute('href')).toBe('https://git.example.org/x');
        expect(screen.getByRole('link', { name: 'Bugs' }).getAttribute('href')).toBe('https://bugs.example');
        screen.getByRole('link', { name: 'Us' });
    });
});

describe('Header', () => {
    it('shows initials with the tooltip and switches the language and signs out through the menu', () => {
        render(wrap(<Header user={{ initials: 'AS', label: 'Alex Schmidt (ga58roj)' }} logoutUrl='https://kc.example/logout' />));
        const avatar = screen.getByRole('button', { name: /Alex Schmidt/ });
        expect(avatar.getAttribute('title')).toBe('Alex Schmidt (ga58roj)');
        expect(avatar.textContent).toBe('AS');
        fireEvent.click(avatar);
        expect(screen.getByRole('link', { name: 'Sign out' }).getAttribute('href')).toBe('https://kc.example/logout');
        expect(screen.getByRole('button', { name: 'English' }).getAttribute('aria-pressed')).toBe('true');
        fireEvent.click(screen.getByRole('button', { name: 'Deutsch' }));
        screen.getByRole('button', { name: /Konto/ });
        expect(localStorage.getItem('lang')).toBe('de');
        localStorage.clear();
    });
});

describe('Header menu', () => {
    it('closes on Escape and on an outside click, and returns the focus to the avatar', () => {
        render(wrap(<Header user={{ initials: 'AS', label: 'Alex' }} logoutUrl='https://kc.example/logout' />));
        const avatar = screen.getByRole('button', { name: /Alex/ });
        fireEvent.click(avatar);
        fireEvent.keyDown(document, { key: 'Escape' });
        expect(screen.queryByRole('link', { name: 'Sign out' })).toBeNull();
        expect(document.activeElement).toBe(avatar);
        fireEvent.click(avatar);
        fireEvent.mouseDown(document.body);
        expect(screen.queryByRole('link', { name: 'Sign out' })).toBeNull();
    });
});

describe('Dialog', () => {
    it('keeps Escape and the focus trap working after a click on plain text', () => {
        const onClose = vi.fn();
        render(
            <Dialog label='Hello' onClose={onClose}>
                <p>plain text</p>
                <button>first</button>
                <button>last</button>
            </Dialog>
        );
        const dialog = screen.getByRole('dialog');
        expect(dialog.getAttribute('tabindex')).toBe('-1');
        dialog.focus();
        expect(document.activeElement).toBe(dialog);
        fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true });
        expect(document.activeElement).toBe(screen.getByRole('button', { name: 'last' }));
        dialog.focus();
        fireEvent.keyDown(dialog, { key: 'Escape' });
        expect(onClose).toHaveBeenCalledOnce();
    });

    it('focuses in, closes on Escape and returns the focus', () => {
        const onClose = vi.fn();
        const Opener = (): React.JSX.Element => (
            <>
                <button>open</button>
            </>
        );
        const { rerender } = render(<Opener />);
        const opener = screen.getByRole('button', { name: 'open' });
        opener.focus();
        rerender(
            <>
                <Opener />
                <Dialog label='Hello' onClose={onClose}>
                    <button>first</button>
                    <button data-autofocus>second</button>
                </Dialog>
            </>
        );
        const dialog = screen.getByRole('dialog', { name: 'Hello' });
        expect(dialog.getAttribute('aria-modal')).toBe('true');
        expect(document.activeElement).toBe(screen.getByRole('button', { name: 'second' }));
        fireEvent.keyDown(dialog, { key: 'Escape' });
        expect(onClose).toHaveBeenCalledOnce();
        rerender(<Opener />);
        expect(document.activeElement).toBe(opener);
    });
});

describe('TemplateTile', () => {
    it('shows the start time, the persistence tag with its sentence, and the info button only with includes', () => {
        const onPick = vi.fn();
        const onInfo = vi.fn();
        const { rerender } = render(wrap(<TemplateTile template={java} onPick={onPick} onInfo={onInfo} />));
        screen.getByText('Ready in about 25 s');
        const tag = screen.getByLabelText(/Saved workspace\. Your files are kept after the environment stops/);
        expect(tag.getAttribute('data-tip')).toBe('Your files are kept after the environment stops, so you can continue later.');
        fireEvent.click(screen.getByRole('button', { name: 'What Java 17 includes' }));
        expect(onInfo).toHaveBeenCalledWith(java);
        expect(onPick).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole('button', { name: 'Start Java 17' }));
        expect(onPick).toHaveBeenCalledWith(java);

        rerender(wrap(<TemplateTile template={python} onPick={onPick} onInfo={onInfo} />));
        screen.getByText('Ready instantly');
        screen.getByLabelText(/Temporary workspace\. Your files are deleted when the environment stops\./);
        expect(screen.queryByRole('button', { name: /includes/ })).toBeNull();
    });

    it('shows Ready instantly for a warm temporary template without an estimate', () => {
        render(wrap(<TemplateTile template={{ ...python, startEstimate: undefined, warm: true }} onPick={vi.fn()} onInfo={vi.fn()} />));
        screen.getByText('Ready instantly');
    });

    it('takes the logo from the bundle and ignores an absolute address from the API', () => {
        const { container } = render(
            wrap(<TemplateTile template={{ ...java, logoUrl: 'https://cdn.example.org/java.png' }} onPick={vi.fn()} onInfo={vi.fn()} />)
        );
        expect(container.querySelector('.logo img')?.getAttribute('src')).toMatch(/java-17-logo/);
    });

    it('shows the folder icon for a template without a logo', () => {
        const { container } = render(
            wrap(
                <TemplateTile
                    template={{ ...python, name: 'zig', logoUrl: 'https://cdn.example.org/zig.png' }}
                    onPick={vi.fn()}
                    onInfo={vi.fn()}
                />
            )
        );
        expect(container.querySelector('.logo img')).toBeNull();
        expect(container.querySelector('.logo svg')).not.toBeNull();
    });

    it('shows no start time without an estimate', () => {
        render(wrap(<TemplateTile template={{ ...python, startEstimate: undefined }} onPick={vi.fn()} onInfo={vi.fn()} />));
        expect(screen.queryByText(/Ready/)).toBeNull();
    });
});

describe('template dialogs', () => {
    it('lists include groups as name version chips, project templates and the image version', () => {
        const onStart = vi.fn();
        render(wrap(<InfoDialog template={java} onClose={vi.fn()} onStart={onStart} />));
        screen.getByText('OpenJDK 17');
        screen.getByText('git');
        screen.getByText('Project templates');
        screen.getByText('EduIDE 2026.11.2');
        fireEvent.click(screen.getByRole('button', { name: 'Start' }));
        expect(onStart).toHaveBeenCalledWith(java);
    });

    it('preselects the first project template, offers an empty project and starts with the choice', () => {
        const onStart = vi.fn();
        render(wrap(<PickDialog template={java} onClose={vi.fn()} onInfo={vi.fn()} onStart={onStart} />));
        screen.getByRole('dialog', { name: 'New Java 17 environment' });
        expect((screen.getByRole('radio', { name: /Maven/ }) as HTMLInputElement).checked).toBe(true);
        fireEvent.click(screen.getByRole('radio', { name: /Gradle/ }));
        fireEvent.click(screen.getByRole('button', { name: 'Start' }));
        expect(onStart).toHaveBeenLastCalledWith(java, 'gradle');
        fireEvent.click(screen.getByRole('radio', { name: /Empty project/ }));
        fireEvent.click(screen.getByRole('button', { name: 'Start' }));
        expect(onStart).toHaveBeenLastCalledWith(java, '');
        screen.getByText('Saved workspace.');
    });

    it('shows bundled logos for known project templates and the folder for the empty project', () => {
        const withIds = {
            ...java,
            variants: [
                { id: 'maven', name: 'Maven' },
                { id: 'gradle', name: 'Gradle', logoUrl: 'https://cdn.example.org/g.png' },
                { id: 'custom', name: 'Custom' }
            ]
        };
        render(wrap(<PickDialog template={withIds} onClose={vi.fn()} onInfo={vi.fn()} onStart={vi.fn()} />));
        const logo = (name: RegExp): Element | undefined =>
            screen.getByRole('radio', { name }).closest('label')?.querySelector('.opt-logo img') ?? undefined;
        expect(logo(/Maven/)?.getAttribute('src')).toMatch(/maven-logo/);
        expect(logo(/Gradle/)?.getAttribute('src')).toMatch(/gradle-logo/);
        expect(logo(/Custom/)).toBeUndefined();
        expect(logo(/Empty project/)).toBeUndefined();
    });
});

const now = Date.now();
const iso = (ms: number): string => new Date(now + ms).toISOString();
const session = (over: Partial<Session> = {}): Session => ({
    name: 's-1',
    template: 'java-17',
    phase: 'Ready',
    createdAt: iso(-12 * 60_000),
    url: 'https://ide.example.org/s-1/',
    ...over
});

describe('EnvironmentRows', () => {
    const rowFor = (item: Overview['items'][number]): ReturnType<typeof toRow> => toRow(item, now);
    const running = rowFor({
        kind: 'session',
        title: 'Pointer exercise',
        template: 'c',
        templateDisplayName: 'C',
        running: true,
        stopsAt: iso(5 * 60_000),
        session: session({ name: 's-2', template: 'c', stopsAt: iso(5 * 60_000) })
    });
    const saved = rowFor({
        kind: 'workspace',
        title: 'Hello World',
        template: 'java-17',
        templateDisplayName: 'Java 17',
        running: false,
        lastUsed: iso(-11 * 24 * 3600_000),
        deletesAt: iso(2 * 24 * 3600_000),
        workspace: { name: 'w-1', template: 'java-17', phase: 'Ready', createdAt: iso(-20 * 24 * 3600_000) }
    });
    const api = (del: ReturnType<typeof vi.fn>): Api => ({ client: { DELETE: del } }) as unknown as Api;

    it('describes the lifecycle and marks files in danger red', () => {
        render(wrap(<EnvironmentRows rows={[running, saved]} api={api(vi.fn())} onStart={vi.fn()} now={now} />));
        const first = screen.getByText('Pointer exercise').closest('.row') as HTMLElement;
        expect(within(first).getByText(/^C · temporary workspace · started 12 minutes ago$/)).toBeTruthy();
        const life = within(first).getByText(/files are deleted then/);
        expect(life.textContent).toMatch(/^Stops at \d\d:\d\d if inactive, files are deleted then$/);
        expect(life.className).toContain('danger');
        const second = screen.getByText('Hello World').closest('.row') as HTMLElement;
        expect(within(second).getByText(/saved workspace · last used 11 days ago/)).toBeTruthy();
        const del = within(second).getByText(/unless you start it/);
        expect(del.className).toContain('danger');
        expect(within(second).getByRole('button', { name: 'Delete' })).toBeTruthy();
        expect(within(second).getByRole('button', { name: 'Start' })).toBeTruthy();
    });

    it('starts the row through the callback', () => {
        const onStart = vi.fn();
        render(wrap(<EnvironmentRows rows={[saved]} api={api(vi.fn())} onStart={onStart} now={now} />));
        fireEvent.click(screen.getByRole('button', { name: 'Start' }));
        expect(onStart).toHaveBeenCalledWith(saved);
    });

    it('confirms before deleting, names the consequences and deletes the workspace', async () => {
        const del = vi.fn(async () => ({ response: { ok: true, status: 204 } }));
        const onDeleted = vi.fn();
        render(wrap(<EnvironmentRows rows={[saved]} api={api(del)} onStart={vi.fn()} onDeleted={onDeleted} now={now} />));
        fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
        const dialog = screen.getByRole('dialog', { name: 'Delete Hello World?' });
        expect(dialog.textContent).toContain('deleted permanently and cannot be restored');
        expect(dialog.textContent).not.toContain('open IDE');
        expect(del).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
        await waitFor(() => expect(onDeleted).toHaveBeenCalledWith(saved));
        expect(del).toHaveBeenCalledWith('/workspaces/{name}', { params: { path: { name: 'w-1' } } });
        expect(screen.queryByRole('dialog')).toBeNull();
    });

    it('warns that the IDE stops for a running item, and shows a readable error when deleting times out', async () => {
        const del = vi.fn(async () => ({ response: { ok: false, status: 500 }, error: { code: 'internal' } }));
        render(wrap(<EnvironmentRows rows={[running]} api={api(del)} onStart={vi.fn()} now={now} />));
        fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
        const dialog = screen.getByRole('dialog');
        expect(dialog.textContent).toContain('The open IDE stops.');
        fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
        await screen.findByText('Deleting did not work. Try again in a moment.');
        expect(screen.getByRole('dialog')).toBeTruthy();
        fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
        expect(screen.queryByRole('dialog')).toBeNull();
    });
});

describe('Home', () => {
    const overview = (items: Overview['items']): Api =>
        ({
            client: {},
            watchOverview: async (on: (o: Overview) => void) => {
                on({ items });
                await new Promise(() => undefined);
            }
        }) as unknown as Api;

    it('greets a new user, shows the empty hint and starts a template without variants directly', async () => {
        const onLaunch = vi.fn();
        render(wrap(<Home api={overview([])} templates={[python]} firstName='Alex' onLaunch={onLaunch} onStartRow={vi.fn()} />));
        await screen.findByText('Environments you start show up here, so you can open them again later.');
        screen.getByRole('heading', { level: 1, name: 'Welcome, Alex' });
        fireEvent.click(screen.getByRole('button', { name: 'Start Python' }));
        expect(onLaunch).toHaveBeenCalledWith({ template: 'python' }, python);
    });

    it('greets a returning user, lists the environments on top and starts a variant through the picker', async () => {
        const onLaunch = vi.fn();
        const items: Overview['items'] = [
            { kind: 'session', title: 'Linked lists', template: 'java-17', running: true, session: session() }
        ];
        render(wrap(<Home api={overview(items)} templates={[java]} firstName='Alex' onLaunch={onLaunch} onStartRow={vi.fn()} />));
        await screen.findByText('Linked lists');
        screen.getByRole('heading', { level: 1, name: 'Welcome back, Alex' });
        const sections = screen.getAllByRole('heading', { level: 2 }).map(h => h.textContent);
        expect(sections[0]).toContain('Your environments');
        fireEvent.click(screen.getByRole('button', { name: 'Start Java 17' }));
        fireEvent.click(screen.getByRole('radio', { name: /Gradle/ }));
        fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Start' }));
        expect(onLaunch).toHaveBeenCalledWith({ template: 'java-17', data: { TEMPLATE: 'gradle' } }, java);
        expect(screen.queryByRole('dialog')).toBeNull();
    });

    it('starts directly when the template does not accept TEMPLATE, even with project templates', async () => {
        const onLaunch = vi.fn();
        const noKey = { ...java, injectableKeys: [] };
        render(wrap(<Home api={overview([])} templates={[noKey]} onLaunch={onLaunch} onStartRow={vi.fn()} />));
        await screen.findByText('Environments you start show up here, so you can open them again later.');
        fireEvent.click(screen.getByRole('button', { name: 'Start Java 17' }));
        expect(screen.queryByRole('dialog')).toBeNull();
        expect(onLaunch).toHaveBeenCalledWith({ template: 'java-17' }, noKey);
    });

    it('updates ages and warning colors on a 30 second tick without new events', async () => {
        vi.useFakeTimers();
        try {
            const items: Overview['items'] = [
                {
                    kind: 'session',
                    title: 'Soon',
                    template: 'java-17',
                    running: true,
                    stopsAt: iso(11 * 60_000),
                    session: session({ stopsAt: iso(11 * 60_000) })
                }
            ];
            const { unmount } = render(wrap(<Home api={overview(items)} templates={[]} onLaunch={vi.fn()} onStartRow={vi.fn()} />));
            await act(async () => {
                await vi.advanceTimersByTimeAsync(0);
            });
            const life = (): HTMLElement => screen.getByText(/^Stops at/);
            expect(life().className).not.toContain('danger');
            screen.getByText(/started 12 minutes ago/);
            await act(async () => {
                await vi.advanceTimersByTimeAsync(90_000);
            });
            expect(life().className).toContain('danger');
            screen.getByText(/started 13 minutes ago/);
            const cleared = vi.getTimerCount();
            unmount();
            expect(vi.getTimerCount()).toBeLessThan(cleared);
        } finally {
            vi.useRealTimers();
        }
    });

    it('offers only listed templates as tiles and hides the section when none is listed', async () => {
        const hidden = { ...python, name: 'hidden', displayName: 'Hidden', listed: false };
        const { unmount } = render(wrap(<Home api={overview([])} templates={[java, hidden]} onLaunch={vi.fn()} onStartRow={vi.fn()} />));
        await screen.findByText('Environments you start show up here, so you can open them again later.');
        screen.getByRole('button', { name: 'Start Java 17' });
        expect(screen.queryByRole('button', { name: 'Start Hidden' })).toBeNull();
        unmount();
        render(wrap(<Home api={overview([])} templates={[hidden]} onLaunch={vi.fn()} onStartRow={vi.fn()} />));
        await screen.findByText('Environments you start show up here, so you can open them again later.');
        expect(screen.queryByRole('heading', { name: 'Start new' })).toBeNull();
    });

    it('opens the info dialog from the picker', async () => {
        render(wrap(<Home api={overview([])} templates={[java]} onLaunch={vi.fn()} onStartRow={vi.fn()} />));
        await screen.findByText('Environments you start show up here, so you can open them again later.');
        screen.getByRole('heading', { level: 1, name: 'Welcome' });
        fireEvent.click(screen.getByRole('button', { name: 'Start Java 17' }));
        fireEvent.click(screen.getByRole('button', { name: "What's included" }));
        screen.getByRole('dialog', { name: 'What this environment includes' });
    });
});

describe('Launch', () => {
    const info = { template: 'c', title: 'Hello World', templateName: 'C', seconds: 5 };
    const launchApi = (launch: () => Promise<unknown>, watch = async (): Promise<void> => new Promise<void>(() => undefined)): Api =>
        ({ launchDeps: { launch, watch }, client: { DELETE: vi.fn() } }) as unknown as Api;

    it('shows the API phases and the time estimate, never that the tab can be closed', async () => {
        render(
            wrap(
                <Launch
                    api={launchApi(async () => ({ session: session({ phase: 'Starting' }) }))}
                    request={{ template: 'c' }}
                    info={info}
                    templates={[]}
                    onBack={vi.fn()}
                    onStartRow={vi.fn()}
                />
            )
        );
        await screen.findByText('Starting your environment');
        screen.getByText('Request accepted');
        screen.getByText('Opening the IDE');
        screen.getByText('Usually ready in about 5 seconds.');
        expect(document.body.textContent).not.toMatch(/close/i);
    });

    it('lists the running environments at the session limit with the actual count and a Back button, without the code', async () => {
        const onBack = vi.fn();
        const sessions = [session({ name: 'a', display: { title: 'One' } }), session({ name: 'b', display: { title: 'Two' } })];
        render(
            wrap(
                <Launch
                    api={launchApi(async () => ({ problem: { code: 'session-limit-reached', sessions } }))}
                    request={{ template: 'c' }}
                    info={info}
                    templates={[java]}
                    onBack={onBack}
                    onStartRow={vi.fn()}
                />
            )
        );
        await screen.findByText('You already have 2 environments running');
        screen.getByText('Delete one to start a new one, or continue in one of these.');
        screen.getByText('One');
        screen.getByText('Two');
        expect(document.body.textContent).not.toContain('session-limit-reached');
        expect(screen.getAllByRole('button', { name: 'Delete' })).toHaveLength(2);
        fireEvent.click(screen.getByRole('button', { name: 'Back' }));
        expect(onBack).toHaveBeenCalledOnce();
    });

    it('restarts the launch after a delete at the limit, so the flow continues', async () => {
        const launch = vi
            .fn()
            .mockResolvedValueOnce({
                problem: { code: 'session-limit-reached', sessions: [session({ name: 'a', display: { title: 'One' } })] }
            })
            .mockResolvedValue({ session: session({ phase: 'Starting' }) });
        const del = vi.fn(async () => ({ response: { ok: true, status: 204 } }));
        const api = { launchDeps: { launch, watch: () => new Promise<void>(() => undefined) }, client: { DELETE: del } } as unknown as Api;
        render(wrap(<Launch api={api} request={{ template: 'c' }} info={info} templates={[]} onBack={vi.fn()} onStartRow={vi.fn()} />));
        await screen.findByText('You already have 1 environment running');
        fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
        fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Delete' }));
        await screen.findByText('Starting your environment');
        expect(launch).toHaveBeenCalledTimes(2);
        expect(document.body.textContent).not.toContain('0 environments');
    });

    it('says Sending request until the session exists, then Request accepted', async () => {
        let resolve: (v: unknown) => void = () => undefined;
        const launch = vi.fn(() => new Promise(r => (resolve = r)));
        render(
            wrap(
                <Launch
                    api={launchApi(launch)}
                    request={{ template: 'c' }}
                    info={info}
                    templates={[]}
                    onBack={vi.fn()}
                    onStartRow={vi.fn()}
                />
            )
        );
        screen.getByText('Sending request');
        expect(screen.queryByText('Request accepted')).toBeNull();
        await act(async () => resolve({ session: session({ phase: 'Pending' }) }));
        screen.getByText('Request accepted');
    });

    it('drops the stored Artemis token when leaving through Back', async () => {
        sessionStorage.setItem('eduide.artemisToken', 'secret');
        render(
            wrap(
                <Launch
                    api={launchApi(async () => ({ problem: { code: 'capacity-exhausted' } }))}
                    request={{ template: 'c' }}
                    info={info}
                    templates={[]}
                    onBack={vi.fn()}
                    onStartRow={vi.fn()}
                />
            )
        );
        fireEvent.click(await screen.findByRole('button', { name: 'Back' }));
        expect(sessionStorage.getItem('eduide.artemisToken')).toBeNull();
    });

    it('shows a readable message with Try again and Back for other failures', async () => {
        render(
            wrap(
                <Launch
                    api={launchApi(async () => ({ problem: { code: 'capacity-exhausted' } }))}
                    request={{ template: 'c' }}
                    info={info}
                    templates={[]}
                    onBack={vi.fn()}
                    onStartRow={vi.fn()}
                />
            )
        );
        await screen.findByText('The cluster is full right now. Please try again in a few minutes.');
        screen.getByRole('button', { name: 'Try again' });
        screen.getByRole('button', { name: 'Back' });
        expect(document.body.textContent).not.toContain('capacity-exhausted');
    });
});

describe('Launch with a refused repository host', () => {
    const info = { template: 'c', title: 'x', templateName: 'C', seconds: 5 };
    const api = (problem: object): Api =>
        ({
            launchDeps: { launch: async () => ({ problem }), watch: async () => undefined },
            client: { DELETE: vi.fn() }
        }) as unknown as Api;

    it('names the host instead of the raw API detail', async () => {
        const detail = 'invalid value for key GIT_URI: host is not allowed';
        render(
            wrap(
                <Launch
                    api={api({ code: 'invalid-input', detail })}
                    request={{ template: 'c', data: { GIT_URI: 'https://untrusted.example.org/x.git' } }}
                    info={info}
                    templates={[]}
                    onBack={vi.fn()}
                    onStartRow={vi.fn()}
                />
            )
        );
        await screen.findByText(
            'Repositories from untrusted.example.org cannot be opened here. Ask your course team for a link to an approved repository.'
        );
        expect(document.body.textContent).not.toContain(detail);
    });

    it('translates other invalid input and adds the key and rule the service names', async () => {
        render(
            wrap(
                <Launch
                    api={api({ code: 'invalid-input', detail: 'invalid value for key TEMPLATE: not a variant of this template' })}
                    request={{ template: 'c' }}
                    info={info}
                    templates={[]}
                    onBack={vi.fn()}
                    onStartRow={vi.fn()}
                />
            )
        );
        await screen.findByText(
            'The link contains a value this environment does not accept. (invalid value for key TEMPLATE: not a variant of this template)'
        );
    });
});

describe('LegalPage', () => {
    it('renders sanitized Markdown from the hosted file', async () => {
        vi.stubGlobal(
            'fetch',
            vi.fn(async () => new Response('# Privacy\n\n<script>alert(1)</script>Hello', { status: 200 }))
        );
        const { container } = render(wrap(<LegalPage route='privacy' page={{ path: '/legal/privacy.md' }} />));
        await waitFor(() => expect(container.querySelector('.prose')).not.toBeNull());
        expect(container.querySelector('.prose script')).toBeNull();
        expect(container.querySelector('.prose')?.textContent).toContain('Hello');
        expect(fetch).toHaveBeenCalledWith('/legal/privacy.md', expect.anything());
    });

    it('says so when the file is missing', async () => {
        vi.stubGlobal(
            'fetch',
            vi.fn(async () => new Response('', { status: 404 }))
        );
        render(wrap(<LegalPage route='imprint' page={{ path: '/legal/imprint.md' }} />));
        await screen.findByText('This page is not available.');
    });

    it('sends the browser to an external page', () => {
        const replace = vi.fn();
        vi.stubGlobal('location', { replace });
        render(wrap(<LegalPage route='imprint' page={{ url: 'https://example.org/imprint' }} />));
        expect(replace).toHaveBeenCalledWith('https://example.org/imprint');
    });
});
