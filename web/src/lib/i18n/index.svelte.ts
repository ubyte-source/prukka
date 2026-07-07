// Locale state: browser language first, the operator's explicit choice
// (persisted) wins, and the <html lang> attribute follows for assistive
// technology.

import { locales, messages, type Locale, type Messages } from "./messages";

const KEY = "prukka-locale";

function initial(): Locale {
  const stored = localStorage.getItem(KEY);
  if (stored && (locales as readonly string[]).includes(stored)) {
    return stored as Locale;
  }

  const browser = navigator.language.slice(0, 2);

  return (locales as readonly string[]).includes(browser) ? (browser as Locale) : "en";
}

class I18n {
  locale = $state<Locale>(initial());
  m = $derived<Messages>(messages[this.locale]);

  set(next: Locale) {
    this.locale = next;
    localStorage.setItem(KEY, next);
    document.documentElement.lang = next;
  }
}

export const i18n = new I18n();

document.documentElement.lang = i18n.locale;
