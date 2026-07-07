<script lang="ts">
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
</script>

<section aria-labelledby="events-title" class="min-w-0 rounded-xl border border-line bg-panel p-5">
  <h2 id="events-title" class="mb-4 text-base font-semibold">{i18n.m.events.title}</h2>

  {#if daemon.events.length === 0}
    <p class="text-sm text-ink-dim">{i18n.m.events.empty}</p>
  {:else}
    <ul
      class="flex flex-col gap-1.5 font-mono text-xs"
      role="log"
      aria-labelledby="events-title"
      aria-live="polite"
      aria-atomic="false"
      aria-relevant="additions"
    >
      {#each daemon.events as entry (entry.id)}
        <li>
          <time datetime={entry.at.toISOString()} class="text-ink-dim">
            {entry.at.toLocaleTimeString()}
          </time>
          <span class="ml-2">{entry.text}</span>
        </li>
      {/each}
    </ul>
  {/if}
</section>
