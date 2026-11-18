import { makeText } from '../i18n';

// Plain language only: students see what works and how long they wait, never pods, pools or error codes.
const en = {
    title: 'Status',
    'hero.ok': 'Everything is working',
    'hero.okText': 'You can start and use your environments as usual.',
    'hero.slow': 'Starting takes longer right now',
    'hero.slowText': 'Many people are starting at the same time. Your environment still starts, it just takes a bit longer.',
    'hero.downStarting': 'New environments cannot start',
    'hero.downStartingText': 'We are working on it. Environments that are already open keep running.',
    'hero.downOther': 'Something is not working right now',
    'hero.downOtherText': 'We are working on it. The list below shows what is affected.',
    'parts.title': 'What works right now',
    'parts.signIn': 'Sign in',
    'parts.signInText': 'Logging in with your university account',
    'parts.starting': 'Starting environments',
    'parts.startingText': 'Opening a new or saved environment',
    'parts.workspaces': 'Saved workspaces',
    'parts.workspacesText': 'Your files in saved workspaces',
    'level.ok': 'Working',
    'level.slow': 'Slower',
    'level.down': 'Not working',
    'waits.title': 'Start time and ready environments',
    'waits.temporary': 'Temporary',
    'waits.saved': 'Saved',
    'waits.instant': 'Instantly',
    'waits.seconds': 'About {n} s',
    'waits.minutes': 'About {n} min',
    'waits.ready': 'Ready environments available',
    'waits.prepared': 'Prepared when you start it',
    'waits.note':
        'Temporary workspaces start from a ready environment when one is free. Saved workspaces always need a moment to attach your files.',
    updated: 'Updated {time}, refreshes on its own',
    unavailable: 'Status is not available right now',
    unavailableText: 'Try again in a moment.',
    loading: 'Loading the status...'
} as const;

const de: Record<keyof typeof en, string> = {
    title: 'Status',
    'hero.ok': 'Alles funktioniert',
    'hero.okText': 'Du kannst deine Umgebungen wie gewohnt starten und nutzen.',
    'hero.slow': 'Der Start dauert gerade länger',
    'hero.slowText': 'Gerade starten viele gleichzeitig. Deine Umgebung startet trotzdem, es dauert nur etwas länger.',
    'hero.downStarting': 'Neue Umgebungen starten gerade nicht',
    'hero.downStartingText': 'Wir arbeiten daran. Bereits geöffnete Umgebungen laufen weiter.',
    'hero.downOther': 'Gerade funktioniert etwas nicht',
    'hero.downOtherText': 'Wir arbeiten daran. Die Liste unten zeigt, was betroffen ist.',
    'parts.title': 'Was gerade funktioniert',
    'parts.signIn': 'Anmelden',
    'parts.signInText': 'Anmeldung mit deinem Uni-Konto',
    'parts.starting': 'Umgebungen starten',
    'parts.startingText': 'Eine neue oder gespeicherte Umgebung öffnen',
    'parts.workspaces': 'Gespeicherte Workspaces',
    'parts.workspacesText': 'Deine Dateien in gespeicherten Workspaces',
    'level.ok': 'Funktioniert',
    'level.slow': 'Langsamer',
    'level.down': 'Funktioniert nicht',
    'waits.title': 'Startzeit und bereite Umgebungen',
    'waits.temporary': 'Temporär',
    'waits.saved': 'Gespeichert',
    'waits.instant': 'Sofort',
    'waits.seconds': 'Etwa {n} s',
    'waits.minutes': 'Etwa {n} min',
    'waits.ready': 'Bereite Umgebungen verfügbar',
    'waits.prepared': 'Wird beim Start vorbereitet',
    'waits.note':
        'Temporäre Workspaces starten aus einer bereiten Umgebung, wenn eine frei ist. Gespeicherte Workspaces brauchen immer einen Moment, um deine Dateien einzubinden.',
    updated: 'Aktualisiert {time}, lädt von selbst neu',
    unavailable: 'Status gerade nicht abrufbar',
    unavailableText: 'Versuche es gleich noch einmal.',
    loading: 'Der Status wird geladen...'
};

export type StatusKey = keyof typeof en;
export const statusText = makeText<StatusKey>(en, de);
