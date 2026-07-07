<script lang="ts">
  import { getConfig, updateConfig } from "../lib/api/client";
  import type { DaemonConfig } from "../lib/api/types";
  import { autoTranslationTargetSupported } from "../lib/capabilities";
  import Select from "../lib/components/Select.svelte";
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
  import { toasts } from "../lib/state/toasts.svelte";
  import { isControlToken, token } from "../lib/state/token.svelte";

  const languages = $derived(daemon.languages);

  let config = $state<DaemonConfig | null>(null);
  let loadError = $state(false);
  let notice = $state("");
  let restartNotes = $state<string[]>([]);
  let busy = $state(false);
  let loadRevision = 0;
  let inFlight: AbortController | null = null;
  const controlToken = $derived(isControlToken(token.value) ? token.value : "");

  $effect(() => {
    const tokenSnapshot = controlToken;
    void load(tokenSnapshot);
    return () => {
      loadRevision += 1;
      inFlight?.abort();
      inFlight = null;
      busy = false;
    };
  });

  async function load(tokenSnapshot: string) {
    const revision = ++loadRevision;
    inFlight?.abort();
    inFlight = null;
    busy = false;
    config = null;
    loadError = false;

    if (!isControlToken(tokenSnapshot)) return;

    const controller = new AbortController();
    inFlight = controller;
    try {
      const loaded = await getConfig(tokenSnapshot, controller.signal);
      if (revision !== loadRevision || controller.signal.aborted) return;

      config = loaded;
      daemon.setConfig(loaded);
    } catch (e) {
      if (revision !== loadRevision || controller.signal.aborted) return;

      loadError = true;
      toasts.failure(e, i18n.m.settings.loadFailed);
    } finally {
      if (revision === loadRevision && inFlight === controller) inFlight = null;
    }
  }

  function toggleLang(tag: string) {
    if (!config?.defaults) return;
    markDirty();
    const langs = config.defaults.langs ?? [];
    config.defaults.langs = langs.includes(tag)
      ? langs.filter((t) => t !== tag)
      : [...langs, tag];
  }

  function defaultLanguageAvailable(tag: string) {
    return autoTranslationTargetSupported(config?.providers?.local?.mt?.pairs ?? [], tag);
  }

  function markDirty() {
    notice = "";
    restartNotes = [];
  }

  async function save(event: SubmitEvent) {
    event.preventDefault();
    const tokenSnapshot = controlToken;
    if (!config || !isControlToken(tokenSnapshot) || busy) return;

    const revision = loadRevision;
    const submitted = $state.snapshot(config);
    const controller = new AbortController();
    inFlight?.abort();
    inFlight = controller;
    notice = "";
    restartNotes = [];
    busy = true;
    try {
      const reply = await updateConfig(submitted, tokenSnapshot, controller.signal);
      if (
        revision !== loadRevision ||
        controller.signal.aborted ||
        controlToken !== tokenSnapshot
      ) return;
      if (!reply.config) throw new Error("configuration response is empty");
      daemon.setConfig(reply.config);
      config = reply.config;
      restartNotes = reply.restartRequired ?? [];
      notice = i18n.m.settings.saved;
    } catch (e) {
      if (revision !== loadRevision || controller.signal.aborted) return;
      toasts.failure(e, i18n.m.settings.saveFailed);
    } finally {
      if (revision === loadRevision) busy = false;
      if (inFlight === controller) inFlight = null;
    }
  }
</script>

{#snippet textField(label: string, get: () => string, set: (v: string) => void, mono = true)}
  <label class="flex flex-col gap-1 text-sm">
    <span class="text-ink-dim">{label}</span>
    <input
      value={get()}
      oninput={(e) => set(e.currentTarget.value)}
      type="text"
      class="rounded-lg border border-line bg-panel-2 px-3 py-2
             {mono ? 'font-mono' : ''} outline-none focus:border-brand"
    />
  </label>
{/snippet}

{#snippet numberField(
  label: string,
  get: () => number,
  set: (v: number) => void,
  step = "any",
  maximum = "",
)}
  <label class="flex flex-col gap-1 text-sm">
    <span class="text-ink-dim">{label}</span>
    <input
      value={get()}
      oninput={(e) => set(Number(e.currentTarget.value))}
      type="number"
      {step}
      min="0"
      max={maximum || undefined}
      class="rounded-lg border border-line bg-panel-2 px-3 py-2 font-mono
             outline-none focus:border-brand"
    />
  </label>
{/snippet}

