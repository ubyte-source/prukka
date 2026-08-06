<script lang="ts">
  // Accessible on/off switch: a real button with role="switch", so assistive
  // technology announces state, and a physical track/thumb so sighted users
  // read it at a glance.

  interface Props {
    label: string;
    checked: boolean;
    disabled?: boolean;
    /** hint renders under the label in dim text (optional). */
    hint?: string;
    onchange?: (checked: boolean) => void;
  }

  const { label, checked, disabled = false, hint = "", onchange }: Props = $props();
</script>

<div class="flex items-start justify-between gap-4">
  <div class="min-w-0">
    <span class="block text-sm font-medium text-ink">{label}</span>
    {#if hint}
      <span class="mt-0.5 block text-xs text-ink-dim">{hint}</span>
    {/if}
  </div>
  <button
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={label}
    {disabled}
    onclick={() => onchange?.(!checked)}
    class={`relative h-7 w-12 shrink-0 rounded-full border transition-colors duration-200
            disabled:cursor-not-allowed disabled:opacity-40 ${
              checked
                ? "border-brand-dim bg-brand-dim"
                : "border-line bg-panel-2"
            }`}
  >
    <span
      aria-hidden="true"
      class={`absolute top-0.5 left-0.5 h-[22px] w-[22px] rounded-full bg-white
              shadow-[0_1px_3px_rgb(0_0_0_/_0.4)] transition-transform duration-200 ${
                checked ? "translate-x-5" : ""
              }`}
    ></span>
  </button>
</div>
