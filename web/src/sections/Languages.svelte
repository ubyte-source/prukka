<script lang="ts">
  import { installEnginePack, removeEnginePack } from "../lib/api/client";
  import {
    coreInstalled,
    languagePlans,
    mib,
    operationBusy,
    totalSizeBytes,
    type LanguagePlan,
    type LanguageState,
  } from "../lib/enginePacks";
  import DownloadProgress from "../lib/components/DownloadProgress.svelte";
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
  import { ensureEngineCore } from "../lib/state/engineInstall.svelte";
  import { liveEngineProgress, type EngineProgress } from "../lib/state/engineProgress";
  import { toasts } from "../lib/state/toasts.svelte";
  import { isControlToken, token } from "../lib/state/token.svelte";

  const engine = $derived(daemon.engine);
  const packs = $derived(engine?.packs ?? []);
  const sttCore = $derived(packs.find((pack) => pack.id === "stt-core"));
  const coreReady = $derived(coreInstalled(engine));
  const coreState = $derived<LanguageState>(
    coreReady
      ? "installed"
      : (engine?.installed ?? false) || (sttCore?.installed ?? false)
        ? "partial"
        : "available",
  );
  const catalogAvailable = $derived((engine?.catalogError ?? "") === "");
  const plans = $derived(engine === null ? [] : languagePlans(engine));
  const installedCount = $derived(plans.filter((plan) => plan.state === "installed").length);
  const daemonBusy = $derived(operationBusy(engine?.operation));

  let busy = $state(false);
  let activeTag = $state("");
  let activeMode = $state<"install" | "remove" | "">("");
  let inFlight: AbortController | null = null;
  const controlToken = $derived(isControlToken(token.value) ? token.value : "");
  const anyBusy = $derived(busy || daemonBusy);

  $effect(() => {
    // A token change invalidates any plan running under the previous one.
    void controlToken;
    return () => {
      inFlight?.abort();
      inFlight = null;
      busy = false;
      activeTag = "";
      activeMode = "";
    };
  });

  const progress = $derived(liveEngineProgress());

  // The row that owns the progress bar: the clicked row while a local plan
  // runs, otherwise the row whose pack set contains the operation's pack.
  const progressTag = $derived.by(() => {
    if (progress === null) return "";
    if (busy && activeTag !== "") return activeTag;
    const packId = progress.packId;
    if (packId === "" || packId === "stt-core" || progress.kind === "install-runtime") {
      return "core";
    }
    const owner = plans.find((plan) =>
      plan.required.some((pack) => pack.id === packId)
      || plan.removable.some((pack) => pack.id === packId)
    );
    return owner?.tag ?? "core";
  });

  const stateLabels = $derived<Record<LanguageState, string>>({
    installed: i18n.m.languages.installed,
    partial: i18n.m.languages.partial,
    available: i18n.m.languages.available,
  });

  const badge: Record<LanguageState, string> = {
    installed: "bg-ok/15 text-ok",
    partial: "bg-warn/15 text-warn",
    available: "bg-panel text-ink-dim",
  };

  function languageLabel(tag: string): string {
    return daemon.languages.find((language) => language.tag === tag)?.label ?? tag;
  }

  function sizeMiB(bytes: number): string {
    return i18n.m.languages.sizeMiB.replace("{size}", mib(bytes));
  }

  function planSize(plan: LanguagePlan): string {
    return sizeMiB(totalSizeBytes(plan.missing.length > 0 ? plan.missing : plan.required));
  }

  function phaseLabel(phase: EngineProgress["phase"]): string {
    if (phase === "download") return i18n.m.languages.phaseDownload;
    if (phase === "verify") return i18n.m.languages.phaseVerify;
    return i18n.m.languages.phaseInstall;
  }

  function sizeLine(view: EngineProgress): string {
    if (view.total <= 0) return "";
    return `${mib(view.done)} / ${sizeMiB(view.total)}`;
  }

  function percentOf(view: EngineProgress): number | null {
    if (view.total <= 0) return null;
    return Math.min(100, Math.round((view.done / view.total) * 100));
  }

  function isLastLanguage(plan: LanguagePlan): boolean {
    return plan.state === "installed" && installedCount <= 1;
  }

  // Shared plan scaffolding: one in-flight controller, a worded failure toast
  // and a final engine refresh, mirroring the wizard's submission discipline.
  async function withPlan(
    tag: string,
    mode: "install" | "remove",
    run: (signal: AbortSignal, tokenSnapshot: string) => Promise<void>,
  ) {
    const tokenSnapshot = controlToken;
    if (busy || daemonBusy || tokenSnapshot === "") return;
    const controller = new AbortController();
    inFlight?.abort();
    inFlight = controller;
    busy = true;
    activeTag = tag;
    activeMode = mode;
    try {
      await run(controller.signal, tokenSnapshot);
    } catch (e) {
      if (!controller.signal.aborted) toasts.failure(e, i18n.m.languages.actionFailed);
    } finally {
      if (inFlight === controller) inFlight = null;
      if (!controller.signal.aborted) {
        busy = false;
        activeTag = "";
        activeMode = "";
        void daemon.refreshEngine();
      }
    }
  }

  // The daemon runs one operation at a time: POST a pack, wait for its
  // terminal event (or the fallback poll), then start the next one.
  async function installSequence(ids: string[], tokenSnapshot: string, signal: AbortSignal) {
    for (const id of ids) {
      if (signal.aborted) return;
      daemon.setEngine(await installEnginePack(id, tokenSnapshot));
      await daemon.waitForEngineIdle(signal);
    }
  }

  function installCore() {
    void withPlan("core", "install", (signal, tokenSnapshot) =>
      ensureEngineCore(tokenSnapshot, signal));
  }

  function installLanguage(plan: LanguagePlan) {
    const ids = plan.missing.map((pack) => pack.id);
    void withPlan(plan.tag, "install", (signal, tokenSnapshot) =>
      installSequence(ids, tokenSnapshot, signal));
  }

  function removeLanguage(plan: LanguagePlan) {
    const ids = plan.removable.map((pack) => pack.id);
    void withPlan(plan.tag, "remove", async (signal, tokenSnapshot) => {
      for (const id of ids) {
        if (signal.aborted) return;
        // Removal is synchronous: each reply is already the next snapshot.
        daemon.setEngine(await removeEnginePack(id, tokenSnapshot));
      }
      await daemon.refreshConfig();
    });
  }
