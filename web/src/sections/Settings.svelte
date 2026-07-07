<script lang="ts">
  import { ApiError, getConfig, setKey, updateConfig } from "../lib/api/client";
  import type { DaemonConfig } from "../lib/api/types";
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
  import { token } from "../lib/state/token.svelte";

  const languages = $derived(daemon.languages);

  let config = $state<DaemonConfig | null>(null);
  let error = $state("");
  let notice = $state("");
  let restartNotes = $state<string[]>([]);
  let busy = $state(false);

  // Key inputs are write-only: they never mirror the stored secret.
  let keyInputs = $state<Record<string, string>>({ openrouter: "", cartesia: "" });
  let keyNotices = $state<Record<string, string>>({ openrouter: "", cartesia: "" });

  $effect(() => {
    void load();
  });

  async function load() {
    try {
      config = await getConfig();
    } catch {
      error = i18n.m.settings.loadFailed;
    }
  }

  function toggleLang(tag: string) {
    if (!config?.defaults) return;
    const langs = config.defaults.langs ?? [];
    config.defaults.langs = langs.includes(tag)
      ? langs.filter((t) => t !== tag)
      : [...langs, tag];
  }

  async function save(event: SubmitEvent) {
    event.preventDefault();
    if (!config) return;

    error = "";
    notice = "";
    restartNotes = [];
    busy = true;
    try {
      const reply = await updateConfig(config, token.value);
      config = reply.config ?? config;
      restartNotes = reply.restartRequired ?? [];
      notice = i18n.m.settings.saved;
    } catch (e) {
      error = e instanceof ApiError ? e.message : i18n.m.settings.saveFailed;
    } finally {
      busy = false;
    }
  }

  async function saveKey(provider: "openrouter" | "cartesia") {
    const key = keyInputs[provider].trim();
    if (!key) return;

    keyNotices[provider] = "";
    try {
      await setKey(provider, key, token.value);
      keyInputs[provider] = "";
      keyNotices[provider] = i18n.m.settings.keySaved;
      await load();
    } catch (e) {
      keyNotices[provider] =
        e instanceof ApiError ? e.message : i18n.m.settings.keyFailed;
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

{#snippet numberField(label: string, get: () => number, set: (v: number) => void, step = "any")}
  <label class="flex flex-col gap-1 text-sm">
    <span class="text-ink-dim">{label}</span>
    <input
      value={get()}
      oninput={(e) => set(Number(e.currentTarget.value))}
      type="number"
      {step}
      min="0"
      class="rounded-lg border border-line bg-panel-2 px-3 py-2 font-mono
             outline-none focus:border-brand"
    />
  </label>
{/snippet}

{#snippet keyRow(provider: "openrouter" | "cartesia", keySet: boolean)}
  <div class="flex flex-col gap-1 text-sm">
    <span class="text-ink-dim">
      {i18n.m.settings.apiKey}
      <span class={keySet ? "text-ok" : "text-warn"}>
        · {keySet ? i18n.m.settings.keySet : i18n.m.settings.keyUnset}
      </span>
    </span>
    <div class="flex gap-2">
      <input
        bind:value={keyInputs[provider]}
        type="password"
        autocomplete="off"
        placeholder={i18n.m.settings.keyPlaceholder}
        class="min-w-0 flex-1 rounded-lg border border-line bg-panel-2 px-3 py-2
               font-mono outline-none focus:border-brand"
      />
      <button
        type="button"
        onclick={() => saveKey(provider)}
        disabled={!keyInputs[provider].trim()}
        class="rounded-lg border border-line bg-panel-2 px-3 py-2
               hover:border-brand disabled:opacity-40"
      >
        {i18n.m.settings.saveKey}
      </button>
    </div>
    {#if keyNotices[provider]}
      <p class="text-xs text-ink-dim">{keyNotices[provider]}</p>
    {/if}
  </div>
{/snippet}

<section class="rounded-xl border border-line bg-panel p-5">
  <h2 class="mb-4 text-base font-semibold">{i18n.m.settings.title}</h2>

  {#if !config}
    <p class="text-sm text-ink-dim">{error || "…"}</p>
  {:else}
    <form onsubmit={save} autocomplete="off" class="flex flex-col gap-6">
      <!-- Inference backend -->
      <fieldset class="flex flex-col gap-3">
        <legend class="mb-2 text-sm font-semibold">{i18n.m.settings.backend}</legend>

        <div class="grid gap-3 sm:grid-cols-2">
          {#each [{ id: "openrouter", name: i18n.m.settings.backendOpenrouter, desc: i18n.m.settings.backendOpenrouterDesc }, { id: "local", name: i18n.m.settings.backendLocal, desc: i18n.m.settings.backendLocalDesc }] as option (option.id)}
            <label
              class="flex cursor-pointer flex-col gap-1 rounded-lg border p-3 text-sm
                     {config.providers?.backend === option.id
                ? 'border-brand bg-panel-2'
                : 'border-line'}"
            >
              <span class="flex items-center gap-2 font-medium">
                <input
                  type="radio"
                  name="backend"
                  value={option.id}
                  checked={config.providers?.backend === option.id}
                  onchange={() => {
                    if (config?.providers) config.providers.backend = option.id;
                  }}
                />
                {option.name}
              </span>
              <span class="text-xs text-ink-dim">{option.desc}</span>
            </label>
          {/each}
        </div>

        {#if config.providers?.backend === "openrouter" && config.providers.openrouter}
          {@const or = config.providers.openrouter}
          <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {@render textField(i18n.m.settings.baseUrl, () => or.baseUrl ?? "", (v) => (or.baseUrl = v))}
            {@render textField(i18n.m.settings.sttModel, () => or.sttModel ?? "", (v) => (or.sttModel = v))}
            {@render textField(i18n.m.settings.mtModel, () => or.mtModel ?? "", (v) => (or.mtModel = v))}
            {@render textField(i18n.m.settings.ttsModel, () => or.ttsModel ?? "", (v) => (or.ttsModel = v))}
            {@render numberField(i18n.m.settings.temperature, () => or.mtTemperature ?? 0, (v) => (or.mtTemperature = v))}
            {@render numberField(i18n.m.settings.eurPerUsd, () => or.eurPerUsd ?? 1, (v) => (or.eurPerUsd = v))}
            {@render numberField(i18n.m.settings.timeout, () => or.timeoutSeconds ?? 30, (v) => (or.timeoutSeconds = v))}
          </div>
          {@render keyRow("openrouter", or.keySet ?? false)}
        {/if}

        {#if config.providers?.backend === "local" && config.providers.local}
          {@const lo = config.providers.local}
          <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {@render textField(i18n.m.settings.baseUrl, () => lo.baseUrl ?? "", (v) => (lo.baseUrl = v))}
            {@render textField(i18n.m.settings.sttBaseUrl, () => lo.sttBaseUrl ?? "", (v) => (lo.sttBaseUrl = v))}
            {@render textField(i18n.m.settings.sttModel, () => lo.sttModel ?? "", (v) => (lo.sttModel = v))}
            {@render textField(i18n.m.settings.mtBaseUrl, () => lo.mtBaseUrl ?? "", (v) => (lo.mtBaseUrl = v))}
            {@render textField(i18n.m.settings.mtModel, () => lo.mtModel ?? "", (v) => (lo.mtModel = v))}
            {@render numberField(i18n.m.settings.temperature, () => lo.mtTemperature ?? 0, (v) => (lo.mtTemperature = v))}
            {@render textField(i18n.m.settings.ttsBaseUrl, () => lo.ttsBaseUrl ?? "", (v) => (lo.ttsBaseUrl = v))}
            {@render textField(i18n.m.settings.ttsModel, () => lo.ttsModel ?? "", (v) => (lo.ttsModel = v))}
            {@render textField(i18n.m.settings.ttsVoice, () => lo.ttsVoice ?? "", (v) => (lo.ttsVoice = v))}
            {@render numberField(i18n.m.settings.timeout, () => lo.timeoutSeconds ?? 120, (v) => (lo.timeoutSeconds = v))}
          </div>
        {/if}
      </fieldset>

      <!-- Voice adaptation -->
      <fieldset class="flex flex-col gap-3">
        <legend class="mb-2 text-sm font-semibold">{i18n.m.settings.clone}</legend>

        <div class="grid gap-3 sm:grid-cols-3">
          {#each [{ id: "off", name: i18n.m.settings.cloneOff, desc: i18n.m.settings.cloneOffDesc }, { id: "pitch", name: i18n.m.settings.clonePitch, desc: i18n.m.settings.clonePitchDesc }, { id: "cartesia", name: i18n.m.settings.cloneCartesia, desc: i18n.m.settings.cloneCartesiaDesc }] as option (option.id)}
            <label
              class="flex cursor-pointer flex-col gap-1 rounded-lg border p-3 text-sm
                     {config.providers?.clone === option.id
                ? 'border-brand bg-panel-2'
                : 'border-line'}"
            >
              <span class="flex items-center gap-2 font-medium">
                <input
                  type="radio"
                  name="clone"
                  value={option.id}
                  checked={config.providers?.clone === option.id}
                  onchange={() => {
                    if (config?.providers) config.providers.clone = option.id;
                  }}
                />
                {option.name}
              </span>
              <span class="text-xs text-ink-dim">{option.desc}</span>
            </label>
          {/each}
        </div>

        {#if config.providers?.clone === "cartesia" && config.providers.cartesia}
          {@const car = config.providers.cartesia}
          <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {@render textField(i18n.m.settings.baseUrl, () => car.baseUrl ?? "", (v) => (car.baseUrl = v))}
            {@render textField(i18n.m.settings.model, () => car.model ?? "", (v) => (car.model = v))}
            {@render numberField(i18n.m.settings.timeout, () => car.timeoutSeconds ?? 30, (v) => (car.timeoutSeconds = v))}
          </div>
          {@render keyRow("cartesia", car.keySet ?? false)}
        {/if}
      </fieldset>

      <!-- Session defaults -->
      {#if config.defaults}
        {@const d = config.defaults}
        <fieldset class="flex flex-col gap-3">
          <legend class="mb-2 text-sm font-semibold">{i18n.m.settings.defaults}</legend>

          <div class="flex flex-col gap-1 text-sm">
            <span class="text-ink-dim">{i18n.m.settings.defaultLangs}</span>
            <div class="flex flex-wrap gap-2">
              {#each languages as lang (lang.tag)}
                <button
                  type="button"
                  onclick={() => toggleLang(lang.tag)}
                  class="rounded-full border px-3 py-1 text-xs
                         {d.langs?.includes(lang.tag)
                    ? 'border-brand bg-panel-2 text-ink'
                    : 'border-line text-ink-dim'}"
                >
                  {lang.label}
                </button>
              {/each}
            </div>
          </div>

          <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-ink-dim">{i18n.m.settings.subs}</span>
              <select
                value={d.subs ?? "vtt"}
                onchange={(e) => (d.subs = e.currentTarget.value)}
                class="rounded-lg border border-line bg-panel-2 px-3 py-2
                       outline-none focus:border-brand"
              >
                <option value="vtt">{i18n.m.settings.subsVtt}</option>
                <option value="off">{i18n.m.settings.subsOff}</option>
                <option value="burn">{i18n.m.settings.subsBurn}</option>
              </select>
            </label>
            {@render textField(i18n.m.settings.bed, () => d.bed ?? "", (v) => (d.bed = v))}
            {@render numberField(i18n.m.settings.delay, () => d.delaySeconds ?? 8, (v) => (d.delaySeconds = v), "1")}
          </div>
        </fieldset>
      {/if}

      <!-- Budgets & privacy -->
      <div class="grid gap-6 sm:grid-cols-2">
        {#if config.budgets}
          {@const b = config.budgets}
          <fieldset class="flex flex-col gap-3">
            <legend class="mb-2 text-sm font-semibold">{i18n.m.settings.budgets}</legend>
            {@render numberField(i18n.m.settings.budgetPerSession, () => b.perSessionEurH ?? 0, (v) => (b.perSessionEurH = v))}
            <label class="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={b.hardStop ?? false}
                onchange={(e) => (b.hardStop = e.currentTarget.checked)}
                class="mt-1"
              />
              <span>
                {i18n.m.settings.hardStop}
                <span class="block text-xs text-ink-dim">{i18n.m.settings.hardStopDesc}</span>
              </span>
            </label>
          </fieldset>
        {/if}

        {#if config.privacy}
          {@const p = config.privacy}
          <fieldset class="flex flex-col gap-3">
            <legend class="mb-2 text-sm font-semibold">{i18n.m.settings.privacy}</legend>
            {@render numberField(i18n.m.settings.transcripts, () => p.storeTranscriptsHours ?? 24, (v) => (p.storeTranscriptsHours = v), "1")}
            <label class="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={p.storeAudio ?? false}
                onchange={(e) => (p.storeAudio = e.currentTarget.checked)}
                class="mt-1"
              />
              <span>
                {i18n.m.settings.storeAudio}
                <span class="block text-xs text-ink-dim">{i18n.m.settings.storeAudioDesc}</span>
              </span>
            </label>
          </fieldset>
        {/if}
      </div>

      <div class="flex flex-wrap items-center gap-3">
        <button
          type="submit"
          disabled={busy}
          class="rounded-lg bg-brand px-4 py-2 font-medium text-white
                 hover:opacity-90 disabled:opacity-40"
        >
          {i18n.m.settings.save}
        </button>
        {#if notice}
          <p class="text-sm text-ok">{notice}</p>
        {/if}
        {#if error}
          <p class="text-sm text-danger">{error}</p>
        {/if}
      </div>

      {#if restartNotes.length > 0}
        <p class="text-xs text-warn">
          {i18n.m.settings.restartNote}
          {restartNotes.join(", ")}
        </p>
      {/if}
    </form>
  {/if}
</section>
