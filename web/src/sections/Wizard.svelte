<script lang="ts">
  import { ApiError, createSession } from "../lib/api/client";
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
  import { token } from "../lib/state/token.svelte";

  const languages = $derived(daemon.languages);

  let slug = $state("");
  let sourceUrl = $state("");
  let sourceLang = $state("auto");
  let subs = $state("vtt");
  let dub = $state(true);
  let targets = $state<string[]>([]);
  let error = $state("");
  let busy = $state(false);

  function toggleTarget(tag: string) {
    targets = targets.includes(tag)
      ? targets.filter((t) => t !== tag)
      : [...targets, tag];
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    error = "";

    if (targets.length === 0) {
      error = i18n.m.wizard.needTarget;
      return;
    }

    const flags: Record<string, string> = { subs };
    if (sourceLang !== "auto") flags.source = sourceLang;
    if (!dub) flags.dub = "off";

    busy = true;
    try {
      await createSession(
        { slug: slug.trim(), sourceUrl: sourceUrl.trim(), langs: targets, flags },
        token.value,
      );
      slug = "";
      sourceUrl = "";
      targets = [];
      await daemon.refresh();
    } catch (e) {
      error = e instanceof ApiError ? e.message : i18n.m.wizard.createFailed;
    } finally {
      busy = false;
    }
  }
</script>

<section class="rounded-xl border border-line bg-panel p-5">
  <h2 class="mb-4 text-base font-semibold">{i18n.m.wizard.title}</h2>

  <form onsubmit={submit} autocomplete="off" class="flex flex-col gap-4">
    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <label class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.name}</span>
        <input
          bind:value={slug}
          type="text"
          pattern="[a-z0-9][a-z0-9-]*"
          placeholder="my-stream"
          required
          class="rounded-lg border border-line bg-panel-2 px-3 py-2 font-mono
                 outline-none focus:border-brand"
        />
      </label>

      <label class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.source}</span>
        <input
          bind:value={sourceUrl}
          type="text"
          placeholder="rtmp://0.0.0.0:1935/in/my-stream"
          required
          class="rounded-lg border border-line bg-panel-2 px-3 py-2 font-mono
                 outline-none focus:border-brand"
        />
      </label>

      <label class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.sourceLang}</span>
        <select
          bind:value={sourceLang}
          class="rounded-lg border border-line bg-panel-2 px-3 py-2
                 outline-none focus:border-brand"
        >
          <option value="auto">{i18n.m.wizard.autoDetect}</option>
          {#each languages as l (l.tag)}
            <option value={l.tag}>{l.label}</option>
          {/each}
        </select>
      </label>

      <div class="flex gap-4">
        <label class="flex flex-1 flex-col gap-1 text-sm">
          <span class="text-ink-dim">{i18n.m.wizard.subtitles}</span>
          <select
            bind:value={subs}
            class="rounded-lg border border-line bg-panel-2 px-3 py-2
                   outline-none focus:border-brand"
          >
            <option value="vtt">{i18n.m.wizard.subsOn}</option>
            <option value="off">{i18n.m.wizard.subsOff}</option>
          </select>
        </label>

        <label class="flex flex-col gap-1 text-sm">
          <span class="text-ink-dim">{i18n.m.wizard.dubbing}</span>
          <span class="flex h-[38px] items-center">
            <input type="checkbox" bind:checked={dub} class="h-4 w-4 accent-brand" />
          </span>
        </label>
      </div>
    </div>

    <fieldset class="rounded-lg border border-line p-3">
      <legend class="px-1 text-sm text-ink-dim">{i18n.m.wizard.targets}</legend>
      <div class="flex flex-wrap gap-2" role="group" aria-label={i18n.m.wizard.targets}>
        {#each languages as l (l.tag)}
          <button
            type="button"
            onclick={() => toggleTarget(l.tag)}
            aria-pressed={targets.includes(l.tag)}
            class={`rounded-full border px-3 py-1 text-sm transition-colors
                    focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand ${
                      targets.includes(l.tag)
                        ? "border-brand bg-brand/15 text-brand"
                        : "border-line bg-panel-2 text-ink-dim hover:border-ink-dim"
                    }`}
          >
            {l.label}
          </button>
        {/each}
      </div>
    </fieldset>

    <div class="flex flex-wrap items-center gap-3">
      <input
        value={token.value}
        oninput={(e) => token.set(e.currentTarget.value)}
        type="password"
        placeholder={i18n.m.wizard.token}
        aria-label="Control token"
        class="min-w-64 flex-1 rounded-lg border border-line bg-panel-2 px-3
               py-2 font-mono text-sm outline-none focus:border-brand"
      />
      <button
        type="submit"
        disabled={busy}
        class="rounded-lg bg-brand px-4 py-2 text-sm font-semibold text-white
               transition-colors hover:bg-brand-dim disabled:opacity-50
               focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand"
      >
        {i18n.m.wizard.create}
      </button>
      {#if error}
        <span class="text-sm text-danger" role="alert">{error}</span>
      {/if}
    </div>
  </form>
</section>
