import { useSyncExternalStore } from "react";
import { dict as enDict } from "./en";
import { dict as zhCNDict } from "./zh-cn";
import { dict as zhTWDict } from "./zh-tw";

export type Locale = "zh-CN" | "zh-TW" | "en";
export type Dict = Record<string, string>;

const FALLBACK_LOCALE: Locale = "zh-CN";
const STORAGE_KEY = "buzz.locale";
const DICTS: Record<Locale, Dict> = {
  "zh-CN": zhCNDict,
  "zh-TW": zhTWDict,
  en: enDict,
};

export const LOCALE_LABEL: Record<Locale, string> = {
  "zh-CN": "中文(简体)",
  "zh-TW": "中文(繁體)",
  en: "English",
};

function detectInitial(): Locale {
  if (typeof window === "undefined") return FALLBACK_LOCALE;
  const stored = window.localStorage.getItem(STORAGE_KEY);
  if (stored === "zh-CN" || stored === "zh-TW" || stored === "en") return stored;
  const lang = navigator.language?.toLowerCase() ?? "";
  if (lang.startsWith("zh")) {
    if (lang.includes("tw") || lang.includes("hk") || lang.includes("hant")) return "zh-TW";
    return "zh-CN";
  }
  return "en";
}

type Listener = () => void;

class LocaleStore {
  private current: Locale = detectInitial();
  private listeners = new Set<Listener>();

  getSnapshot = (): Locale => this.current;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  set = (next: Locale): void => {
    if (this.current === next) return;
    this.current = next;
    if (typeof window !== "undefined") {
      window.localStorage.setItem(STORAGE_KEY, next);
      document.documentElement.lang = next;
    }
    for (const listener of this.listeners) listener();
  };
}

export const localeStore = new LocaleStore();

export function useLocale(): {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: string, params?: Record<string, string | number>) => string;
} {
  const locale = useSyncExternalStore(localeStore.subscribe, localeStore.getSnapshot, localeStore.getSnapshot);
  const t = (key: string, params?: Record<string, string | number>): string => {
    return interpolate(translate(key, locale), params);
  };
  return { locale, setLocale: localeStore.set, t };
}

export function tNow(key: string, params?: Record<string, string | number>): string {
  return interpolate(translate(key, localeStore.getSnapshot()), params);
}

function translate(key: string, locale: Locale): string {
  const value = DICTS[locale]?.[key];
  if (value != null) return value;
  if (locale !== FALLBACK_LOCALE) {
    const fallback = DICTS[FALLBACK_LOCALE]?.[key];
    if (fallback != null) return fallback;
  }
  return key;
}

function interpolate(template: string, params?: Record<string, string | number>): string {
  if (!params) return template;
  return template.replace(/\{\{(\w+)\}\}/g, (_, key) => {
    const value = params[key];
    return value == null ? `{{${key}}}` : String(value);
  });
}
