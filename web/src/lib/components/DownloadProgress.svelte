<script lang="ts">
  // Shared download bar for the language pack manager and every wizard call
  // site, so progress looks and reads the same everywhere. The progressbar
  // role carries the announced value; the size/step lines are visual only, so
  // a screen reader is not flooded on every byte frame.

  interface Props {
    /** pct is 0..100, or null for an indeterminate (unknown-total) pulse. */
    pct: number | null;
    /** phaseLabel is the plain-language step, e.g. "Downloading". */
    phaseLabel: string;
    /** sizeLine is the human size, e.g. "12 MB of 45 MB" (optional). */
    sizeLine?: string;
    /** stepLine is the pack counter, e.g. "Step 1 of 3" (optional). */
    stepLine?: string;
  }

  const { pct, phaseLabel, sizeLine = "", stepLine = "" }: Props = $props();
</script>

<div class="mt-3 flex flex-col gap-1">
  <div class="flex flex-wrap items-baseline justify-between gap-x-3 text-xs">
    <span class="font-medium text-ink">{phaseLabel}</span>
    {#if stepLine}<span class="text-ink-dim">{stepLine}</span>{/if}
  </div>
  <div
    role="progressbar"
    aria-label={phaseLabel}
    aria-valuemin={0}
    aria-valuemax={100}
    aria-valuenow={pct ?? undefined}
    class="h-2 overflow-hidden rounded-full bg-panel"
  >
    <div
      class="h-full rounded-full bg-brand-dim transition-[width] duration-300"
      class:animate-pulse={pct === null}
      style="width: {pct ?? 100}%"
    ></div>
  </div>
  {#if sizeLine}<p class="text-xs text-ink-dim">{sizeLine}</p>{/if}
</div>
