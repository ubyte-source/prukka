<!-- Styled single-select dropdown (ARIA select-only combobox): the
     trigger keeps DOM focus and steers the option list through
     aria-activedescendant, following the select-only combobox pattern.
     `compact` renders the small round "+" trigger used for add actions. -->

<script lang="ts" module>
  let instances = 0;
</script>

<script lang="ts">
  import { onDestroy } from "svelte";

  interface Option {
    value: string;
    label: string;
  }

  interface Props {
    options: Option[];
    label: string;
    value?: string;
    compact?: boolean;
    disabled?: boolean;
    onchange?: (value: string) => void;
  }

  let {
    options,
    label,
    value = $bindable(""),
    compact = false,
    disabled = false,
    onchange,
  }: Props = $props();

  const uid = `select-${++instances}`;

  let root = $state<HTMLDivElement | null>(null);
  let trigger = $state<HTMLButtonElement | null>(null);
  let open = $state(false);
  let active = $state(0);
  let typeahead = "";
  let typeaheadTimer: ReturnType<typeof setTimeout> | undefined;

  const selected = $derived(options.find((o) => o.value === value));

  function show() {
    if (disabled || options.length === 0) return;
    clearTimeout(typeaheadTimer);
    typeahead = "";
    active = Math.max(
      options.findIndex((o) => o.value === value),
      0,
    );
    open = true;
  }

  function pick(index: number) {
    const option = options[index];
    open = false;
    clearTimeout(typeaheadTimer);
    typeahead = "";

    if (option) {
      value = option.value;
      onchange?.(option.value);
    }

    // Closing unmounts the listbox; if an option button held focus (mouse
    // pick) it would fall to <body> and break Tab order, so return focus to
    // the combobox trigger — the APG select-only combobox behavior.
    trigger?.focus();
  }

  function onkeydown(event: KeyboardEvent) {
    if (
      event.key.length === 1 && !event.altKey && !event.ctrlKey &&
      !event.metaKey && event.key !== " "
    ) {
      if (!open) show();
      clearTimeout(typeaheadTimer);
      typeahead += event.key.toLocaleLowerCase(i18nLocale());
      typeaheadTimer = setTimeout(() => (typeahead = ""), 700);

      const start = typeahead.length === 1 ? active + 1 : active;
      for (let offset = 0; offset < options.length; offset += 1) {
        const index = (start + offset) % options.length;
        if (options[index]?.label.toLocaleLowerCase(i18nLocale()).startsWith(typeahead)) {
          active = index;
          break;
        }
      }
      event.preventDefault();
      return;
    }

    if (!open) {
      if (["ArrowDown", "ArrowUp", "Enter", " "].includes(event.key)) {
        event.preventDefault();
        show();
      }

      return;
    }

    switch (event.key) {
      case "ArrowDown":
        active = Math.min(active + 1, options.length - 1);
        break;
      case "ArrowUp":
        active = Math.max(active - 1, 0);
        break;
      case "Home":
        active = 0;
        break;
      case "End":
        active = options.length - 1;
        break;
      case "Enter":
      case " ":
        pick(active);
        break;
      case "Escape":
        open = false;
        break;
      case "Tab":
        open = false;

        return;
      default:
        return;
    }

    event.preventDefault();
  }

  function i18nLocale(): string | undefined {
    return document.documentElement.lang || undefined;
  }

  onDestroy(() => clearTimeout(typeaheadTimer));

  $effect(() => {
    if (open) {
      document
        .getElementById(`${uid}-${active}`)
        ?.scrollIntoView({ block: "nearest" });
    }
  });
</script>

<svelte:window
  onpointerdown={(e) => {
    if (open && root && !root.contains(e.target as Node)) open = false;
  }}
/>

<div
  bind:this={root}
  class="relative"
  onfocusout={(e) => {
    if (!root?.contains(e.relatedTarget as Node)) open = false;
  }}
>
  <button
    bind:this={trigger}
    type="button"
    role="combobox"
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-label={label}
    aria-controls={open ? `${uid}-list` : undefined}
    aria-activedescendant={open && options.length > 0 ? `${uid}-${active}` : undefined}
    disabled={disabled || options.length === 0}
    onclick={() => (open ? (open = false) : show())}
    {onkeydown}
    class={compact
      ? `w-8 rounded-full border border-line bg-panel-2 py-0.5 text-center
         text-xs text-ink-dim outline-none hover:border-ink-dim
         focus:border-brand disabled:cursor-not-allowed disabled:opacity-40`
      : `flex w-full items-center justify-between gap-2 rounded-lg border
         border-line bg-panel-2 px-3 py-2 text-start outline-none
         focus:border-brand disabled:cursor-not-allowed disabled:opacity-40`}
  >
    {#if compact}
      +
    {:else}
      <span class="min-w-0 truncate">{selected?.label ?? value}</span>
      <svg
        viewBox="0 0 12 12"
        fill="none"
        aria-hidden="true"
        class="size-3 shrink-0 text-ink-dim transition-transform
               {open ? 'rotate-180' : ''}"
      >
        <path
          d="M2.5 4.5 6 8l3.5-3.5"
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
      </svg>
    {/if}
  </button>

  {#if open}
    <div
      id="{uid}-list"
      role="listbox"
      aria-label={label}
      class="absolute z-20 mt-1 max-h-60 min-w-full overflow-y-auto
             rounded-lg border border-line bg-panel py-1 shadow-lg
             shadow-black/30"
    >
      {#each options as option, index (option.value)}
        <button
          type="button"
          id="{uid}-{index}"
          role="option"
          aria-selected={option.value === value}
          tabindex="-1"
          onclick={() => pick(index)}
          onpointermove={() => (active = index)}
          class="block w-full cursor-pointer whitespace-nowrap px-3 py-1.5
                 text-start text-sm
                 {index === active ? 'bg-panel-2' : ''}
                 {option.value === value ? 'text-brand' : ''}"
        >
          {option.label}
        </button>
      {/each}
    </div>
  {/if}
</div>