<section
  aria-labelledby="settings-title"
  aria-busy={busy}
  class="rounded-xl border border-line bg-panel p-5"
>
  <h2 id="settings-title" class="mb-4 text-base font-semibold">{i18n.m.settings.title}</h2>

  {#if !isControlToken(token.value)}
    <p class="text-sm text-ink-dim">{i18n.m.settings.tokenRequired}</p>
  {:else if loadError}
    <div role="alert" class="flex flex-wrap items-center gap-3 text-sm text-danger">
      <p>{i18n.m.settings.loadFailed}</p>
      <button
        type="button"
        onclick={() => void load(token.value)}
        class="rounded-lg border border-line px-3 py-1.5 text-ink hover:border-brand"
      >
        {i18n.m.settings.retry}
      </button>
    </div>
  {:else if !config}
    <p class="text-sm text-ink-dim" role="status">{i18n.m.settings.loading}</p>
  {:else}
    <form onsubmit={save} oninput={markDirty} autocomplete="off" class="flex flex-col gap-6">
      <fieldset disabled={busy} class="contents">
      {#if config.defaults}
        {@const d = config.defaults}
        <fieldset class="flex flex-col gap-3">
          <legend class="mb-2 text-sm font-semibold">{i18n.m.settings.defaults}</legend>

          <div class="flex flex-col gap-1 text-sm">
            <span class="text-ink-dim">{i18n.m.settings.defaultLangs}</span>
            <p class="text-xs text-ink-dim">{i18n.m.settings.defaultLangsHint}</p>
            <div
              class="flex max-h-44 flex-wrap gap-2 overflow-y-auto rounded-lg
                     border border-line p-2"
              role="group"
              aria-label={i18n.m.settings.defaultLangs}
            >
              {#each languages.filter((lang) =>
                d.langs?.includes(lang.tag) || defaultLanguageAvailable(lang.tag)
              ) as lang (lang.tag)}
                {@const supported = defaultLanguageAvailable(lang.tag)}
                <button
                  type="button"
                  onclick={() => toggleLang(lang.tag)}
                  aria-pressed={d.langs?.includes(lang.tag) ?? false}
                  class="rounded-full border px-3 py-1 text-xs
                         {d.langs?.includes(lang.tag)
                    ? supported
                      ? 'border-brand bg-panel-2 text-ink'
                      : 'border-danger bg-panel-2 text-danger'
                    : 'border-line text-ink-dim'}"
                >
                  {#if d.langs?.includes(lang.tag)}<span aria-hidden="true">✓ </span>{/if}
                  {lang.label}{supported ? "" : ` · ${i18n.m.wizard.translationUnavailable}`}
                </button>
              {/each}
            </div>
          </div>

          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <div class="flex flex-col gap-1 text-sm">
              <span class="text-ink-dim">{i18n.m.settings.subs}</span>
              <Select
                bind:value={() => d.subs ?? "vtt", (v) => (d.subs = v)}
                onchange={markDirty}
                label={i18n.m.settings.subs}
                options={[
                  { value: "vtt", label: i18n.m.settings.subsVtt },
                  { value: "off", label: i18n.m.settings.subsOff },
                  { value: "burn", label: i18n.m.settings.subsBurn },
                ]}
              />
            </div>
            {@render textField(i18n.m.settings.bed, () => d.bed ?? "", (v) => (d.bed = v))}
            {@render numberField(i18n.m.settings.delay, () => d.delaySeconds ?? 8, (v) => (d.delaySeconds = v), "1", "60")}
          </div>
        </fieldset>
      {/if}

      <div class="flex flex-wrap items-center gap-3">
        <button
          type="submit"
          disabled={busy}
          class="rounded-lg bg-brand-dim px-4 py-2 font-medium text-white
                 hover:brightness-110 disabled:opacity-40"
        >
          {busy ? i18n.m.settings.saving : i18n.m.settings.save}
        </button>
        {#if notice}
          <p class="text-sm text-ok" role="status">{notice}</p>
        {/if}
      </div>

      {#if restartNotes.length > 0}
        <p class="text-xs text-warn" role="status">
          {i18n.m.settings.restartNote}
          {restartNotes.join(", ")}
        </p>
      {/if}
      </fieldset>
    </form>
  {/if}
</section>
