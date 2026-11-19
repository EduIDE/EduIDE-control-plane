import { createContext, useContext } from 'react';

const en = {
    'brand.logoAlt': 'EduIDE',
    'greeting.back': 'Welcome back, {name}',
    'greeting.backAnon': 'Welcome back',
    'greeting.new': 'Welcome, {name}',
    'greeting.newAnon': 'Welcome',
    'lead.back': 'Pick up where you left off, or start a new environment.',
    'lead.new': 'Pick an environment. It opens in your browser, nothing to install.',
    'section.mine': 'Your environments',
    'section.new': 'Start new',
    'empty.mine': 'Environments you start show up here, so you can open them again later.',
    'loading.mine': 'Loading your environments...',
    connectionLost: 'Lost the connection to the service, retrying.',
    templatesFailed: 'Could not load the environments.',
    unknownTemplate: 'Unknown environment: {name}',
    account: 'Account {label}',
    language: 'Language',
    signOut: 'Sign out',
    'persistence.saved': 'Saved workspace',
    'persistence.temporary': 'Temporary workspace',
    'persistence.savedLong': 'Your files are kept after the environment stops, so you can continue later.',
    'persistence.temporaryLong': 'Your files are deleted when the environment stops.',
    'ready.instantly': 'Ready instantly',
    'ready.about': 'Ready in about {n} s',
    'tile.start': 'Start {name}',
    'tile.includes': 'What {name} includes',
    'info.title': 'What this environment includes',
    'info.variants': 'Project templates',
    'info.whatsIncluded': "What's included",
    'pick.title': 'New {name} environment',
    'pick.subtitle': 'Pick a project template',
    'pick.legend': 'Project template',
    'pick.empty': 'Empty project',
    'pick.emptyDesc': 'Start with an empty folder',
    start: 'Start',
    cancel: 'Cancel',
    close: 'Close',
    back: 'Back',
    tryAgain: 'Try again',
    delete: 'Delete',
    'row.saved': 'saved workspace',
    'row.temporary': 'temporary workspace',
    'row.startedAgo': 'started {time}',
    'row.lastUsedAgo': 'last used {time}',
    'life.stopsIdle': 'Stops at {time} if inactive',
    'life.stopsMax': 'Stops at {time} (time limit)',
    'life.filesDeleted': ', files are deleted then',
    'life.deletesOn': 'Deleted on {date}',
    'life.unlessStart': ' unless you start it',
    'delete.title': 'Delete {title}?',
    'delete.text': 'All files of this environment are deleted permanently and cannot be restored.',
    'delete.running': 'The open IDE stops.',
    'delete.retry': 'Deleting did not work. Try again in a moment.',
    'delete.failed': 'The environment could not be deleted.',
    'launch.sending': 'Sending request',
    'launch.accepted': 'Request accepted',
    'launch.starting': 'Starting your environment',
    'launch.opening': 'Opening the IDE',
    'launch.estimate': 'Usually ready in about {n} seconds.',
    'launch.steps': 'Progress',
    'limit.titleOne': 'You already have 1 environment running',
    'limit.title': 'You already have {n} environments running',
    'limit.text': 'Delete one to start a new one, or continue in one of these.',
    'error.capacity-exhausted': 'The cluster is full right now. Please try again in a few minutes.',
    'error.persistence-not-allowed': 'This environment does not support the requested storage mode.',
    'error.template-not-found': 'This environment does not exist.',
    'error.start-failed': 'The environment could not be started.',
    'error.rate-limited': 'Too many requests. Please wait a moment and try again.',
    'error.workspace-limit-reached': 'You have reached the maximum number of saved workspaces. Delete one you no longer need and try again.',
    'error.unauthorized': 'Your login expired. Reload the page to sign in again.',
    'error.forbidden': 'You are not allowed to start this environment.',
    'error.unreachable': 'The service could not be reached.',
    'error.connection-lost': 'Lost the connection to the service.',
    'error.no-address': 'The environment has no valid address.',
    'error.host-not-allowed': 'Repositories from {host} cannot be opened here. Ask your course team for a link to an approved repository.',
    'error.invalid-input': 'The link contains a value this environment does not accept.',
    'error.generic': 'Something went wrong.',
    startFailed: 'The page could not be started. Please reload.',
    'footer.version': 'EduIDE {version}',
    'footer.github': 'GitHub',
    'footer.bug': 'Report a bug',
    'footer.imprint': 'Imprint',
    'footer.privacy': 'Privacy',
    'footer.status': 'Status',
    'menu.admin': 'Admin',
    'legal.imprint': 'Imprint',
    'legal.privacy': 'Privacy',
    'legal.loading': 'Loading...',
    'legal.failed': 'This page is not available.',
    'legal.home': 'Back to EduIDE'
} as const;

