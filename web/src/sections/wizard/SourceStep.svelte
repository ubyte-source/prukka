<script lang="ts">
  import { inputClasses, primaryButton, secondaryButton } from "../../lib/components/classes";
  import Select from "../../lib/components/Select.svelte";
  import { i18n } from "../../lib/i18n/index.svelte";
  import { daemon } from "../../lib/state/daemon.svelte";
  import { token } from "../../lib/state/token.svelte";
  import type { SourceKind } from "../../lib/wizard/model";
  import type { WizardState } from "../../lib/wizard/state.svelte";

  interface Props {
    wizard: WizardState;
  }

  const { wizard }: Props = $props();

  interface SourceCard {
    kind: SourceKind;
    title: string;
    desc: string;
    /** available gates the card on discovered hardware; manual-URL kinds stay available. */
    available: boolean;
  }

  const sourceCards = $derived<SourceCard[]>([
    {
      kind: "mic",
      title: i18n.m.wizard.sourceMic,
      desc: i18n.m.wizard.sourceMicDesc,
      available: wizard.captureDevices.length > 0,
    },
    {
      kind: "camera",
      title: i18n.m.wizard.sourceCamera,
      desc: i18n.m.wizard.sourceCameraDesc,
      available: wizard.cameraDevices.length > 0,
    },
    {
      kind: "call",
      title: i18n.m.wizard.sourceCall,
      desc: i18n.m.wizard.sourceCallDesc,
      available: true,
    },
    {
      kind: "stream",
      title: i18n.m.wizard.sourceStream,
      desc: i18n.m.wizard.sourceStreamDesc,
      available: true,
    },
  ]);
</script>

