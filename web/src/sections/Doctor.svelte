<script lang="ts">
  import { doctor } from "../lib/api/client";
  import { i18n } from "../lib/i18n/index.svelte";
  import type { DoctorCheck } from "../lib/api/types";

  let checks = $state<DoctorCheck[]>([]);
  let unreachable = $state(false);

  const badge: Record<string, string> = {
    ok: "bg-ok/15 text-ok",
    warn: "bg-warn/15 text-warn",
    fail: "bg-danger/15 text-danger",
  };

  async function refresh() {
    try {
      checks = await doctor();
      unreachable = false;
    } catch {
      unreachable = true;
    }
  }

  $effect(() => {
    void refresh();
    const timer = setInterval(() => void refresh(), 60_000);
    return () => clearInterval(timer);
  });
</script>

<section class="rounded-xl border border-line bg-panel p-5">
  <h2 class="mb-4 text-base font-semibold">{i18n.m.doctor.title}</h2>

  {#if unreachable}
    <p class="text-sm text-danger">{i18n.m.doctor.unreachable}</p>
  {:else}
    <ul class="flex flex-col gap-2">
      {#each checks as check (check.name)}
        <li class="flex items-start gap-3 text-sm">
          <span
            class={`mt-0.5 rounded px-1.5 py-0.5 font-mono text-xs
                    ${badge[check.status] ?? "bg-panel-2 text-ink-dim"}`}
          >
            {check.status}
          </span>
          <div>
            <span class="font-mono">{check.name}</span>
            <span class="text-ink-dim"> — {check.detail}</span>
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</section>
