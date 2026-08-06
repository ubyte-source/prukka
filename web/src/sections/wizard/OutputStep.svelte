<script lang="ts">
  import { primaryButton, secondaryButton } from "../../lib/components/classes";
  import Segmented from "../../lib/components/Segmented.svelte";
  import Select from "../../lib/components/Select.svelte";
  import { i18n } from "../../lib/i18n/index.svelte";
  import { daemon } from "../../lib/state/daemon.svelte";
  import type { WizardState } from "../../lib/wizard/state.svelte";

  interface Props {
    wizard: WizardState;
  }

  const { wizard }: Props = $props();
</script>

<div class="step-enter flex flex-col gap-4">
  <h3 tabindex="-1" class="text-base font-medium text-ink">
    {i18n.m.wizard.outputStep}
  </h3>
  {#if daemon.devicesError}
    <p role="status" class="rounded-xl border border-hairline bg-panel-2 p-3 text-sm text-warn">
      {i18n.m.wizard.devicesUnavailable}
    </p>
  {/if}

  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    {#if wizard.draft.sourceKind === "call"}
      {#if wizard.audioOutputDevices.length > 0}
        <div class="flex flex-col gap-1 text-sm">
          <span class="text-ink-dim">{i18n.m.wizard.listenOutput}</span>
          <Select
            bind:value={
              () => wizard.draft.roles.listen,
              (value) => (wizard.draft.roles = { ...wizard.draft.roles, listen: value })
            }
            label={i18n.m.wizard.listenOutput}
            options={[
              { value: "", label: i18n.m.wizard.outputNone },
              ...wizard.audioOutputDevices.map((device) => ({ value: device.url, label: device.label })),
            ]}
          />
          <span class="text-xs text-ink-dim">{i18n.m.wizard.listenOutputHint}</span>
        </div>
      {/if}

      {#if wizard.twoWay}
        {#if wizard.captureDevices.length > 0}
          <div class="flex flex-col gap-1 text-sm">
            <span class="text-ink-dim">{i18n.m.wizard.yourMic}</span>
            <Select
              bind:value={
                () => wizard.draft.roles.mic,
                (value) => (wizard.draft.roles = { ...wizard.draft.roles, mic: value })
              }
              label={i18n.m.wizard.yourMic}
              options={[
                { value: "", label: i18n.m.wizard.outputNone },
                ...wizard.captureDevices.map((device) => ({ value: device.url, label: device.label })),
              ]}
            />
            <span class="text-xs text-ink-dim">{i18n.m.wizard.yourMicHint}</span>
          </div>
        {/if}

        {#if wizard.audioOutputDevices.length > 0}
          <div class="flex flex-col gap-1 text-sm">
            <span class="text-ink-dim">{i18n.m.wizard.outgoing}</span>
            <Select
              bind:value={
                () => wizard.draft.roles.outgoing,
                (value) => (wizard.draft.roles = { ...wizard.draft.roles, outgoing: value })
              }
              label={i18n.m.wizard.outgoing}
              options={[
                { value: "", label: i18n.m.wizard.outputNone },
                ...wizard.audioOutputDevices.map((device) => ({ value: device.url, label: device.label })),
              ]}
            />
            <span class="text-xs text-ink-dim">{i18n.m.wizard.outgoingHint}</span>
          </div>
        {/if}
      {/if}
    {:else if wizard.draft.sourceKind === "mic"}
      {#if wizard.audioOutputDevices.length > 0}
        <div class="flex flex-col gap-1 text-sm">
          <span class="text-ink-dim">{i18n.m.wizard.outputAudio}</span>
          <Select
            bind:value={wizard.draft.output}
            label={i18n.m.wizard.outputAudio}
            options={[
              { value: "", label: i18n.m.wizard.outputNone },
              ...wizard.audioOutputDevices.map((device) => ({ value: device.url, label: device.label })),
            ]}
          />
        </div>
      {/if}
    {:else if wizard.videoOutputDevices.length > 0}
      <div class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.videoOutput}</span>
        <Select
          bind:value={wizard.draft.output}
          label={i18n.m.wizard.videoOutput}
          options={[
            { value: "", label: i18n.m.wizard.outputNone },
            ...wizard.videoOutputDevices.map((device) => ({ value: device.url, label: device.label })),
          ]}
        />
      </div>
    {/if}

    <div class="flex flex-col gap-1 text-sm">
      <span class="text-ink-dim">{i18n.m.wizard.subtitles}</span>
      <Segmented
        bind:value={wizard.draft.subs}
        label={i18n.m.wizard.subtitles}
        options={[
          { value: "vtt", label: i18n.m.wizard.subsOn },
          { value: "off", label: i18n.m.wizard.subsOff },
          { value: "burn", label: i18n.m.wizard.subsBurn },
        ]}
      />
    </div>
  </div>

  <div class="flex items-center justify-between gap-3">
    <button type="button" onclick={() => wizard.moveToStep(2)} class={secondaryButton}>
      {i18n.m.wizard.back}
    </button>
    <button type="button" onclick={() => wizard.continueFromOutput()} class={primaryButton}>
      {i18n.m.wizard.next}
    </button>
  </div>
</div>
