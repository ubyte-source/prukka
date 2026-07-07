<script lang="ts">
  import { doctor } from "../lib/api/client";
  import { i18n } from "../lib/i18n/index.svelte";
  import { isControlToken, token } from "../lib/state/token.svelte";
  import type { DoctorCheck } from "../lib/api/types";

  let checks = $state<DoctorCheck[]>([]);
  let unreachable = $state(false);
  let loading = $state(false);
  let revision = 0;
  let inFlight: AbortController | null = null;
  const controlToken = $derived(isControlToken(token.value) ? token.value : "");

  const badge: Record<string, string> = {
    ok: "bg-ok/15 text-ok",
    warn: "bg-warn/15 text-warn",
    fail: "bg-danger/15 text-danger",
  };

  async function refresh(tokenSnapshot: string) {
    const requestRevision = ++revision;
    inFlight?.abort();
    inFlight = null;

    if (tokenSnapshot === "") {
      checks = [];
      unreachable = false;
      loading = false;
      return;
    }

    const controller = new AbortController();
    inFlight = controller;
    loading = true;
    unreachable = false;
    try {
      const next = await doctor(tokenSnapshot, controller.signal);
      if (requestRevision !== revision || controller.signal.aborted) return;

      checks = next;
      unreachable = false;
    } catch {
      if (requestRevision !== revision || controller.signal.aborted) return;

      unreachable = true;
    } finally {
      if (requestRevision !== revision) return;

      if (inFlight === controller) inFlight = null;
      loading = false;
    }
  }

  $effect(() => {
    const tokenSnapshot = controlToken;
    checks = [];
    void refresh(tokenSnapshot);
    if (tokenSnapshot === "") return;

    const timer = setInterval(() => void refresh(tokenSnapshot), 60_000);
    return () => {
      clearInterval(timer);
      revision += 1;
      inFlight?.abort();
      inFlight = null;
    };
  });
</script>

<section
  aria-labelledby="doctor-title"
  aria-busy={loading}
  class="min-w-0 rounded-xl border border-line bg-panel p-5"
>
  <h2 id="doctor-title" class="mb-4 text-base font-semibold">{i18n.m.doctor.title}</h2>

  {#if !isControlToken(token.value)}
    <p class="text-sm text-ink-dim">{i18n.m.doctor.tokenRequired}</p>
  {:else if unreachable}
    <div class="flex flex-wrap items-center gap-3 text-sm text-danger" role="alert">
      <p>{i18n.m.doctor.unreachable}</p>
      <button
        type="button"
        disabled={loading}
        onclick={() => void refresh(token.value)}
        class="rounded-lg border border-line px-3 py-1.5 text-ink hover:border-brand"
      >
        {i18n.m.doctor.retry}
      </button>
    </div>
  {:else if loading && checks.length === 0}
    <p class="text-sm text-ink-dim" role="status">{i18n.m.doctor.loading}</p>
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
          <div class="min-w-0 flex-1">
            <span class="font-mono">{check.name}</span>
            <span class="block break-words text-ink-dim">{check.detail}</span>
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</section>
