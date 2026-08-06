// Locale state: browser language first, the operator's explicit choice
// (persisted) wins, and the <html lang> attribute follows for assistive
// technology.

import { locales, messages, type Locale, type Messages } from "./messages";

const KEY = "prukka-locale";

function initial(): Locale {
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(KEY);
  } catch {
    // Browser storage is optional; the current language still works in memory.
  }
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
    try {
      localStorage.setItem(KEY, next);
    } catch {
      // Keep the in-memory selection when persistence is unavailable.
    }
    document.documentElement.lang = next;
  }
}

export const i18n = new I18n();

document.documentElement.lang = i18n.locale;
