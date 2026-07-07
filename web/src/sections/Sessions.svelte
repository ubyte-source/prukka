<script lang="ts">
  import { api, ApiError, deleteSession, updateLangs } from "../lib/api/client";
  import type { Session } from "../lib/api/types";
  import {
    autoTranslationTargetSupported,
    translationSupported,
  } from "../lib/capabilities";
  import Select from "../lib/components/Select.svelte";
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
  import { toasts } from "../lib/state/toasts.svelte";
  import { token } from "../lib/state/token.svelte";
  import PushDialog from "./PushDialog.svelte";

  let pushTarget = $state<Session | null>(null);
  let pending = $state<Set<string>>(new Set());

  async function guard(slug: string, action: () => Promise<void>, what: string) {
    if (pending.has(slug)) return;
    pending = new Set(pending).add(slug);
    try {
      await action();
    } catch (e) {
      daemon.log(e instanceof ApiError ? `${what}: ${e.message}` : `${what} failed`);
      toasts.failure(e, `${what} failed`);
    } finally {
      await daemon.refresh();
      const next = new Set(pending);
      next.delete(slug);
      pending = next;
    }
  }

  function remove(slug: string) {
    void guard(slug, () => deleteSession(slug, token.value), `remove ${slug}`);
  }

  function dropLang(s: Session, lang: string) {
    void guard(
      s.slug,
      () => updateLangs(s.slug, [], [lang], token.value),
      `remove ${lang} from ${s.slug}`,
    );
  }

  function addLang(s: Session, lang: string) {
    void guard(
      s.slug,
      () => updateLangs(s.slug, [lang], [], token.value),
      `add ${lang} to ${s.slug}`,
    );
  }

  function canTranslateTo(session: Session, target: string) {
    const pairs = daemon.config.providers?.local?.mt?.pairs ?? [];
    const source = session.flags?.source;
    return source && source !== "auto"
      ? translationSupported(pairs, source, target)
      : autoTranslationTargetSupported(pairs, target);
  }

  function isDubbed(s: Session, lang: string) {
    const ready = s.status === "running" || s.status === "finished";
    return ready && (s.effectiveDubbedLangs ?? []).includes(lang);
  }

  function statusLabel(status: Session["status"]) {
    switch (status) {
      case "starting": return i18n.m.sessions.statusStarting;
      case "running": return i18n.m.sessions.statusRunning;
      case "finished": return i18n.m.sessions.statusFinished;
      case "failed": return i18n.m.sessions.statusFailed;
      default: return i18n.m.sessions.statusUnknown;
    }
  }

  function sourceLabel(session: Session): string {
    if (session.sourceLabel) return session.sourceLabel;
    if (!session.sourceUrl) return "—";

    try {
      const source = new URL(session.sourceUrl);
      if (source.protocol === "file:") return "file://[local]";
      return `${source.protocol}//${source.host || "[source]"}`;
    } catch {
      return "[source]";
    }
  }
</script>

