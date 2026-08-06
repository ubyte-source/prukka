<script lang="ts">
  // The one-click "add this language" control used everywhere a chosen
  // language is missing: an add button that becomes a detailed download bar,
  // or a plain, recoverable error (the daemon's reason goes to a toast). All
  // state comes from the single-flight engineInstall controller. The owner
  // renders this component only while the language is missing and gives the
  // success feedback itself (a selected chip, a confirmation line).

  import type { LanguagePlan } from "../enginePacks";
  import { mb, operationBusy } from "../enginePacks";
  import { i18n } from "../i18n/index.svelte";
  import { daemon } from "../state/daemon.svelte";
  import { engineInstall } from "../state/engineInstall.svelte";
  import { toasts } from "../state/toasts.svelte";
  import { isControlToken, token } from "../state/token.svelte";
  import DownloadProgress from "./DownloadProgress.svelte";

  interface Props {
    /** tag is the registry language to add. */
    tag: string;
    /** plan is its catalog plan (from planForLanguage). */
    plan: LanguagePlan;
    /** needVoice asks for a dubbed voice (vs captions-only routes). */
    needVoice: boolean;
    /** addBytes is the download size to show on the button. */
    addBytes: number;
    /** onAdded fires once the language is installed, so the caller can select it. */
    onAdded?: (tag: string) => void;
    /** onCancel fires when the operator abandons the download, so an owner
     *  that put this panel on screen can take it back down. */
    onCancel?: () => void;
  }

  const { tag, plan, needVoice, addBytes, onAdded, onCancel }: Props = $props();

  const controlToken = $derived(isControlToken(token.value) ? token.value : "");
  const mine = $derived(engineInstall.tag === tag && engineInstall.phase !== "idle");
  const catalogError = $derived((daemon.engine?.catalogError ?? "") !== "");
  // Busy covers this dashboard's controller AND an operation another client
  // started (visible only on the wire), matching Languages.svelte's
  // busy || daemonBusy gate — the daemon runs one engine operation at a time.
  const daemonBusy = $derived(operationBusy(daemon.engine?.operation));

  function label(): string {
    return daemon.languages.find((language) => language.tag === tag)?.label ?? tag;
  }

  function fill(text: string): string {
    return text.replace("{language}", label());
  }

  function phaseLabel(): string {
    if (engineInstall.phase === "preparing") return i18n.m.add.preparing;
    const phase = engineInstall.livePhase();
    if (phase === "verify") return i18n.m.add.phaseVerify;
    if (phase === "install") return i18n.m.add.phaseInstall;
    return i18n.m.add.phaseDownload;
  }

  function sizeLine(): string {
    const total = engineInstall.totalBytesNow();
    if (total <= 0) return "";
    return i18n.m.add.size
      .replace("{done}", mb(engineInstall.doneBytesNow()))
      .replace("{total}", mb(total));
  }

  function stepLine(): string {
    if (engineInstall.stepCount <= 1) return "";
    return i18n.m.add.step
      .replace("{index}", String(engineInstall.stepIndex))
      .replace("{count}", String(engineInstall.stepCount));
  }

  function start() {
    if (controlToken === "" || engineInstall.busy || daemonBusy) return;
    void engineInstall.start(
      tag,
      plan,
      needVoice,
      controlToken,
      () => onAdded?.(tag),
      // The toast carries the daemon's reason (checksum, disk, 400…) the way
      // every other section reports failures; the inline line below stays the
      // plain-language recovery affordance.
      (err) => toasts.failure(err, fill(i18n.m.add.errorTitle)),
    );
  }
</script>

{#if !daemon.engineSupported}
  <p class="text-xs text-ink-dim">{i18n.m.add.unsupported}</p>
{:else if catalogError}
  <div class="flex flex-wrap items-center gap-2 text-xs text-warn">
    <span>{i18n.m.add.catalogError}</span>
    <button
      type="button"
      onclick={() => void daemon.refreshEngine()}
      class="rounded-lg border border-line px-2 py-1 text-ink hover:border-brand"
    >
      {i18n.m.add.retry}
    </button>
  </div>
{:else if mine && engineInstall.phase === "error"}
  <div class="flex flex-col gap-1">
    <p class="text-xs text-danger">{fill(i18n.m.add.errorTitle)}</p>
    <button
      type="button"
      onclick={start}
      class="self-start rounded-lg border border-line px-2 py-1 text-xs text-ink hover:border-brand"
    >
      {i18n.m.add.tryAgain}
    </button>
  </div>
{:else if mine}
  <div class="flex flex-col gap-1">
    <DownloadProgress
      pct={engineInstall.overallPct()}
      phaseLabel={phaseLabel()}
      sizeLine={sizeLine()}
      stepLine={stepLine()}
    />
    <button
      type="button"
      onclick={() => {
        engineInstall.cancel();
        onCancel?.();
      }}
      class="self-start text-xs text-ink-dim underline hover:text-ink"
    >
      {i18n.m.add.cancel}
    </button>
  </div>
{:else if controlToken === ""}
  <p class="text-xs text-ink-dim">{i18n.m.add.connect}</p>
{:else if engineInstall.busy || daemonBusy}
  <button
    type="button"
    disabled
    class="rounded-full border border-line px-3.5 py-1.5 text-xs text-ink-dim opacity-60"
  >
    {i18n.m.add.busyOther}
  </button>
{:else}
  <div class="flex flex-col gap-0.5">
    <button
      type="button"
      onclick={start}
      class="self-start rounded-lg bg-brand-dim px-3 py-1.5 text-xs font-medium text-white
             hover:brightness-110"
    >
      {i18n.m.add.button.replace("{language}", label()).replace("{size}", mb(addBytes))}
    </button>
    <span class="text-xs text-ink-dim">{fill(i18n.m.add.sub)}</span>
  </div>
{/if}
