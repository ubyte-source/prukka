<script lang="ts">
  import { push } from "../lib/api/client";
  import Select from "../lib/components/Select.svelte";
  import { i18n } from "../lib/i18n/index.svelte";
  import type { Session } from "../lib/api/types";
  import { daemon } from "../lib/state/daemon.svelte";
  import { toasts } from "../lib/state/toasts.svelte";
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
  let targetChoice = $state("custom");
  let targetUrl = $state("");
  let subs = $state("off");
  let busy = $state(false);
  const outputDevices = $derived(
    daemon.devices.filter((device) =>
      device.kind === "audio-out" || device.kind === "video-out"
    ),
  );

  $effect(() => {
    dialog?.showModal();
  });

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    busy = true;
    const target = targetChoice === "custom" ? targetUrl.trim() : targetChoice;

    try {
      await push(
        { slug: session.slug, lang, targetUrl: target, subs },
        token.value,
      );
      daemon.log(`push started for ${session.slug}/${lang}`);
      dialog?.close();
    } catch (e) {
      toasts.failure(e, i18n.m.pushDialog.failed);
    } finally {
      busy = false;
    }
  }
</script>

<dialog
  bind:this={dialog}
  onclose={onclose}
  oncancel={(event) => {
    if (busy) event.preventDefault();
  }}
  aria-labelledby="push-dialog-title"
  aria-describedby="push-target-notice"
  class="m-auto w-full max-w-md rounded-xl border border-line bg-panel p-0
         text-ink backdrop:bg-black/50"
>
  <form onsubmit={submit} aria-busy={busy} class="flex flex-col gap-4 p-5">
    <h3 id="push-dialog-title" class="text-base font-semibold">
      {i18n.m.pushDialog.push} <span class="font-mono text-brand">{session.slug}</span>
      {i18n.m.pushDialog.title}
    </h3>

    <fieldset disabled={busy} class="contents">
    <div class="flex flex-col gap-1 text-sm">
      <span class="text-ink-dim">{i18n.m.pushDialog.language}</span>
      <Select
        bind:value={lang}
        label={i18n.m.pushDialog.language}
        options={session.langs.map((tag) => ({ value: tag, label: tag }))}
      />
    </div>

    <div class="flex flex-col gap-1 text-sm">
      <span class="text-ink-dim">{i18n.m.pushDialog.target}</span>
      {#if outputDevices.length > 0}
        <Select
          bind:value={targetChoice}
          label={i18n.m.pushDialog.targetPicker}
          options={[
            ...outputDevices.map((device) => ({ value: device.url, label: device.label })),
            { value: "custom", label: i18n.m.wizard.sourceCustom },
          ]}
        />
      {/if}
      {#if outputDevices.length === 0 || targetChoice === "custom"}
        <input
          bind:value={targetUrl}
          type="text"
          required
          spellcheck="false"
          autocapitalize="none"
          aria-label={i18n.m.pushDialog.customTarget}
          placeholder="rtmp://a.rtmp.youtube.com/live2/KEY"
          class="rounded-lg border border-line bg-panel-2 px-3 py-2 font-mono
                 text-sm outline-none focus:border-brand"
        />
      {/if}
    </div>

    <p id="push-target-notice" class="rounded-lg border border-line bg-panel-2 p-3 text-xs text-ink-dim">
      {i18n.m.pushDialog.targetNotice}
    </p>

    <div class="flex flex-col gap-1 text-sm">
      <span class="text-ink-dim">{i18n.m.pushDialog.subtitles}</span>
      <Select
        bind:value={subs}
        label={i18n.m.pushDialog.subtitles}
        options={[
          { value: "off", label: i18n.m.pushDialog.subsOff },
          { value: "burn", label: i18n.m.pushDialog.subsBurn },
        ]}
      />
    </div>

    <div class="flex items-center justify-end gap-3">
      <button
        type="button"
        disabled={busy}
        onclick={() => dialog?.close()}
        class="rounded-lg border border-line px-4 py-2 text-sm text-ink-dim
               hover:border-ink-dim"
      >
        {i18n.m.pushDialog.cancel}
      </button>
      <button
        type="submit"
        disabled={busy}
        class="rounded-lg bg-brand-dim px-4 py-2 text-sm font-semibold text-white
               hover:brightness-110 disabled:opacity-50"
      >
        {busy ? i18n.m.pushDialog.starting : i18n.m.pushDialog.start}
      </button>
    </div>
    </fieldset>
  </form>
</dialog>
