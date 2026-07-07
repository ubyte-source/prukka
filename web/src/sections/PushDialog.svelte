<script lang="ts">
  import { ApiError, push } from "../lib/api/client";
  import { i18n } from "../lib/i18n/index.svelte";
  import type { Session } from "../lib/api/types";
  import { daemon } from "../lib/state/daemon.svelte";
  import { token } from "../lib/state/token.svelte";

  interface Props {
    session: Session;
    onclose: () => void;
  }

  const { session, onclose }: Props = $props();

  let dialog = $state<HTMLDialogElement | null>(null);

  // The dialog is mounted fresh per session (guarded by {#if} in Sessions),
  // so capturing the initial language here is the intended behaviour.
  // svelte-ignore state_referenced_locally
  let lang = $state(session.langs[0] ?? "");
  let targetUrl = $state("");
  let subs = $state("off");
  let error = $state("");
  let busy = $state(false);

  $effect(() => {
    dialog?.showModal();
  });

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    error = "";
    busy = true;

    try {
      await push(
        { slug: session.slug, lang, targetUrl: targetUrl.trim(), subs },
        token.value,
      );
      daemon.log(`pushing ${session.slug}/${lang} → ${targetUrl.trim()}`);
      onclose();
    } catch (e) {
      error = e instanceof ApiError ? e.message : i18n.m.pushDialog.failed;
    } finally {
      busy = false;
    }
  }
</script>

<dialog
  bind:this={dialog}
  onclose={onclose}
  class="m-auto w-full max-w-md rounded-xl border border-line bg-panel p-0
         text-ink backdrop:bg-black/50"
>
  <form onsubmit={submit} class="flex flex-col gap-4 p-5">
    <h3 class="text-base font-semibold">
      Push <span class="font-mono text-brand">{session.slug}</span> {i18n.m.pushDialog.title}
    </h3>

    <label class="flex flex-col gap-1 text-sm">
      <span class="text-ink-dim">{i18n.m.pushDialog.language}</span>
      <select
        bind:value={lang}
        class="rounded-lg border border-line bg-panel-2 px-3 py-2 outline-none
               focus:border-brand"
      >
        {#each session.langs as tag (tag)}
          <option value={tag}>{tag}</option>
        {/each}
      </select>
    </label>

    <label class="flex flex-col gap-1 text-sm">
      <span class="text-ink-dim">{i18n.m.pushDialog.target}</span>
      <input
        bind:value={targetUrl}
        type="text"
        required
        placeholder="rtmp://a.rtmp.youtube.com/live2/KEY"
        class="rounded-lg border border-line bg-panel-2 px-3 py-2 font-mono
               text-sm outline-none focus:border-brand"
      />
    </label>

    <label class="flex flex-col gap-1 text-sm">
      <span class="text-ink-dim">{i18n.m.pushDialog.subtitles}</span>
      <select
        bind:value={subs}
        class="rounded-lg border border-line bg-panel-2 px-3 py-2 outline-none
               focus:border-brand"
      >
        <option value="off">{i18n.m.pushDialog.subsOff}</option>
        <option value="vtt">{i18n.m.pushDialog.subsVtt}</option>
        <option value="burn">{i18n.m.pushDialog.subsBurn}</option>
      </select>
    </label>

    <div class="flex items-center justify-end gap-3">
      {#if error}
        <span class="mr-auto text-sm text-danger" role="alert">{error}</span>
      {/if}
      <button
        type="button"
        onclick={onclose}
        class="rounded-lg border border-line px-4 py-2 text-sm text-ink-dim
               hover:border-ink-dim"
      >
        {i18n.m.pushDialog.cancel}
      </button>
      <button
        type="submit"
        disabled={busy}
        class="rounded-lg bg-brand px-4 py-2 text-sm font-semibold text-white
               hover:bg-brand-dim disabled:opacity-50"
      >
        {i18n.m.pushDialog.start}
      </button>
    </div>
  </form>
</dialog>
