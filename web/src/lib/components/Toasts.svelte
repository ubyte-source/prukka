<script lang="ts">
  import { i18n } from "../i18n/index.svelte";
  import { toasts } from "../state/toasts.svelte";

  let region = $state<HTMLDivElement | null>(null);

  // A manual popover joins the browser's top layer, so toasts stay visible
  // above modal dialogs (PushDialog); entering after them stacks on top.
  $effect(() => {
    if (!region) return;
    try {
      if (toasts.items.length > 0 && !region.matches(":popover-open")) {
        region.showPopover();
      } else if (toasts.items.length === 0 && region.matches(":popover-open")) {
        region.hidePopover();
      }
    } catch {
      region.removeAttribute("popover");
    }
  });
</script>

<div
  bind:this={region}
  popover="manual"
  role="region"
  aria-label={i18n.m.toasts.notifications}
  class="fixed inset-auto right-4 bottom-4 m-0 flex w-80 max-w-[calc(100vw-2rem)]
         flex-col gap-2 border-0 bg-transparent p-0"
>
  {#each toasts.items as toast (toast.id)}
    <div
      role="alert"
      class="flex items-start gap-3 rounded-xl border border-line bg-panel p-3 text-sm
             text-danger shadow-lg"
    >
      <span class="min-w-0 flex-1 break-words">{toast.text}</span>
      <button
        type="button"
        class="text-ink-dim hover:text-ink"
        aria-label={i18n.m.toasts.dismiss}
        onclick={() => toasts.dismiss(toast.id)}
      >
        ×
      </button>
    </div>
  {/each}
</div>
