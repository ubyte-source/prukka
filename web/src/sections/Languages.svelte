<script lang="ts">
  import { installEnginePack, installEngineRuntime, removeEnginePack } from "../lib/api/client";
  import type { EnginePhase } from "../lib/api/types";
  import {
    languagePlans,
    mib,
    operationBusy,
    totalSizeBytes,
    type LanguagePlan,
    type LanguageState,
  } from "../lib/enginePacks";
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
  import { toasts } from "../lib/state/toasts.svelte";
  import { isControlToken, token } from "../lib/state/token.svelte";

  interface ProgressView {
    packId: string;
    kind: string;
    phase: Exclude<EnginePhase, "done" | "error">;
    done: number;
    total: number;
  }

  const engine = $derived(daemon.engine);
  const packs = $derived(engine?.packs ?? []);
  const sttCore = $derived(packs.find((pack) => pack.id === "stt-core"));
  const coreInstalled = $derived((engine?.installed ?? false) && (sttCore?.installed ?? false));
  const coreState = $derived<LanguageState>(
    coreInstalled
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

  // The freshest live progress: the SSE frame (number counters) wins over the
  // string-typed REST operation the 2s fallback poll refreshes.
  const progress = $derived.by<ProgressView | null>(() => {
    const event = daemon.engineEvent;
    if (event !== null && event.phase !== "done" && event.phase !== "error") {
      return {
        packId: event.packId ?? "",
        kind: event.kind,
        phase: event.phase,
        done: event.doneBytes ?? 0,
        total: event.totalBytes ?? 0,
      };
    }
    const operation = engine?.operation;
    if (operation !== undefined && operation.phase !== "done" && operation.phase !== "error") {
      return {
        packId: operation.packId ?? "",
        kind: operation.kind,
        phase: operation.phase,
        done: Number(operation.doneBytes ?? "0"),
        total: Number(operation.totalBytes ?? "0"),
      };
    }
    return null;
  });

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

  function phaseLabel(phase: ProgressView["phase"]): string {
    if (phase === "download") return i18n.m.languages.phaseDownload;
    if (phase === "verify") return i18n.m.languages.phaseVerify;
    return i18n.m.languages.phaseInstall;
  }

  function progressText(view: ProgressView): string {
    const label = phaseLabel(view.phase);
    if (view.total <= 0) return label;
    return `${label} — ${mib(view.done)} / ${sizeMiB(view.total)}`;
  }

  function percentOf(view: ProgressView): number | null {
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
    void withPlan("core", "install", async (signal, tokenSnapshot) => {
      if (!(daemon.engine?.installed ?? false)) {
        daemon.setEngine(await installEngineRuntime(tokenSnapshot));
        await daemon.waitForEngineIdle(signal);
      }
      const core = daemon.engine?.packs?.find((pack) => pack.id === "stt-core");
      if (core !== undefined && !(core.installed ?? false)) {
        await installSequence(["stt-core"], tokenSnapshot, signal);
      }
    });
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

{#snippet progressBar(view: ProgressView)}
  {@const percent = percentOf(view)}
  <div class="mt-3 flex flex-col gap-1">
    <p class="text-xs text-ink-dim" aria-live="polite">{progressText(view)}</p>
    <div
      role="progressbar"
      aria-label={phaseLabel(view.phase)}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent ?? undefined}
      class="h-2 overflow-hidden rounded-full bg-panel"
    >
      <div
        class="h-full rounded-full bg-brand-dim"
        class:animate-pulse={percent === null}
        style="width: {percent ?? 100}%"
      ></div>
    </div>
  </div>
{/snippet}

{#if daemon.engineSupported}
  <section
    aria-labelledby="languages-title"
    aria-busy={anyBusy}
    class="rounded-xl border border-line bg-panel p-5"
  >
    <h2 id="languages-title" class="mb-4 text-base font-semibold">{i18n.m.languages.title}</h2>

    {#if !isControlToken(token.value)}
      <p class="text-sm text-ink-dim">{i18n.m.languages.tokenRequired}</p>
    {:else if daemon.engineError}
      <div role="alert" class="flex flex-wrap items-center gap-3 text-sm text-danger">
        <p>{i18n.m.languages.loadFailed}</p>
        <button
          type="button"
          onclick={() => daemon.retryEngine()}
          class="rounded-lg border border-line px-3 py-1.5 text-ink hover:border-brand"
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
            class="rounded-lg border border-line px-3 py-1.5 text-ink hover:border-brand"
          >
            {i18n.m.languages.retry}
          </button>
        </div>
      {/if}

      <ul class="flex flex-col gap-3">
        <li class="rounded-lg border border-line bg-panel-2 p-3">
          <div class="flex flex-wrap items-center gap-3">
            <div class="min-w-0 flex-1">
              <p class="text-sm font-medium">{i18n.m.languages.engineCore}</p>
              {#if sttCore?.sizeBytes}
                <p class="text-xs text-ink-dim">{sizeMiB(Number(sttCore.sizeBytes))}</p>
              {/if}
            </div>
            <span class={`rounded px-1.5 py-0.5 font-mono text-xs ${badge[coreState]}`}>
              {coreInstalled ? i18n.m.languages.ready : stateLabels[coreState]}
            </span>
            {#if !coreInstalled && catalogAvailable}
              <button
                type="button"
                disabled={anyBusy}
                onclick={installCore}
                class="rounded-lg bg-brand-dim px-4 py-2 text-sm font-medium text-white
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
          <li class="rounded-lg border border-line bg-panel-2 p-3">
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
                  disabled={anyBusy || !coreInstalled}
                  onclick={() => installLanguage(plan)}
                  class="rounded-lg bg-brand-dim px-4 py-2 text-sm font-medium text-white
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
                  class="rounded-lg border border-line px-3 py-1.5 text-sm text-ink
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