<div class="step-enter flex flex-col gap-4">
  <h3 tabindex="-1" class="text-base font-medium text-ink">
    {i18n.m.wizard.chooseUseCase}
  </h3>
  {#if !wizard.connected}
    <p class="text-sm text-ink-dim">{i18n.m.wizard.connectFirst}</p>
  {:else if wizard.setupFailed}
    <div role="alert" class="flex flex-wrap items-center gap-3 text-sm text-danger">
      <p>{i18n.m.wizard.unavailable}</p>
      <button
        type="button"
        onclick={() => daemon.reloadSetup(token.value)}
        class={secondaryButton}
      >
        {i18n.m.wizard.retry}
      </button>
    </div>
  {:else if !wizard.ready}
    <p class="text-sm text-ink-dim" role="status">{i18n.m.wizard.loading}</p>
  {/if}
  {#if daemon.devicesError}
    <p role="status" class="rounded-xl border border-hairline bg-panel-2 p-3 text-sm text-warn">
      {i18n.m.wizard.devicesUnavailable}
    </p>
  {/if}

  <div
    class="grid grid-cols-1 gap-3 sm:grid-cols-2"
    role="group"
    aria-label={i18n.m.wizard.chooseUseCase}
  >
    {#each sourceCards as card (card.kind)}
      <button
        type="button"
        disabled={!wizard.ready || !card.available}
        aria-pressed={wizard.draft.sourceKind === card.kind}
        onclick={() => wizard.chooseSource(card.kind)}
        class={`group rounded-xl border p-4 text-left transition-colors duration-150
                disabled:cursor-not-allowed disabled:opacity-45 ${
                  wizard.draft.sourceKind === card.kind
                    ? "border-brand bg-brand/10"
                    : "border-line bg-panel-2 hover:border-brand"
                }`}
      >
        <span class="flex items-center gap-3">
          <span
            aria-hidden="true"
            class={`flex h-9 w-9 shrink-0 items-center justify-center rounded-full ${
              wizard.draft.sourceKind === card.kind ? "bg-brand/20 text-brand" : "bg-panel text-ink-dim"
            }`}
          >
            {#if card.kind === "mic"}
              <svg viewBox="0 0 20 20" fill="none" class="size-4.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="7.5" y="2" width="5" height="9" rx="2.5"/><path d="M4.5 9.5a5.5 5.5 0 0 0 11 0M10 15v3"/></svg>
            {:else if card.kind === "camera"}
              <svg viewBox="0 0 20 20" fill="none" class="size-4.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="5" width="11" height="10" rx="2"/><path d="m13 9 5-2.5v7L13 11"/></svg>
            {:else if card.kind === "call"}
              <svg viewBox="0 0 20 20" fill="none" class="size-4.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 3.5h3l1.5 4-2 1.5a11 11 0 0 0 4.5 4.5l1.5-2 4 1.5v3a1.5 1.5 0 0 1-1.6 1.5C8.6 17 3 11.4 2.5 5.1A1.5 1.5 0 0 1 4 3.5Z"/></svg>
            {:else}
              <svg viewBox="0 0 20 20" fill="none" class="size-4.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 12.5 12 10 8 7.5v5Z"/><rect x="2" y="4" width="16" height="12" rx="2.5"/></svg>
            {/if}
          </span>
          <span class="min-w-0">
            <span class="block text-sm font-semibold">{card.title}</span>
            <span class="mt-0.5 block text-xs leading-relaxed text-ink-dim">{card.desc}</span>
          </span>
        </span>
      </button>
    {/each}
  </div>

  {#if wizard.draft.sourceKind === "mic" && wizard.captureDevices.length > 0}
    <div class="flex flex-col gap-1 text-sm sm:max-w-sm">
      <span class="text-ink-dim">{i18n.m.wizard.micPicker}</span>
      <Select
        bind:value={wizard.draft.micChoice}
        label={i18n.m.wizard.micPicker}
        options={wizard.captureDevices.map((device) => ({ value: device.url, label: device.label }))}
      />
    </div>
  {:else if wizard.draft.sourceKind === "camera"}
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <div class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.cameraPicker}</span>
        <Select
          bind:value={wizard.draft.cameraChoice}
          label={i18n.m.wizard.cameraPicker}
          options={wizard.cameraDevices.map((device) => ({ value: device.url, label: device.label }))}
        />
      </div>
      <div class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.pairedMic}</span>
        <Select
          bind:value={wizard.draft.pairedMic}
          label={i18n.m.wizard.pairedMic}
          options={[
            { value: "", label: "—" },
            ...wizard.captureDevices.map((device) => ({ value: device.url, label: device.label })),
          ]}
        />
      </div>
    </div>
  {:else if wizard.draft.sourceKind === "call"}
    <div class="flex flex-col gap-3">
      <p class="rounded-xl border border-hairline bg-panel-2 p-3 text-sm leading-relaxed text-ink-dim">
        {i18n.m.wizard.callHowTo}
      </p>
      <div class="flex flex-col gap-1 text-sm sm:max-w-sm">
        <span class="text-ink-dim">{i18n.m.wizard.remoteSource}</span>
        {#if wizard.captureDevices.length > 0}
          <Select
            bind:value={wizard.draft.callRemote}
            label={i18n.m.wizard.remoteSource}
            options={[
              ...wizard.captureDevices.map((device) => ({ value: device.url, label: device.label })),
              { value: "custom", label: i18n.m.wizard.sourceCustom },
            ]}
          />
        {/if}
        {#if wizard.captureDevices.length === 0 || wizard.draft.callRemote === "custom"}
          <input
            bind:value={wizard.draft.callRemoteUrl}
            type="text"
            spellcheck="false"
            autocapitalize="none"
            placeholder="device://audio/<id>"
            required
            aria-label={i18n.m.wizard.remoteSourceUrl}
            class={inputClasses}
          />
        {/if}
        <span class="text-xs text-ink-dim">{i18n.m.wizard.remoteSourceHint}</span>
      </div>
    </div>
  {:else if wizard.draft.sourceKind === "stream"}
    <label class="flex flex-col gap-1 text-sm sm:max-w-lg">
      <span class="text-ink-dim">{i18n.m.wizard.source}</span>
      <input
        bind:value={wizard.draft.streamUrl}
        type="text"
        spellcheck="false"
        autocapitalize="none"
        placeholder="rtmp://0.0.0.0:1935/in/my-stream"
        required
        class={inputClasses}
      />
    </label>
  {/if}

  <div class="flex items-center justify-end">
    <button
      type="button"
      disabled={wizard.draft.sourceKind === "" || !wizard.ready}
      onclick={(event) => wizard.continueFromSource(event.currentTarget.form!)}
      class={primaryButton}
    >
      {i18n.m.wizard.next}
    </button>
  </div>
</div>
