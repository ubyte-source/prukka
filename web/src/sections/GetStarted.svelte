<script lang="ts">
  // First-run hero: the engine core install as its own entry point, because
  // every language add has to run it first anyway (engineInstall.start).

  import { coreInstalled, mib, operationBusy } from "../lib/enginePacks";
  import { primaryButton } from "../lib/components/classes";
  import DownloadProgress from "../lib/components/DownloadProgress.svelte";
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
  import { ensureEngineCore } from "../lib/state/engineInstall.svelte";
  import { liveEngineProgress, type EngineProgress } from "../lib/state/engineProgress";
  import { toasts } from "../lib/state/toasts.svelte";
  import { isControlToken, token } from "../lib/state/token.svelte";

  const engine = $derived(daemon.engine);
  const controlToken = $derived(isControlToken(token.value) ? token.value : "");
  const visible = $derived(
    controlToken !== ""
      && daemon.engineSupported
      && !daemon.engineError
      && engine !== null
      && !coreInstalled(engine),
  );
  const catalogAvailable = $derived((engine?.catalogError ?? "") === "");
  const sttCore = $derived(engine?.packs?.find((pack) => pack.id === "stt-core"));

  let busy = $state(false);
  let inFlight: AbortController | null = null;
  const anyBusy = $derived(busy || operationBusy(engine?.operation));
  // The live progress belongs here only while the core prerequisites run.
  const progress = $derived.by<EngineProgress | null>(() => {
    if (!anyBusy) return null;
    const live = liveEngineProgress();
    if (live === null) return null;
    if (live.kind !== "install-runtime" && live.packId !== "stt-core") return null;
    return live;
  });

  $effect(() => {
    // A token change invalidates an install running under the previous one.
    void controlToken;
    return () => {
      inFlight?.abort();
      inFlight = null;
      busy = false;
    };
  });

  function lead(): string {
    // An unreachable catalog leaves packs empty, so there is no size to quote.
    const size = mib(Number(sttCore?.sizeBytes ?? "0"));
    if (size === "0") return i18n.m.getStarted.leadUnsized;
    return i18n.m.getStarted.lead.replace("{size}", size);
  }

  // Mirrors Languages.phaseLabel: both read the languages namespace.
  function phaseLabel(phase: EngineProgress["phase"]): string {
    if (phase === "download") return i18n.m.languages.phaseDownload;
    if (phase === "verify") return i18n.m.languages.phaseVerify;
    return i18n.m.languages.phaseInstall;
  }

  function install() {
    const tokenSnapshot = controlToken;
    if (anyBusy || tokenSnapshot === "" || !catalogAvailable) return;
    const controller = new AbortController();
    inFlight?.abort();
    inFlight = controller;
    busy = true;
    void ensureEngineCore(tokenSnapshot, controller.signal)
      .catch((e: unknown) => {
        if (!controller.signal.aborted) toasts.failure(e, i18n.m.languages.actionFailed);
      })
      .finally(() => {
        if (inFlight === controller) inFlight = null;
        if (!controller.signal.aborted) {
          busy = false;
          void daemon.refreshEngine();
        }
      });
  }
</script>

{#if visible}
  <section
    aria-labelledby="get-started-title"
    aria-busy={anyBusy}
    class="rounded-2xl border border-brand/30 bg-brand/5 p-6 shadow-card sm:p-7"
  >
    <div class="flex flex-wrap items-start gap-4">
      <span
        aria-hidden="true"
        class="flex h-11 w-11 shrink-0 items-center justify-center rounded-full bg-brand/15 text-brand"
      >
        <svg viewBox="0 0 20 20" fill="none" class="size-5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
          <path d="M10 3v9m0 0 3.5-3.5M10 12 6.5 8.5M4 15.5h12" />
        </svg>
      </span>
      <div class="min-w-0 flex-1">
        <h2 id="get-started-title" class="text-lg font-semibold tracking-tight">
          {i18n.m.getStarted.title}
        </h2>
        <p class="mt-1 max-w-2xl text-sm leading-relaxed text-ink-dim">{lead()}</p>
        {#if !catalogAvailable}
          <div role="alert" class="mt-3 flex flex-wrap items-center gap-3 text-sm text-warn">
            <p>{i18n.m.languages.catalogUnreachable}</p>
            <button
              type="button"
              onclick={() => void daemon.refreshEngine()}
              class="rounded-full border border-line px-3.5 py-1.5 text-ink hover:border-brand"
            >
              {i18n.m.languages.retry}
            </button>
          </div>
        {:else if progress !== null}
          <div class="max-w-md">
            <DownloadProgress
              pct={progress.total > 0
                ? Math.min(100, Math.round((progress.done / progress.total) * 100))
                : null}
              phaseLabel={phaseLabel(progress.phase)}
              sizeLine={progress.total > 0 ? `${mib(progress.done)} / ${mib(progress.total)} MiB` : ""}
            />
          </div>
        {:else}
          <button
            type="button"
            disabled={anyBusy}
            onclick={install}
            class={`mt-4 ${primaryButton}`}
          >
            {anyBusy ? i18n.m.getStarted.installing : i18n.m.getStarted.install}
          </button>
        {/if}
      </div>
    </div>
  </section>
{/if}