</script>

{#snippet progressBar(view: EngineProgress)}
  <DownloadProgress
    pct={percentOf(view)}
    phaseLabel={phaseLabel(view.phase)}
    sizeLine={sizeLine(view)}
  />
{/snippet}

{#if daemon.engineSupported}
  <section
    aria-labelledby="languages-title"
    aria-busy={anyBusy}
    class="rounded-2xl border border-hairline bg-panel p-6 shadow-card"
  >
    <h2 id="languages-title" class="mb-4 text-lg font-semibold tracking-tight">{i18n.m.languages.title}</h2>

    {#if !isControlToken(token.value)}
      <p class="text-sm text-ink-dim">{i18n.m.languages.tokenRequired}</p>
    {:else if daemon.engineError}
      <div role="alert" class="flex flex-wrap items-center gap-3 text-sm text-danger">
        <p>{i18n.m.languages.loadFailed}</p>
        <button
          type="button"
          onclick={() => daemon.retryEngine()}
          class="rounded-full border border-line px-3.5 py-1.5 text-ink hover:border-brand"
        >
          {i18n.m.languages.retry}
        </button>
      </div>
    {:else if !daemon.engineLoaded || engine === null}
      <p class="text-sm text-ink-dim" role="status">{i18n.m.languages.loading}</p>
    {:else}
      <p class="mb-4 max-w-3xl text-sm text-ink-dim">{i18n.m.languages.lead}</p>

      {#if !catalogAvailable}
        <div role="alert" class="mb-4 flex flex-wrap items-center gap-3 text-sm text-warn">
          <p>{i18n.m.languages.catalogUnreachable}</p>
          <button
            type="button"
            onclick={() => void daemon.refreshEngine()}
            class="rounded-full border border-line px-3.5 py-1.5 text-ink hover:border-brand"
          >
            {i18n.m.languages.retry}
          </button>
        </div>
      {/if}

      <ul class="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <li class="rounded-xl border border-hairline bg-panel-2 p-3 lg:col-span-2">
          <div class="flex flex-wrap items-center gap-3">
            <div class="min-w-0 flex-1">
              <p class="text-sm font-medium">{i18n.m.languages.engineCore}</p>
              {#if sttCore?.sizeBytes}
                <p class="text-xs text-ink-dim">{sizeMiB(Number(sttCore.sizeBytes))}</p>
              {/if}
            </div>
            <span class={`rounded px-1.5 py-0.5 font-mono text-xs ${badge[coreState]}`}>
              {coreReady ? i18n.m.languages.ready : stateLabels[coreState]}
            </span>
            {#if !coreReady && catalogAvailable}
              <button
                type="button"
                disabled={anyBusy}
                onclick={installCore}
                class="rounded-full bg-brand-dim px-5 py-2 text-sm font-medium text-white
                       hover:brightness-110 disabled:opacity-40"
              >
                {busy && activeTag === "core" ? i18n.m.languages.installing : i18n.m.languages.install}
              </button>
            {/if}
          </div>
          {#if progressTag === "core" && progress !== null}
            {@render progressBar(progress)}
          {/if}
        </li>

        {#each plans as plan (plan.tag)}
          {@const lastLanguage = isLastLanguage(plan)}
          <li class="rounded-xl border border-hairline bg-panel-2 p-3">
            <div class="flex flex-wrap items-center gap-3">
              <div class="min-w-0 flex-1">
                <p class="text-sm font-medium">{languageLabel(plan.tag)}</p>
                <p class="text-xs text-ink-dim">{planSize(plan)}</p>
              </div>
              <span class={`rounded px-1.5 py-0.5 font-mono text-xs ${badge[plan.state]}`}>
                {stateLabels[plan.state]}
              </span>
              {#if plan.missing.length > 0 && catalogAvailable}
                <button
                  type="button"
                  disabled={anyBusy || !coreReady}
                  onclick={() => installLanguage(plan)}
                  class="rounded-full bg-brand-dim px-5 py-2 text-sm font-medium text-white
                         hover:brightness-110 disabled:opacity-40"
                >
                  {busy && activeTag === plan.tag && activeMode === "install"
                    ? i18n.m.languages.installing
                    : i18n.m.languages.install}
                </button>
              {/if}
              {#if plan.removable.length > 0}
                <button
                  type="button"
                  disabled={anyBusy || lastLanguage}
                  onclick={() => removeLanguage(plan)}
                  class="rounded-full border border-line px-3.5 py-1.5 text-sm text-ink
                         hover:border-brand disabled:opacity-40"
                >
                  {busy && activeTag === plan.tag && activeMode === "remove"
                    ? i18n.m.languages.removing
                    : i18n.m.languages.remove}
                </button>
              {/if}
            </div>
            {#if lastLanguage && plan.removable.length > 0}
              <p class="mt-1 text-xs text-ink-dim">{i18n.m.languages.lastLanguageHint}</p>
            {/if}
            {#if progressTag === plan.tag && progress !== null}
              {@render progressBar(progress)}
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
{/if}
