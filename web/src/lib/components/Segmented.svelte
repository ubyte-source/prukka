<script lang="ts" module>
  let instances = 0;
</script>

<script lang="ts">
  // Segmented single-choice control (radiogroup semantics): the whole group is
  // one Tab stop, arrows move the selection — the standard radio pattern in a
  // segmented skin.

  interface Option {
    value: string;
    label: string;
  }

  interface Props {
    options: Option[];
    label: string;
    value?: string;
    disabled?: boolean;
    onchange?: (value: string) => void;
  }

  let {
    options,
    label,
    value = $bindable(""),
    disabled = false,
    onchange,
  }: Props = $props();

  const uid = `segmented-${++instances}`;

  function pick(next: string) {
    if (value === next) return;
    value = next;
    onchange?.(next);
  }

  function onkeydown(event: KeyboardEvent) {
    const index = Math.max(options.findIndex((option) => option.value === value), 0);
    let next = -1;
    if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      next = (index + 1) % options.length;
    } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      next = (index - 1 + options.length) % options.length;
    } else if (event.key === "Home") {
      next = 0;
    } else if (event.key === "End") {
      next = options.length - 1;
    } else {
      return;
    }
    event.preventDefault();
    const option = options[next];
    if (option) {
      pick(option.value);
      document.getElementById(`${uid}-${next}`)?.focus();
    }
  }

  function tabbable(option: Option, index: number): number {
    if (value === option.value) return 0;
    return options.some((candidate) => candidate.value === value) || index !== 0 ? -1 : 0;
  }
</script>

<div
  role="radiogroup"
  aria-label={label}
  class="inline-flex max-w-full overflow-x-auto rounded-[10px] bg-panel-2 p-0.5"
>
  {#each options as option, index (option.value)}
    <button
      type="button"
      id="{uid}-{index}"
      role="radio"
      aria-checked={value === option.value}
      tabindex={tabbable(option, index)}
      {disabled}
      onclick={() => pick(option.value)}
      {onkeydown}
      class={`whitespace-nowrap rounded-lg px-3 py-1.5 text-sm transition-colors duration-150
              disabled:cursor-not-allowed disabled:opacity-40 ${
                value === option.value
                  ? "bg-panel font-medium text-ink shadow-[0_1px_2px_rgb(0_0_0_/_0.25)]"
                  : "text-ink-dim hover:text-ink"
              }`}
    >
      {option.label}
    </button>
  {/each}
</div>
