<script lang="ts">
  // Shell of the guided wizard: owns the WizardState instance and the
  // lifecycle effects (default seeding, call-language seeding, the device
  // poll, step focus). Each step's markup lives in its own component under
  // sections/wizard/.

  import { onDestroy, tick } from "svelte";
  import Stepper from "../lib/components/Stepper.svelte";
  import { i18n } from "../lib/i18n/index.svelte";
  import { token } from "../lib/state/token.svelte";
  import { WizardState } from "../lib/wizard/state.svelte";
  import LanguagesStep from "./wizard/LanguagesStep.svelte";
  import OutputStep from "./wizard/OutputStep.svelte";
  import ReviewStep from "./wizard/ReviewStep.svelte";
  import SourceStep from "./wizard/SourceStep.svelte";

  const wizard = new WizardState();
  onDestroy(() => wizard.dispose());

  let steps = $state<HTMLDivElement | null>(null);
  let lastStep = wizard.step;

  // Moving between steps sends focus to the new step's heading; the initial
  // render must not steal focus from the page.
  $effect(() => {
    const step = wizard.step;
    if (step === lastStep) return;
    lastStep = step;
    void tick().then(() => {
      steps?.querySelector<HTMLElement>("h3[tabindex='-1']")?.focus();
    });
  });

  $effect(() => wizard.seedDefaults());
  $effect(() => wizard.seedCallLanguages());

  // Devices can appear after load (OS capture consent, hotplug): while a
  // device picker is actually in front of the user, keep the list fresh and
  // complete the roles that are still empty as soon as the canonical devices
  // show up. The idle wizard (no source chosen yet) polls nothing.
  $effect(() => {
    if (!wizard.devicePollActive) return;
    const timer = setInterval(() => wizard.refreshDevices(token.value), 4_000);
    return () => clearInterval(timer);
  });

  function submit(event: SubmitEvent) {
    event.preventDefault();
    wizard.handleSubmit(event.currentTarget as HTMLFormElement);
  }
</script>

<section
  aria-labelledby="wizard-title"
  class="rounded-2xl border border-hairline bg-panel p-6 shadow-card sm:p-7"
>
  <div class="mb-5 flex flex-wrap items-baseline justify-between gap-3">
    <h2 id="wizard-title" class="text-lg font-semibold tracking-tight">{i18n.m.wizard.title}</h2>
    <Stepper steps={wizard.stepTitles} current={wizard.step - 1} />
  </div>

  <form
    onsubmit={submit}
    autocomplete="off"
    aria-busy={wizard.busy}
    class="flex flex-col gap-5"
  >
    <fieldset disabled={wizard.busy} class="contents">
      <div bind:this={steps} class="contents">
        {#if wizard.step === 1}
          <SourceStep {wizard} />
        {:else if wizard.step === 2}
          <LanguagesStep {wizard} />
        {:else if wizard.step === 3}
          <OutputStep {wizard} />
        {:else}
          <ReviewStep {wizard} />
        {/if}
      </div>
    </fieldset>
  </form>
</section>