export type MessageKey = keyof typeof en;

const de: Record<MessageKey, string> = {
    'brand.logoAlt': 'EduIDE',
    'greeting.back': 'Willkommen zurück, {name}',
    'greeting.backAnon': 'Willkommen zurück',
    'greeting.new': 'Willkommen, {name}',
    'greeting.newAnon': 'Willkommen',
    'lead.back': 'Mach dort weiter, wo du aufgehört hast, oder starte eine neue Umgebung.',
    'lead.new': 'Wähle eine Umgebung. Sie öffnet sich im Browser, du musst nichts installieren.',
    'section.mine': 'Deine Umgebungen',
    'section.new': 'Neue Umgebung starten',
    'empty.mine': 'Umgebungen, die du startest, erscheinen hier, damit du sie später wieder öffnen kannst.',
    'loading.mine': 'Deine Umgebungen werden geladen...',
    connectionLost: 'Die Verbindung zum Dienst ist abgebrochen, neuer Versuch läuft.',
    templatesFailed: 'Die Umgebungen konnten nicht geladen werden.',
    unknownTemplate: 'Unbekannte Umgebung: {name}',
    account: 'Konto {label}',
    language: 'Sprache',
    signOut: 'Abmelden',
    'persistence.saved': 'Gespeicherter Workspace',
    'persistence.temporary': 'Temporärer Workspace',
    'persistence.savedLong': 'Deine Dateien bleiben erhalten, wenn die Umgebung stoppt, du kannst also später weitermachen.',
    'persistence.temporaryLong': 'Deine Dateien werden gelöscht, wenn die Umgebung stoppt.',
    'ready.instantly': 'Sofort bereit',
    'ready.about': 'Bereit in etwa {n} s',
    'tile.start': '{name} starten',
    'tile.includes': 'Was {name} enthält',
    'info.title': 'Was diese Umgebung enthält',
    'info.variants': 'Projektvorlagen',
    'info.whatsIncluded': 'Was ist enthalten',
    'pick.title': 'Neue {name}-Umgebung',
    'pick.subtitle': 'Projektvorlage wählen',
    'pick.legend': 'Projektvorlage',
    'pick.empty': 'Leeres Projekt',
    'pick.emptyDesc': 'Mit einem leeren Ordner beginnen',
    start: 'Starten',
    cancel: 'Abbrechen',
    close: 'Schließen',
    back: 'Zurück',
    tryAgain: 'Erneut versuchen',
    delete: 'Löschen',
    'row.saved': 'gespeicherter Workspace',
    'row.temporary': 'temporärer Workspace',
    'row.startedAgo': 'gestartet {time}',
    'row.lastUsedAgo': 'zuletzt genutzt {time}',
    'life.stopsIdle': 'Stoppt um {time} bei Inaktivität',
    'life.stopsMax': 'Stoppt um {time} (Zeitlimit)',
    'life.filesDeleted': ', danach werden die Dateien gelöscht',
    'life.deletesOn': 'Wird am {date} gelöscht',
    'life.unlessStart': ', es sei denn, du startest sie',
    'delete.title': '{title} löschen?',
    'delete.text': 'Alle Dateien dieser Umgebung werden endgültig gelöscht und können nicht wiederhergestellt werden.',
    'delete.running': 'Die geöffnete IDE wird gestoppt.',
    'delete.retry': 'Das Löschen hat nicht geklappt. Versuche es gleich noch einmal.',
    'delete.failed': 'Die Umgebung konnte nicht gelöscht werden.',
    'launch.sending': 'Anfrage wird gesendet',
    'launch.accepted': 'Anfrage angenommen',
    'launch.starting': 'Deine Umgebung startet',
    'launch.opening': 'Die IDE wird geöffnet',
    'launch.estimate': 'Meist in etwa {n} Sekunden bereit.',
    'launch.steps': 'Fortschritt',
    'limit.titleOne': 'Du hast bereits eine laufende Umgebung',
    'limit.title': 'Du hast bereits {n} laufende Umgebungen',
    'limit.text': 'Lösche eine, um eine neue zu starten, oder mach in einer dieser weiter.',
    'error.capacity-exhausted': 'Der Cluster ist gerade voll. Bitte versuche es in ein paar Minuten erneut.',
    'error.persistence-not-allowed': 'Diese Umgebung unterstützt den gewünschten Speichermodus nicht.',
    'error.template-not-found': 'Diese Umgebung gibt es nicht.',
    'error.start-failed': 'Die Umgebung konnte nicht gestartet werden.',
    'error.rate-limited': 'Zu viele Anfragen. Bitte warte kurz und versuche es erneut.',
    'error.workspace-limit-reached': 'Du hast die maximale Zahl gespeicherter Workspaces erreicht. Lösche einen, den du nicht mehr brauchst, und versuche es erneut.',
    'error.unauthorized': 'Deine Anmeldung ist abgelaufen. Lade die Seite neu, um dich anzumelden.',
    'error.forbidden': 'Du darfst diese Umgebung nicht starten.',
    'error.unreachable': 'Der Dienst ist nicht erreichbar.',
    'error.connection-lost': 'Die Verbindung zum Dienst ist abgebrochen.',
    'error.no-address': 'Die Umgebung hat keine gültige Adresse.',
    'error.host-not-allowed':
        'Repositories von {host} können hier nicht geöffnet werden. Frag dein Kursteam nach einem Link zu einem freigegebenen Repository.',
    'error.invalid-input': 'Der Link enthält einen Wert, den diese Umgebung nicht annimmt.',
    'error.generic': 'Etwas ist schiefgelaufen.',
    startFailed: 'Die Seite konnte nicht gestartet werden. Bitte lade sie neu.',
    'footer.version': 'EduIDE {version}',
    'footer.github': 'GitHub',
    'footer.bug': 'Fehler melden',
    'footer.imprint': 'Impressum',
    'footer.privacy': 'Datenschutz',
    'footer.status': 'Status',
    'menu.admin': 'Admin',
    'legal.imprint': 'Impressum',
    'legal.privacy': 'Datenschutz',
    'legal.loading': 'Wird geladen...',
    'legal.failed': 'Diese Seite ist nicht verfügbar.',
    'legal.home': 'Zurück zu EduIDE'
};

