// Operator error notifications: API failures surface here as persistent toasts
// instead of inline text, so every section reports errors the same way.

import { ApiError } from "../api/client";
import { i18n } from "../i18n/index.svelte";

export interface Toast {
  id: number;
  text: string;
}

const maxItems = 3;

class ToastState {
  items = $state<Toast[]>([]);
  #next = 0;

  error(text: string) {
    const id = ++this.#next;
    this.items = [...this.items, { id, text }].slice(-maxItems);
  }

  /** failure words an exception for the operator; a 401 names the token. */
  failure(e: unknown, fallback: string) {
    if (e instanceof ApiError && e.status === 401) {
      this.error(i18n.m.toasts.unauthorized);
      return;
    }
    if (e instanceof ApiError) {
      this.error(e.message);
      return;
    }
    this.error(e instanceof Error ? `${fallback}: ${e.message}` : fallback);
  }

  dismiss(id: number) {
    this.items = this.items.filter((t) => t.id !== id);
  }
}

export const toasts = new ToastState();
