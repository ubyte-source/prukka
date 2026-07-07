<script lang="ts">
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
</script>

<section class="rounded-xl border border-line bg-panel p-5">
  <h2 class="mb-4 text-base font-semibold">{i18n.m.events.title}</h2>

  {#if daemon.events.length === 0}
    <p class="text-sm text-ink-dim">{i18n.m.events.empty}</p>
  {:else}
    <ul class="flex flex-col gap-1.5 font-mono text-xs" aria-live="polite">
      {#each daemon.events as entry (entry.at.getTime() + entry.text)}
        <li>
          <span class="text-ink-dim">{entry.at.toLocaleTimeString()}</span>
          <span class="ml-2">{entry.text}</span>
        </li>
      {/each}
    </ul>
  {/if}
</section>