export type Lang = 'en' | 'de';
export const LANGS: Lang[] = ['en', 'de'];
const MESSAGES: Record<Lang, Record<MessageKey, string>> = { en, de };
const STORAGE_KEY = 'lang';

export type Translate = (key: MessageKey, params?: Record<string, string | number>) => string;

export type Params = Record<string, string | number>;

export function fill(text: string, params?: Params): string {
    return text.replace(/\{(\w+)\}/g, (whole, name: string) => (name in (params ?? {}) ? String(params?.[name]) : whole));
}

export function translate(lang: Lang, key: MessageKey, params?: Params): string {
    return fill(MESSAGES[lang][key], params);
}

/** A separate dictionary for a lazily loaded page, so its strings stay out of the main bundle. */
export function makeText<K extends string>(
    enText: Record<K, string>,
    deText: Record<K, string>
): (lang: Lang, key: K, params?: Params) => string {
    // A key from a newer server (a new phase or reason) has no text yet: show its last part instead of crashing.
    return (lang, key, params) => fill((lang === 'de' ? deText : enText)[key] ?? key.split('.').pop() ?? key, params);
}

export function hasMessage(key: string): key is MessageKey {
    return key in en;
}

export function detectLang(navigatorLang: string | undefined, stored: string | undefined): Lang {
    if (stored === 'en' || stored === 'de') {
        return stored;
    }
    return navigatorLang?.toLowerCase().startsWith('de') ? 'de' : 'en';
}

export function loadLang(): Lang {
    let stored: string | undefined;
    try {
        stored = localStorage.getItem(STORAGE_KEY) ?? undefined;
    } catch {
        // Blocked storage only means the choice is not remembered.
    }
    return detectLang(navigator.language, stored);
}

export function saveLang(lang: Lang): void {
    try {
        localStorage.setItem(STORAGE_KEY, lang);
    } catch {
        // See loadLang.
    }
}

export interface I18n {
    lang: Lang;
    setLang: (lang: Lang) => void;
    t: Translate;
}

export const I18nContext = createContext<I18n | undefined>(undefined);

export function useI18n(): I18n {
    const value = useContext(I18nContext);
    if (!value) {
        throw new Error('useI18n must be used within an I18nProvider');
    }
    return value;
}