<section aria-labelledby="sessions-title" class="rounded-xl border border-line bg-panel p-5">
  <h2 id="sessions-title" class="mb-4 text-base font-semibold">{i18n.m.sessions.title}</h2>

  {#if daemon.sessions.length === 0}
    <p class="text-sm text-ink-dim">
      {i18n.m.sessions.empty}
      <code class="rounded bg-panel-2 px-1.5 py-0.5 font-mono text-xs">
        prukka session add &lt;slug&gt; --in &lt;url&gt; --langs it,en
      </code>
    </p>
  {:else}
    <div class="overflow-x-auto">
      <table class="w-full text-left text-sm" aria-labelledby="sessions-title">
        <thead>
          <tr class="border-b border-line text-xs uppercase tracking-wide text-ink-dim">
            <th scope="col" class="py-2 pr-4">{i18n.m.sessions.session}</th>
            <th scope="col" class="py-2 pr-4">{i18n.m.sessions.status}</th>
            <th scope="col" class="py-2 pr-4">{i18n.m.sessions.source}</th>
            <th scope="col" class="py-2 pr-4">{i18n.m.sessions.languages}</th>
            <th scope="col" class="py-2 pr-4 text-right">{i18n.m.sessions.delay}</th>
            <th scope="col" class="py-2 text-right">{i18n.m.sessions.actions}</th>
          </tr>
        </thead>
        <tbody>
          {#each daemon.sessions as s (s.slug)}
            <tr class="border-b border-line/50 align-top">
              <th scope="row" class="py-3 pr-4 text-left font-mono font-medium">{s.slug}</th>
              <td class="py-3 pr-4">
                <span
                  class:text-danger={s.status === "failed"}
                  class:text-brand={s.status === "running"}
                  class="font-medium"
                >{statusLabel(s.status)}</span>
                {#if s.error}
                  <p class="mt-1 max-w-72 text-xs text-danger" title={s.error}>{s.error}</p>
                {/if}
              </td>
              <td class="max-w-56 truncate py-3 pr-4 font-mono text-xs text-ink-dim"
                  title={sourceLabel(s)}>{sourceLabel(s)}</td>
              <td class="py-3 pr-4">
                <div class="flex flex-wrap items-center gap-1.5">
                  {#each s.langs as lang (lang)}
                    <span
                      class="group inline-flex items-center gap-1 rounded-full
                             border border-brand/40 bg-brand/10 px-2 py-0.5
                             font-mono text-xs text-brand"
                    >
                      <a
                        href={api(`/${s.slug}/${lang}/subs.vtt`)}
                        title={i18n.m.sessions.liveSubs}
                        aria-label={`${i18n.m.sessions.liveSubs} ${lang}`}
                        class="inline-flex min-h-6 min-w-6 items-center justify-center
                               hover:underline">{lang}</a
                      >
                      {#if isDubbed(s, lang)}
                        <a
                          href={api(`/${s.slug}/${lang}/audio.ts`)}
                          title={i18n.m.sessions.dubbedAudio}
                          aria-label={`${i18n.m.sessions.dubbedAudio} ${lang}`}
                          class="inline-flex min-h-6 min-w-6 items-center justify-center
                                 text-ink-dim hover:text-brand">♪</a
                        >
                      {/if}
                      <button
                        disabled={pending.has(s.slug)}
                        onclick={() => dropLang(s, lang)}
                        title={`${i18n.m.sessions.removeLang} ${lang}`}
                        aria-label={`${i18n.m.sessions.removeLang} ${lang} (${s.slug})`}
                        class="text-ink-dim hover:text-danger disabled:opacity-40">×</button
                      >
                    </span>
                  {/each}
                  <Select
                    compact
                    disabled={pending.has(s.slug)}
                    label={`${i18n.m.sessions.addLanguage} ${s.slug}`}
                    options={daemon.languages
                      .filter((l) => !s.langs.includes(l.tag) && canTranslateTo(s, l.tag))
                      .map((l) => ({ value: l.tag, label: l.label }))}
                    onchange={(tag) => addLang(s, tag)}
                  />
                </div>
              </td>
              <td class="py-3 pr-4 text-right font-mono">{s.delaySeconds ?? 0}s</td>
              <td class="py-3">
                <div class="flex justify-end gap-2">
                  <button
                    onclick={() => (pushTarget = s)}
                    title={i18n.m.sessions.pushTitle}
                    class="rounded-lg border border-line px-2.5 py-1 text-xs
                           text-ink-dim transition-colors hover:border-brand
                           hover:text-brand focus-visible:outline
                           focus-visible:outline-2 focus-visible:outline-brand"
                  >
                    {i18n.m.sessions.push}
                  </button>
                  <button
                    disabled={pending.has(s.slug)}
                    onclick={() => remove(s.slug)}
                    title={i18n.m.sessions.remove}
                    aria-label={`${i18n.m.sessions.removeSession} ${s.slug}`}
                    class="rounded-lg border border-line px-2.5 py-1 text-xs
                           text-ink-dim transition-colors hover:border-danger
                           hover:text-danger focus-visible:outline
                           focus-visible:outline-2 focus-visible:outline-danger
                           disabled:opacity-40"
                  >
                    ✕
                  </button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

{#if pushTarget}
  <PushDialog session={pushTarget} onclose={() => (pushTarget = null)} />
{/if}
