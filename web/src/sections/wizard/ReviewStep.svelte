<script lang="ts">
  import { inputClasses, primaryButton, secondaryButton } from "../../lib/components/classes";
  import { i18n } from "../../lib/i18n/index.svelte";
  import { slugPattern } from "../../lib/wizard/model";
  import type { WizardState } from "../../lib/wizard/state.svelte";

  interface Props {
    wizard: WizardState;
  }

  const { wizard }: Props = $props();
</script>

<div class="step-enter flex flex-col gap-4">
  <h3 tabindex="-1" class="text-base font-medium text-ink">
    {i18n.m.wizard.reviewStep}
  </h3>

  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    <label class="flex flex-col gap-1 text-sm">
      <span class="text-ink-dim">{i18n.m.wizard.name}</span>
      <input
        bind:value={wizard.draft.slug}
        type="text"
        pattern={slugPattern}
        maxlength={wizard.nameMax}
        placeholder="my-stream"
        required
        class={inputClasses}
      />
      {#if wizard.draft.sourceKind === "call" && wizard.twoWay}
        <span class="text-xs text-ink-dim">{i18n.m.wizard.nameTwoWayHint}</span>
      {/if}
    </label>

    {#if wizard.draft.sourceKind !== "call"}
      <label class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.delay}</span>
        <!-- A number binding yields null for an empty field, and a null delay
             is omitted from the request, so clearing this reverts to the
             daemon default instead of asking for zero seconds. -->
        <input
          bind:value={wizard.draft.delaySeconds}
          type="number"
          min="0"
          max="60"
          step="1"
          class={inputClasses}
        />
        <span class="text-xs text-ink-dim">{i18n.m.wizard.delayHint}</span>
      </label>
    {/if}
  </div>

  <dl class="flex flex-col gap-2 rounded-xl border border-hairline bg-panel-2 p-4 text-sm">
    <div class="flex flex-wrap items-baseline gap-x-3">
      <dt class="w-24 shrink-0 text-xs uppercase tracking-wide text-ink-dim">
        {i18n.m.wizard.summarySource}
      </dt>
      <dd class="min-w-0 break-all font-medium">{wizard.summarySource()}</dd>
    </div>
    <div class="flex flex-wrap items-baseline gap-x-3">
      <dt class="w-24 shrink-0 text-xs uppercase tracking-wide text-ink-dim">
        {i18n.m.wizard.summaryLanguages}
      </dt>
      <dd class="min-w-0 font-medium">{wizard.summaryLanguages()}</dd>
    </div>
    <div class="flex flex-wrap items-baseline gap-x-3">
      <dt class="w-24 shrink-0 text-xs uppercase tracking-wide text-ink-dim">
        {i18n.m.wizard.summaryOutput}
      </dt>
      <dd class="min-w-0 break-all font-medium">{wizard.summaryOutput()}</dd>
    </div>
  </dl>

  <p class="rounded-xl border border-hairline bg-panel-2 p-3 text-xs leading-relaxed text-ink-dim">
    {i18n.m.wizard.participantNotice}
  </p>

  <div class="flex flex-wrap items-center gap-3">
    <button type="button" onclick={() => wizard.moveToStep(3)} class={secondaryButton}>
      {i18n.m.wizard.back}
    </button>
    <span class="flex-1"></span>
    <button
      type="submit"
      disabled={wizard.busy || !wizard.nameValid
        || (wizard.draft.sourceKind === "call" && !wizard.callSubmittable)}
      class={primaryButton}
    >
      {wizard.busy ? i18n.m.wizard.creating : i18n.m.wizard.create}
    </button>
  </div>
</div>
