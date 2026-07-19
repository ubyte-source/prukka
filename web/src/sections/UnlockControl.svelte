<script lang="ts">
  // The single place a user connects Prukka. The control token is normally
  // adopted from the launch URL and stays invisible; the lock only glows when
  // it is missing. Clicking it opens a small field to paste (or change) the
  // access code, or to disconnect — so the token never lives inside the
  // configuration wizard.

  import { i18n } from "../lib/i18n/index.svelte";
  import { isControlToken, token } from "../lib/state/token.svelte";

  const connected = $derived(isControlToken(token.value));

  let open = $state(false);
  let value = $state("");
  let field = $state<HTMLInputElement | null>(null);

  function toggle() {
    open = !open;
    if (open) {
      value = "";
      queueMicrotask(() => field?.focus());
    }
  }

  function apply(event: SubmitEvent) {
    event.preventDefault();
    const candidate = value.trim();
    if (!isControlToken(candidate)) return;
    token.set(candidate);
    open = false;
  }

  function disconnect() {
    token.set("");
    open = false;
  }
</script>

<div class="relative">
  <button
    type="button"
    onclick={toggle}
    aria-expanded={open}
    class={`inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs font-medium
            text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand ${
              connected
                ? "border-ok/40 bg-ok/10 hover:border-ok"
                : "border-warn/50 bg-warn/10 hover:border-warn"
            }`}
  >
    <span class={`h-1.5 w-1.5 rounded-full ${connected ? "bg-ok" : "bg-warn"}`} aria-hidden="true"></span>
    {connected ? i18n.m.unlock.connected : i18n.m.unlock.connect}
  </button>

  {#if open}
    <form
      onsubmit={apply}
      class="absolute right-0 z-20 mt-2 flex w-72 flex-col gap-2 rounded-xl border border-line
             bg-panel p-3 shadow-lg"
    >
      <p class="text-xs text-ink-dim">{i18n.m.unlock.explain}</p>
      <label class="flex flex-col gap-1 text-xs font-medium">
        {i18n.m.unlock.field}
        <input
          bind:this={field}
          bind:value
          type="password"
          autocomplete="off"
          maxlength="64"
          onkeydown={(e) => e.key === "Escape" && (open = false)}
          class="rounded-lg border border-line bg-panel-2 px-2 py-1.5 font-mono text-sm
                 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand"
        />
      </label>
      <div class="flex items-center justify-between">
        {#if connected}
          <button
            type="button"
            onclick={disconnect}
            class="rounded-lg px-2 py-1.5 text-xs text-ink-dim hover:text-danger"
          >
            {i18n.m.unlock.disconnect}
          </button>
        {:else}
          <span></span>
        {/if}
        <button
          type="submit"
          disabled={!isControlToken(value.trim())}
          class="rounded-lg bg-brand-dim px-3 py-1.5 text-xs font-medium text-white
                 hover:brightness-110 disabled:opacity-40"
        >
          {i18n.m.unlock.apply}
        </button>
      </div>
    </form>
  {/if}
</div>
