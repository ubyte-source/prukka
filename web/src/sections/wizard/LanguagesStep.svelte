<script lang="ts">
  import { primaryButton, secondaryButton } from "../../lib/components/classes";
  import LanguageDownload from "../../lib/components/LanguageDownload.svelte";
  import Select from "../../lib/components/Select.svelte";
  import Switch from "../../lib/components/Switch.svelte";
  import { i18n } from "../../lib/i18n/index.svelte";
  import { daemon } from "../../lib/state/daemon.svelte";
  import type { TargetIssue, WizardState } from "../../lib/wizard/state.svelte";

  interface Props {
    wizard: WizardState;
  }

  const { wizard }: Props = $props();
</script>

<!-- A blocked chip keeps its worded reason in the accessible name; visually
     the reason is one glyph, so thirty blocked languages read as a quiet tail
     instead of a wall of repeated text. -->
{#snippet chip(
  tag: string,
  pressed: boolean,
  issue: TargetIssue,
  describedBy: string,
  onclick: () => void,
)}
  {@const reason = wizard.issueLabel(issue)}
  <button
    type="button"
    {onclick}
    aria-pressed={pressed}
    aria-describedby={describedBy}
    aria-label={reason ? `${wizard.languageLabel(tag)} · ${reason}` : undefined}
    title={reason || undefined}
    disabled={issue !== ""}
    class={`inline-flex items-center gap-1.5 rounded-full border px-3.5 py-1.5 text-sm
            transition-colors duration-150 ${
      pressed
        ? "border-brand bg-brand/10 font-medium text-brand"
        : "border-line bg-panel-2 text-ink-dim hover:border-brand disabled:cursor-not-allowed disabled:opacity-55"
    }`}
  >
    {#if pressed}<span aria-hidden="true">✓ </span>{/if}
    {wizard.languageLabel(tag)}
    {#if issue === "route"}
      <svg viewBox="0 0 12 12" fill="none" aria-hidden="true" class="size-3 shrink-0"
        stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M1.5 4h7M6.5 2 8.5 4 6.5 6M10.5 8h-7M5.5 6 3.5 8l2 2" />
        <path d="M1 11 11 1" />
      </svg>
    {:else if issue === "voice"}
      <svg viewBox="0 0 12 12" fill="none" aria-hidden="true" class="size-3 shrink-0"
        stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round">
        <rect x="1" y="2.5" width="10" height="7" rx="2" />
        <path d="M3 6h2.5M7 6h2M3 7.8h1.5M6 7.8h3" />
      </svg>
    {/if}
  </button>
{/snippet}

<div class="step-enter flex flex-col gap-4">
  <h3 tabindex="-1" class="text-base font-medium text-ink">
    {i18n.m.wizard.languagesStep}
  </h3>

  {#if wizard.draft.sourceKind === "call"}
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <div class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.youLang}</span>
        <Select
          bind:value={wizard.draft.youLang}
          label={i18n.m.wizard.youLang}
          options={wizard.languages.map((language) => ({ value: language.tag, label: language.label }))}
        />
      </div>
      <div class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.themLang}</span>
        <Select
          bind:value={wizard.draft.themLang}
          label={i18n.m.wizard.themLang}
          options={wizard.languages.map((language) => ({ value: language.tag, label: language.label }))}
        />
      </div>
    </div>

    <Switch
      label={i18n.m.wizard.twoWaySwitch}
      hint={wizard.twoWayAvailable ? i18n.m.wizard.twoWaySwitchHint : wizard.callFallbackHint()}
      checked={wizard.twoWay}
      disabled={!wizard.twoWayAvailable}
      onchange={(checked) => (wizard.draft.twoWayWanted = checked)}
    />

    {#if !wizard.voiceEnabled}
      <p role="status" class="rounded-xl border border-hairline bg-panel-2 p-3 text-sm text-warn">
        {i18n.m.wizard.dubCapabilityOff}
      </p>
    {:else if wizard.dubbedLangs.length === 0}
      <p role="status" class="rounded-xl border border-hairline bg-panel-2 p-3 text-sm text-warn">
        {i18n.m.wizard.dubCapabilityUnknown}
      </p>
    {:else if !wizard.callDiffers}
      <p role="status" class="rounded-xl border border-hairline bg-panel-2 p-3 text-sm text-ink-dim">
        {i18n.m.wizard.callSameLang}
      </p>
    {:else if wizard.twoWay}
      <p role="status" class="rounded-xl border border-hairline bg-panel-2 p-3 text-sm text-ink-dim">
        {wizard.callReadyNote()}
      </p>
    {:else if wizard.oneWayAvailable}
      <p role="status" class="rounded-xl border border-hairline bg-panel-2 p-3 text-sm text-warn">
        {wizard.callFallbackNote()}
      </p>
    {:else}
      <p role="status" class="rounded-xl border border-hairline bg-panel-2 p-3 text-sm text-warn">
        {wizard.callUnavailableNote()}
      </p>
    {/if}
    {#if wizard.callDownloadList.length > 0}
      <div class="flex flex-col gap-3 rounded-xl border border-brand/30 bg-brand/5 p-3">
        <p class="text-sm font-medium text-ink">{i18n.m.wizard.callNeedsDownload}</p>
        {#each wizard.callDownloadList as download (download.tag)}
          <LanguageDownload
            tag={download.tag}
            plan={download.plan}
            needVoice={download.needVoice}
            addBytes={download.addBytes}
            onAdded={(tag) => wizard.finishCallAdd(tag)}
          />
        {/each}
      </div>
    {/if}
    {#each wizard.draft.callAdded as tag (tag)}
      <p role="status" class="text-sm text-ok">
        {i18n.m.add.done.replace("{language}", wizard.languageLabel(tag))}
      </p>
    {/each}
  {:else}
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <div class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.sourceLang}</span>
        <Select
          bind:value={wizard.draft.sourceLang}
          onchange={(value) => wizard.setSourceLanguage(value)}
          label={i18n.m.wizard.sourceLang}
          options={[
            { value: "auto", label: i18n.m.wizard.autoDetect },
            ...wizard.languages.map((language) => ({ value: language.tag, label: language.label })),
          ]}
        />
      </div>
      <div class="flex flex-col justify-center">
        <Switch
          label={i18n.m.wizard.dubbing}
          hint={wizard.voiceEnabled ? "" : i18n.m.wizard.dubCapabilityOff}
          checked={wizard.draft.dub}
          disabled={!wizard.voiceEnabled}
          onchange={(checked) => wizard.setDubbing(checked)}
        />
      </div>
    </div>

    <p id="translation-capability" class="text-xs leading-relaxed text-ink-dim">
      {wizard.draft.sourceLang === "auto"
        ? i18n.m.wizard.translationAuto
        : wizard.sourceMessage(i18n.m.wizard.translationConcrete)}
    </p>

    <fieldset class="rounded-xl border border-hairline p-4">
      <legend class="px-1 text-sm text-ink-dim">
        {wizard.draft.dub ? i18n.m.wizard.dubTargets : i18n.m.wizard.targets}
      </legend>
      {#if wizard.draft.dub}
        <p id="dub-capability" class="mb-2.5 text-xs text-ink-dim">
          {!wizard.voiceEnabled
            ? i18n.m.wizard.dubCapabilityOff
            : wizard.dubbedLangs.length === 0
              ? i18n.m.wizard.dubCapabilityUnknown
              : wizard.voiceMessage(i18n.m.wizard.dubCapability)}
        </p>
      {/if}
      <div
        class="flex max-h-44 flex-wrap gap-2 overflow-y-auto"
        role="group"
        aria-label={wizard.draft.dub ? i18n.m.wizard.dubTargets : i18n.m.wizard.targets}
      >
        {#each wizard.orderedLanguages((tag) => wizard.targetIssue(tag, wizard.draft.dub) === "") as language (language.tag)}
          {@render chip(
            language.tag,
            wizard.draft.targets.includes(language.tag),
            wizard.targetIssue(language.tag, wizard.draft.dub),
            wizard.draft.dub ? "translation-capability dub-capability" : "translation-capability",
            () => wizard.toggleTarget(language.tag),
          )}
        {/each}
      </div>
      {#if wizard.draft.broadcastAdd}
        <div class="mt-3 max-w-xs">
          <LanguageDownload
            tag={wizard.draft.broadcastAdd.tag}
            plan={wizard.draft.broadcastAdd.plan}
            needVoice={wizard.draft.broadcastAdd.needVoice}
            addBytes={wizard.draft.broadcastAdd.addBytes}
            onAdded={(tag) => wizard.finishBroadcastAdd(tag)}
            onCancel={() => wizard.cancelBroadcastAdd()}
          />
        </div>
      {:else if daemon.engineSupported && wizard.broadcastAddable().length > 0}
        <div class="mt-3 max-w-xs">
          <Select
            compact
            label={i18n.m.wizard.addMissingBroadcast}
            options={wizard.broadcastAddable().map((language) => ({
              value: language.tag,
              label: language.label,
            }))}
            onchange={(tag) => wizard.beginBroadcastAdd(tag)}
          />
        </div>
      {/if}
    </fieldset>

    <fieldset class="rounded-xl border border-hairline p-4">
      <legend class="px-1 text-sm text-ink-dim">{i18n.m.wizard.captionTargets}</legend>
      <p class="mb-2.5 text-xs text-ink-dim">{i18n.m.wizard.captionTargetsDesc}</p>
      <div
        class="flex max-h-44 flex-wrap gap-2 overflow-y-auto"
        role="group"
        aria-label={i18n.m.wizard.captionTargets}
      >
        {#each wizard.orderedLanguages((tag) => wizard.targetIssue(tag, false) === "") as language (language.tag)}
          {@render chip(
            language.tag,
            wizard.draft.captionTargets.includes(language.tag),
            wizard.targetIssue(language.tag, false),
            "translation-capability",
            () => wizard.toggleCaptionTarget(language.tag),
          )}
        {/each}
      </div>
    </fieldset>
  {/if}

  <div class="flex items-center justify-between gap-3">
    <button type="button" onclick={() => wizard.moveToStep(1)} class={secondaryButton}>
      {i18n.m.wizard.back}
    </button>
    <button type="button" onclick={() => wizard.moveToStep(3)} class={primaryButton}>
      {i18n.m.wizard.next}
    </button>
  </div>
</div>
