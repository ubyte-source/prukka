<script lang="ts">
  // Plain-language status line for the Start view, replacing the bare coloured
  // dot with a sentence a non-technical user understands.

  import { i18n } from "../i18n/index.svelte";
  import { daemon } from "../state/daemon.svelte";

  const tone: Record<string, string> = {
    idle: "border-line bg-panel text-ink",
    live: "border-ok/40 bg-ok/10 text-ink",
    degraded: "border-warn/40 bg-warn/10 text-ink",
  };

  const dot: Record<string, string> = {
    idle: "bg-ink-dim",
    live: "bg-ok",
    degraded: "bg-warn",
  };

  const text = $derived.by(() => {
    if (daemon.status === "degraded") return i18n.m.home.attention;
    if (daemon.status === "live") return i18n.m.home.translating;
    return i18n.m.home.ready;
  });
</script>

<div
  role="status"
  aria-label={`${i18n.m.daemonStatus.label}: ${i18n.m.daemonStatus[daemon.status]}`}
  class={`flex items-center gap-2.5 rounded-xl border px-4 py-3 text-sm font-medium ${tone[daemon.status]}`}
>
  <span class={`h-2.5 w-2.5 shrink-0 rounded-full ${dot[daemon.status]}`} aria-hidden="true"></span>
  <span>{text}</span>
</div>
