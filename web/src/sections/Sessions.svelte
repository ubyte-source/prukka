<script lang="ts">
  import { api, ApiError, deleteSession, updateLangs } from "../lib/api/client";
  import type { Session } from "../lib/api/types";
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
  import { token } from "../lib/state/token.svelte";
  import PushDialog from "./PushDialog.svelte";

  let pushTarget = $state<Session | null>(null);

  async function guard(action: () => Promise<void>, what: string) {
    try {
      await action();
    } catch (e) {
      daemon.log(e instanceof ApiError ? `${what}: ${e.message}` : `${what} failed`);
    }
    await daemon.refresh();
  }

  function remove(slug: string) {
    void guard(() => deleteSession(slug, token.value), `remove ${slug}`);
  }

  function dropLang(s: Session, lang: string) {
    void guard(
      () => updateLangs(s.slug, [], [lang], token.value),
      `remove ${lang} from ${s.slug}`,
    );
  }

  function addLang(s: Session, event: Event) {
    const select = event.currentTarget as HTMLSelectElement;
    const lang = select.value;
    select.value = "";
    if (!lang) return;
    void guard(
      () => updateLangs(s.slug, [lang], [], token.value),
      `add ${lang} to ${s.slug}`,
    );
  }
</script>

<section class="rounded-xl border border-line bg-panel p-5">
  <h2 class="mb-4 text-base font-semibold">{i18n.m.sessions.title}</h2>

  {#if daemon.sessions.length === 0}
    <p class="text-sm text-ink-dim">
      {i18n.m.sessions.empty}
      <code class="rounded bg-panel-2 px-1.5 py-0.5 font-mono text-xs">
        prukka session add &lt;slug&gt; --in &lt;url&gt; --langs it,en
      </code>
    </p>
  {:else}
    <div class="overflow-x-auto">
      <table class="w-full text-left text-sm">
        <thead>
          <tr class="border-b border-line text-xs uppercase tracking-wide text-ink-dim">
            <th class="py-2 pr-4">{i18n.m.sessions.session}</th>
            <th class="py-2 pr-4">{i18n.m.sessions.source}</th>
            <th class="py-2 pr-4">{i18n.m.sessions.languages}</th>
            <th class="py-2 pr-4 text-right">{i18n.m.sessions.budget}</th>
            <th class="py-2 pr-4 text-right">{i18n.m.sessions.delay}</th>
            <th class="py-2"></th>
          </tr>
        </thead>
        <tbody>
          {#each daemon.sessions as s (s.slug)}
            <tr class="border-b border-line/50 align-top">
              <td class="py-3 pr-4 font-mono font-medium">{s.slug}</td>
              <td class="max-w-56 truncate py-3 pr-4 font-mono text-xs text-ink-dim"
                  title={s.sourceUrl}>{s.sourceUrl}</td>
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
                        class="hover:underline">{lang}</a
                      >
                      <a
                        href={api(`/${s.slug}/${lang}/audio.ts`)}
                        title={i18n.m.sessions.dubbedAudio}
                        aria-label={`${i18n.m.sessions.dubbedAudio} ${lang}`}
                        class="text-ink-dim hover:text-brand">♪</a
                      >
                      <button
                        onclick={() => dropLang(s, lang)}
                        title={`${i18n.m.sessions.removeLang} ${lang}`}
                        aria-label={`${i18n.m.sessions.removeLang} ${lang} (${s.slug})`}
                        class="text-ink-dim hover:text-danger">×</button
                      >
                    </span>
                  {/each}
                  <select
                    onchange={(e) => addLang(s, e)}
                    aria-label={`${i18n.m.sessions.addLanguage} ${s.slug}`}
                    class="w-8 rounded-full border border-line bg-panel-2 py-0.5
                           text-center text-xs text-ink-dim outline-none
                           hover:border-ink-dim"
                  >
                    <option value="">+</option>
                    {#each daemon.languages.filter((l) => !s.langs.includes(l.tag)) as l (l.tag)}
                      <option value={l.tag}>{l.label}</option>
                    {/each}
                  </select>
                </div>
              </td>
              <td class="py-3 pr-4 text-right font-mono">
                {(s.budgetEurPerHour ?? 0).toFixed(2)}
              </td>
              <td class="py-3 pr-4 text-right font-mono">{s.delaySeconds ?? 0}s</td>
              <td class="py-3 text-right">
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
                  onclick={() => remove(s.slug)}
                  title={i18n.m.sessions.remove}
                  aria-label={`${i18n.m.sessions.removeSession} ${s.slug}`}
                  class="rounded-lg border border-line px-2.5 py-1 text-xs
                         text-ink-dim transition-colors hover:border-danger
                         hover:text-danger focus-visible:outline
                         focus-visible:outline-2 focus-visible:outline-danger"
                >
                  ✕
                </button>
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
