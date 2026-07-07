<script lang="ts">
  import { i18n } from "../lib/i18n/index.svelte";
  import { locales } from "../lib/i18n/messages";
  import { daemon } from "../lib/state/daemon.svelte";

  const dot: Record<string, string> = {
    idle: "bg-ink-dim",
    live: "bg-ok",
    degraded: "bg-danger",
  };

  const uptime = $derived(formatUptime(daemon.stats.uptimeSeconds ?? 0));
  const statusText = $derived(i18n.m.daemonStatus[daemon.status]);

  function formatUptime(seconds: number): string {
    const s = Math.floor(seconds);
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    return h > 0 ? `${h}h ${m}m` : `${m}m ${s % 60}s`;
  }
</script>

<header
  class="sticky top-0 z-10 -mx-4 flex flex-wrap items-center justify-between
         gap-4 border-b border-line bg-surface/90 px-4 py-3 backdrop-blur
         sm:-mx-6 sm:px-6"
>
  <div class="flex items-center gap-3">
    <span
      role="status"
      aria-label={`${i18n.m.daemonStatus.label}: ${statusText}`}
      class="inline-flex items-center gap-2"
    >
      <span class={`h-2.5 w-2.5 rounded-full ${dot[daemon.status]}`} aria-hidden="true"></span>
      <span class="text-xs text-ink-dim">{statusText}</span>
    </span>
    <svg viewBox="0 0 256 256" class="h-6 w-6" aria-hidden="true">
      <g fill="none" stroke="#0f766e" stroke-linecap="round" stroke-linejoin="round">
        <path
          d="M140 108 C124 100 114 86 112 72 C110 57 117 48 130 50 C137 51 142 60 150 68 C173 89 200 95 227 98 C248 101 258 120 253 142 C250 153 245 161 237 168 C245 182 241 198 228 207 C218 215 206 216 197 209 C202 188 196 171 182 158 C170 147 156 141 141 140 C131 136 126 126 130 117 C132 113 135 110 140 108 Z"
          fill="#ffffff"
          stroke-width="7"
        />
        <path d="M196 209 C214 208 229 199 237 184" stroke-width="7" />
        <path d="M185 159 C202 169 214 183 220 200" stroke-width="7" />
        <path
          d="M107 151 C133 132 178 139 204 170 C218 187 214 202 194 210 C168 220 113 219 94 203 C82 193 82 177 92 165 C96 159 101 155 107 151 Z"
          fill="#0f766e"
          stroke-width="7"
        />
        <path d="M160 151 C184 164 199 185 201 205" stroke="#ffffff" stroke-width="7" />
        <path d="M134 204 C156 213 184 210 202 196" stroke="#ffffff" stroke-width="6" />
        <path
          d="M120 178 C100 163 74 159 62 149 C55 143 52 136 53 129 C43 126 34 120 31 109 C18 101 8 86 11 72 C14 57 29 55 40 66 C64 97 108 107 149 111 C170 113 184 124 188 141 C192 158 182 174 164 183 C147 191 131 188 120 178 Z"
          fill="#ffffff"
          stroke-width="7"
        />
        <path d="M53 129 C68 137 88 143 109 146" stroke-width="7" />
        <path d="M62 149 C78 157 98 162 119 164" stroke-width="7" />
        <path d="M31 109 C48 122 71 131 95 136" stroke-width="7" />
        <circle cx="116" cy="174" r="22" fill="#ffffff" stroke-width="7" />
      </g>
      <circle cx="116" cy="174" r="12" fill="#0f766e" />
    </svg>
    <h1 class="text-lg font-semibold tracking-tight">Prukka</h1>
    <span class="hidden text-sm text-ink-dim md:inline">{i18n.m.tagline}</span>
  </div>

  <div class="flex flex-wrap items-center justify-end gap-5">
    <dl class="flex flex-wrap items-center justify-end gap-5 text-sm">
      <div class="flex flex-col text-center">
        <dt class="order-2 text-xs text-ink-dim">{i18n.m.stats.sessions}</dt>
        <dd class="order-1 font-mono font-semibold">{daemon.sessions.length}</dd>
      </div>
      <div class="flex flex-col text-center">
        <dt class="order-2 text-xs text-ink-dim">{i18n.m.stats.uptime}</dt>
        <dd class="order-1 font-mono font-semibold">{uptime}</dd>
      </div>
      <div class="flex flex-col text-center">
        <dt class="order-2 text-xs text-ink-dim">{i18n.m.stats.version}</dt>
        <dd class="order-1 font-mono font-semibold">{daemon.stats.version ?? "–"}</dd>
      </div>
    </dl>

    <nav aria-label={i18n.m.language} class="flex gap-1">
      {#each locales as loc (loc)}
        <button
          onclick={() => i18n.set(loc)}
          aria-pressed={i18n.locale === loc}
          class={`rounded px-1.5 py-0.5 font-mono text-xs uppercase transition-colors
                  focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand ${
                    i18n.locale === loc
                      ? "bg-brand/15 font-semibold text-brand underline underline-offset-4"
                      : "text-ink-dim hover:text-ink"
                  }`}
        >
          {loc}
        </button>
      {/each}
    </nav>
  </div>
</header>
