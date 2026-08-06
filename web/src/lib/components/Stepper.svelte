<script lang="ts">
  // Orientation rail for the guided wizard: which step you are on, which are
  // done, and what comes next — so a basic user always knows where they are.

  import { i18n } from "../i18n/index.svelte";

  interface Props {
    /** steps are the ordered step titles. */
    steps: string[];
    /** current is the 0-based active step. */
    current: number;
    /** nextLabel names what the next step is (shown as "Next: …"). */
    nextLabel?: string;
  }

  const { steps, current, nextLabel = "" }: Props = $props();
</script>

<nav aria-label={i18n.m.stepper.progress} class="flex flex-col gap-2">
  <ol class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
    {#each steps as step, index (step)}
      <li class="flex items-center gap-2">
        <span
          aria-current={index === current ? "step" : undefined}
          class={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 font-medium ${
            index === current
              ? "bg-brand/10 text-brand"
              : index < current
                ? "text-ink-dim"
                : "text-ink-dim"
          }`}
        >
          <span
            class={`inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-[10px] ${
              index < current
                ? "bg-ok text-white"
                : index === current
                  ? "bg-brand text-white"
                  : "border border-line"
            }`}
            aria-hidden="true"
          >
            {index < current ? "✓" : index + 1}
          </span>
          <!-- sr-only, not hidden: on narrow viewports the name must leave the
               visual layout but stay in the accessibility tree, or the list
               items (and aria-current="step") announce as empty bullets. -->
          <span class="sr-only sm:not-sr-only">{step}</span>
        </span>
        {#if index < steps.length - 1}
          <span class="text-ink-dim/40" aria-hidden="true">→</span>
        {/if}
      </li>
    {/each}
  </ol>
  {#if nextLabel}
    <p class="text-xs text-ink-dim sm:hidden">{nextLabel}</p>
  {/if}
</nav>
