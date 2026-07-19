<script lang="ts">
  import { onDestroy, tick } from "svelte";
  import { ApiError, createSession, deleteSession, push } from "../lib/api/client";
  import type { Session } from "../lib/api/types";
  import {
    autoTranslationTargetSupported,
    sameBaseLanguage,
    translationSupported,
  } from "../lib/capabilities";
  import Select from "../lib/components/Select.svelte";
  import { i18n } from "../lib/i18n/index.svelte";
  import { daemon } from "../lib/state/daemon.svelte";
  import { toasts } from "../lib/state/toasts.svelte";
  import { isControlToken, token } from "../lib/state/token.svelte";

  const languages = $derived(daemon.languages);
  const setupLoaded = $derived(
    daemon.languagesLoaded && daemon.devicesLoaded && daemon.configLoaded,
  );
  const setupFailed = $derived(
    setupLoaded && (daemon.languagesError || daemon.configError || languages.length === 0),
  );
  const ready = $derived(setupLoaded && !setupFailed);
  // Real device names beat raw device:// URLs; an empty enumeration
  // simply keeps the manual field.
  const captureDevices = $derived(
    daemon.devices.filter((d) => d.kind === "audio-in"),
  );
  const audioOutputDevices = $derived(
    daemon.devices.filter((d) => d.kind === "audio-out"),
  );
  const videoOutputDevices = $derived(
    daemon.devices.filter((d) => d.kind === "video-out"),
  );
  const cameraDevices = $derived(
    daemon.devices.filter((d) => d.kind === "video-in"),
  );
  const sourceIsCamera = $derived(
    cameraDevices.some((d) => d.url === sourceChoice),
  );

  let slug = $state("");
  let profile = $state("");
  let step = $state(1);
  let sourceChoice = $state("custom");
  let sourceUrl = $state("");
  let pairedMic = $state("");
  let output = $state("");
  let sourceLang = $state("auto");
  let subs = $state("");
  let dub = $state(true);
  let targets = $state<string[]>([]);
  let captionTargets = $state<string[]>([]);
  // A call runs as two audio lanes: youLang is what you speak, themLang is
  // what the other party speaks. callMic captures your voice; callOutgoing is
  // the virtual mic the call app reads.
  let youLang = $state("");
  let themLang = $state("");
  let callMic = $state("");
  let callOutgoing = $state("");
  let busy = $state(false);
  let stepHeading = $state<HTMLHeadingElement | null>(null);
  const outputDevices = $derived(videoOutputDevices);
  const voiceEnabled = $derived(daemon.config.providers?.voices !== "off");
  const translationPairs = $derived(daemon.config.providers?.local?.mt?.pairs ?? []);
  // The daemon reports the full set of languages the local voices can dub.
  const dubbedLangs = $derived(
    voiceEnabled ? (daemon.config.providers?.local?.dubbedLangs ?? []) : [],
  );
  const dubbedLangLabels = $derived(
    dubbedLangs
      .map((tag) => languages.find((language) => sameBaseLanguage(language.tag, tag))?.label ?? tag)
      .join(", "),
  );

  // A two-way call needs both directions; the incoming-only fallback needs the
  // remote → you direction and a voice for the language you hear.
  const callDiffers = $derived(
    youLang !== "" && themLang !== "" && !sameBaseLanguage(youLang, themLang),
  );
  const forwardMT = $derived(
    callDiffers && translationSupported(translationPairs, themLang, youLang),
  );
  const reverseMT = $derived(
    callDiffers && translationSupported(translationPairs, youLang, themLang),
  );
  const youDub = $derived(voiceSupports(youLang));
  const themDub = $derived(voiceSupports(themLang));
  const twoWayAvailable = $derived(callDiffers && forwardMT && reverseMT && youDub && themDub);
  const oneWayAvailable = $derived(callDiffers && forwardMT && youDub);
  const callSubmittable = $derived(twoWayAvailable || oneWayAvailable);

  const pushReadyTimeoutMs = 30_000;
  const pushReadyInitialDelayMs = 100;
  const pushReadyMaxDelayMs = 1_000;
  let routingAbort: AbortController | null = null;

  onDestroy(() => routingAbort?.abort());

  $effect(() => {
    if (!ready || profile !== "") return;

    const available = new Set(languages.map((language) => language.tag));
    const configured = (daemon.config.defaults?.langs ?? []).filter((tag) => available.has(tag));
    dub = voiceEnabled;
    targets = configured.filter((tag) =>
      translationTargetSupported(tag) && (!dub || voiceSupports(tag))
    );
    captionTargets = dub
      ? configured.filter((tag) => translationTargetSupported(tag) && !voiceSupports(tag))
      : [];

    const configuredSubs = daemon.config.defaults?.subs ?? "vtt";
    subs = ["off", "vtt", "burn"].includes(configuredSubs) ? configuredSubs : "vtt";

  });

  // A call runs as two lanes; auto-detect the canonical devices for both while
  // leaving each field user-overridable. Their voice arrives on the virtual
  // speaker; your translated voice leaves through the virtual microphone.
  // Only untouched (empty) roles are filled so a manual choice survives the
  // periodic device refresh.
  function applyCallDefaults() {
    const remote = captureDevices.find((device) => device.label.includes("Prukka Speaker"));
    const listen = audioOutputDevices.find((device) => !device.virtual);
    const mic = captureDevices.find((device) => !device.virtual);
    const outgoing = audioOutputDevices.find((device) => device.label.includes("Prukka Microphone"));
    if (remote && sourceChoice === "custom" && sourceUrl === "") sourceChoice = remote.url;
    if (listen && output === "") output = listen.url;
    if (mic && callMic === "") callMic = mic.url;
    if (outgoing && callOutgoing === "") callOutgoing = outgoing.url;
    dub = voiceEnabled;
  }

  // Seed the call's two languages from the daemon's dubbing capability and
  // installed routes: you hear a language a voice can synthesize, and the other
  // side speaks a language that translates into it.
  function seedCallLanguages() {
    if (youLang === "") {
      youLang = languages.find((language) => voiceSupports(language.tag))?.tag ?? "";
    }
    if (themLang === "" && youLang !== "") {
      const candidates = [
        ...(daemon.config.defaults?.langs ?? []),
        ...languages.map((language) => language.tag),
      ];
      themLang =
        candidates.find(
          (tag) =>
            !sameBaseLanguage(tag, youLang) && translationSupported(translationPairs, tag, youLang),
        ) ??
        candidates.find((tag) => !sameBaseLanguage(tag, youLang)) ??
        "";
    }
  }

  $effect(() => {
    if (ready && profile === "call") seedCallLanguages();
  });

  function chooseProfile(value: string) {
    profile = value;
    sourceChoice = "custom";
    sourceUrl = "";
    pairedMic = "";
    output = "";
    callMic = "";
    callOutgoing = "";
    youLang = "";
    themLang = "";
    if (value === "call") applyCallDefaults();
    moveToStep(2);
  }

  function moveToStep(next: number) {
    step = next;
    void tick().then(() => stepHeading?.focus());
  }

  // Devices can appear after load (OS capture consent, hotplug): while the
  // device step is open, keep the list fresh and complete the call roles that
  // are still empty as soon as the canonical devices show up.
  $effect(() => {
    if (step !== 2) return;
    const timer = setInterval(() => {
      daemon.refreshDevices(token.value);
      if (profile === "call") applyCallDefaults();
    }, 4_000);
    return () => clearInterval(timer);
  });

  function continueToLanguages(form: HTMLFormElement) {
    if (!form.reportValidity()) return;
    if (sourceIsCamera && pairedMic === "") {
      toasts.error(i18n.m.wizard.needMic);
      return;
    }
    moveToStep(3);
  }

  function toggleTarget(tag: string) {
    if (!translationTargetSupported(tag) || (dub && !voiceSupports(tag))) return;
    targets = targets.includes(tag)
      ? targets.filter((t) => t !== tag)
      : [...targets, tag];
    if (targets.includes(tag)) captionTargets = captionTargets.filter((target) => target !== tag);
  }

  function toggleCaptionTarget(tag: string) {
    if (!translationTargetSupported(tag)) return;
    captionTargets = captionTargets.includes(tag)
      ? captionTargets.filter((target) => target !== tag)
      : [...captionTargets, tag];
    if (captionTargets.includes(tag)) targets = targets.filter((target) => target !== tag);
  }

  function sessionTargets() {
    return [...new Set([...targets, ...captionTargets])];
  }

  function voiceSupports(tag: string) {
    return dubbedLangs.some((language) => sameBaseLanguage(tag, language));
  }

  function languageLabel(tag: string) {
    return languages.find((language) => language.tag === tag)?.label ?? tag;
  }

  function translationTargetSupportedFor(source: string, target: string) {
    if (source === "auto") {
      return autoTranslationTargetSupported(translationPairs, target);
    }
    return translationSupported(translationPairs, source, target);
  }

  function translationTargetSupported(tag: string) {
    return translationTargetSupportedFor(sourceLang, tag);
  }

  function setSourceLanguage(value: string) {
    sourceLang = value;
    pruneTargetsForSource(value);
  }

  function pruneTargetsForSource(source: string) {
    targets = targets.filter((tag) =>
      translationTargetSupportedFor(source, tag) && (!dub || voiceSupports(tag))
    );
    captionTargets = captionTargets.filter((tag) => translationTargetSupportedFor(source, tag));
  }

  function targetSuffix(tag: string, requireVoice: boolean) {
    if (!translationTargetSupported(tag)) return i18n.m.wizard.translationUnavailable;
    if (requireVoice && !voiceSupports(tag)) return i18n.m.wizard.captionOnly;
    return "";
  }

  function voiceMessage(message: string) {
    const capability = voiceEnabled
      ? (dubbedLangLabels || i18n.m.wizard.unknownVoice)
      : i18n.m.wizard.disabledVoice;
    return message.replace("{language}", capability);
  }

  function sourceMessage(message: string) {
    return message.replace("{source}", languageLabel(sourceLang));
  }

  function reasonNoRoute(from: string, to: string) {
    return i18n.m.wizard.reasonNoRoute
      .replace("{from}", languageLabel(from))
      .replace("{to}", languageLabel(to));
  }

  function reasonNoVoice(lang: string) {
    return i18n.m.wizard.reasonNoVoice.replace("{lang}", languageLabel(lang));
  }

  // The call note is honest about which direction, if any, is missing and why.
  function callReadyNote() {
    return i18n.m.wizard.twoWayReady
      .replace("{them}", languageLabel(themLang))
      .replace("{you}", languageLabel(youLang));
  }

  function callFallbackNote() {
    const reason = !reverseMT ? reasonNoRoute(youLang, themLang) : reasonNoVoice(themLang);
    return i18n.m.wizard.twoWayUnavailable
      .replace("{reason}", reason)
      .replace("{you}", languageLabel(youLang));
  }

  function callUnavailableNote() {
    const reason = !youDub ? reasonNoVoice(youLang) : reasonNoRoute(themLang, youLang);
    return i18n.m.wizard.callUnavailable.replace("{reason}", reason);
  }


  function setDubbing(enabled: boolean) {
    if (!voiceEnabled) {
      dub = false;
      return;
    }
    dub = enabled;
    if (!enabled) return;

    const unsupported = targets.filter((tag) => !voiceSupports(tag));
    targets = targets.filter(voiceSupports);
    captionTargets = [...new Set([...captionTargets, ...unsupported])];
  }

  function assertEffectiveVoice(session: Session, expected: string) {
    if ((session.effectiveDubbedLangs ?? []).includes(expected)) return;
    throw new Error(voiceMessage(i18n.m.wizard.voiceUnavailable));
  }

  async function routeWhenReady(
    name: string,
    lang: string,
    targetUrl: string,
    signal: AbortSignal,
  ) {
    const readiness = new AbortController();
    const cancel = () => readiness.abort(signal.reason);
    if (signal.aborted) cancel();
    else signal.addEventListener("abort", cancel, { once: true });
    const timeout = setTimeout(
      () => readiness.abort(new Error(i18n.m.wizard.routeTimeout)),
      pushReadyTimeoutMs,
    );
    const deadline = performance.now() + pushReadyTimeoutMs;
    let delay = pushReadyInitialDelayMs;
    try {
      for (;;) {
        try {
          await push(
            { slug: name, lang, targetUrl, subs: "off" },
            token.value,
            readiness.signal,
          );
          return;
        } catch (e) {
          const starting = e instanceof ApiError && e.status === 503;
          const remaining = deadline - performance.now();
          if (!starting || remaining <= 0) throw e;
          await retryDelay(Math.min(delay, remaining), readiness.signal);
          delay = Math.min(delay * 2, pushReadyMaxDelayMs);
        }
      }
    } finally {
      clearTimeout(timeout);
      signal.removeEventListener("abort", cancel);
    }
  }

  function retryDelay(ms: number, signal: AbortSignal) {
    return new Promise<void>((resolve, reject) => {
      if (signal.aborted) {
        reject(signal.reason);
        return;
      }
      const timer = setTimeout(done, ms);
      signal.addEventListener("abort", aborted, { once: true });

      function done() {
        signal.removeEventListener("abort", aborted);
        resolve();
      }

      function aborted() {
        clearTimeout(timer);
        reject(signal.reason);
      }
    });
  }

  async function rollbackSessions(created: string[], cause: unknown): Promise<never> {
    const failures: unknown[] = [];
    const failedSlugs: string[] = [];
    for (const createdSlug of [...created].reverse()) {
      try {
        await deleteSession(createdSlug, token.value);
      } catch (failure) {
        if (failure instanceof ApiError && failure.status === 404) continue;
        failures.push(failure);
        failedSlugs.push(createdSlug);
      }
    }
    if (failures.length > 0) {
      throw new AggregateError(
        [cause, ...failures],
        `session rollback failed for ${failedSlugs.join(", ")}`,
      );
    }
    throw cause;
  }

  function resetWizard() {
    slug = "";
    profile = "";
    moveToStep(1);
    sourceChoice = "custom";
    sourceUrl = "";
    pairedMic = "";
    output = "";
    callMic = "";
    callOutgoing = "";
    youLang = "";
    themLang = "";
    sourceLang = "auto";
    dub = true;
    targets = [];
    captionTargets = [];
  }

  function submit(event: SubmitEvent) {
    event.preventDefault();
    // Enter inside an earlier step's field advances the wizard; only the
    // final step creates sessions.
    if (step === 2) {
      continueToLanguages(event.currentTarget as HTMLFormElement);
      return;
    }
    if (step !== 3) return;
    if (profile === "call") {
      void submitCall();
      return;
    }
    void submitBroadcast();
  }

  async function submitBroadcast() {
    const allTargets = sessionTargets();
    if (allTargets.length === 0) {
      toasts.error(i18n.m.wizard.needTarget);
      return;
    }
    const flags: Record<string, string> = { subs };
    if (sourceLang !== "auto") flags.source = sourceLang;
    if (dub) flags.dub_langs = targets.join(",");
    else flags.dub = "off";

    if (sourceIsCamera && pairedMic === "") {
      toasts.error(i18n.m.wizard.needMic);
      return;
    }

    // A camera pairs with a microphone: device://av/<camera>|<mic>, ids
    // taken from the enumerated URLs.
    const deviceId = (url: string) => url.replace(/^device:\/\/(audio|video)\//, "");
    const source = sourceIsCamera
      ? `device://av/${deviceId(sourceChoice)}|${deviceId(pairedMic)}`
      : sourceChoice === "custom"
        ? sourceUrl.trim()
        : sourceChoice;
    const name = slug.trim();

    await withSubmission(async (signal) => {
      const created = await createSession(
        { slug: name, profile, sourceUrl: source, langs: allTargets, flags },
        token.value,
      );

      try {
        if (dub) {
          for (const target of targets) assertEffectiveVoice(created, target);
        }

        const routedOutput = outputDevices.some((device) => device.url === output)
          ? output
          : "";
        const routedLanguage = allTargets[0];
        if (routedOutput !== "" && routedLanguage !== undefined) {
          await routeWhenReady(name, routedLanguage, routedOutput, signal);
        }
      } catch (cause) {
        await rollbackSessions([name], cause);
      }
    });
  }

  async function submitCall() {
    const name = slug.trim();
    const remote = sourceChoice === "custom" ? sourceUrl.trim() : sourceChoice;
    // Validation failures return before anything is created, keeping the form
    // intact for correction.
    if (!callSubmittable) {
      toasts.error(callUnavailableNote());
      return;
    }
    if (remote === "") {
      toasts.error(i18n.m.wizard.remoteSourceRequired);
      return;
    }
    if (twoWayAvailable) {
      // Both lanes need every device role resolved: an empty source or target
      // would only fail after the first session already exists.
      if (callMic === "" || output === "" || callOutgoing === "") {
        toasts.error(i18n.m.wizard.needCallDevices);
        return;
      }
      // The "-in"/"-out" suffixes must keep both slugs within the server's
      // 63-character limit.
      if (name.length > 59) {
        toasts.error(i18n.m.wizard.nameTooLong);
        return;
      }
      await withSubmission((signal) => createTwoWayCall(name, remote, signal));
      return;
    }
    await withSubmission((signal) => createOneWayCall(name, remote, signal));
  }

  // Two lanes, one atomic transaction: create both, confirm both voices, then
  // route. A failure at any step rolls both lanes back.
  async function createTwoWayCall(name: string, remote: string, signal: AbortSignal) {
    const inSlug = `${name}-in`;
    const outSlug = `${name}-out`;
    const created: string[] = [];
    try {
      const incoming = await createSession(
        {
          slug: inSlug,
          profile: "call",
          sourceUrl: remote,
          langs: [youLang],
          flags: { subs, source: themLang, dub_langs: youLang, pair: outSlug },
        },
        token.value,
      );
      created.push(inSlug);
      assertEffectiveVoice(incoming, youLang);

      const outgoing = await createSession(
        {
          slug: outSlug,
          profile: "call",
          sourceUrl: callMic,
          langs: [themLang],
          flags: { subs, source: youLang, dub_langs: themLang, pair: inSlug },
        },
        token.value,
      );
      created.push(outSlug);
      assertEffectiveVoice(outgoing, themLang);

      await routeWhenReady(inSlug, youLang, output, signal);
      await routeWhenReady(outSlug, themLang, callOutgoing, signal);
    } catch (cause) {
      await rollbackSessions(created, cause);
    }
  }

  // Incoming-only fallback: hear the other side in your language on your output.
  async function createOneWayCall(name: string, remote: string, signal: AbortSignal) {
    const created: string[] = [];
    try {
      const incoming = await createSession(
        {
          slug: name,
          profile: "call",
          sourceUrl: remote,
          langs: [youLang],
          flags: { subs, source: themLang, dub_langs: youLang },
        },
        token.value,
      );
      created.push(name);
      assertEffectiveVoice(incoming, youLang);

      // Routing stays optional for the incoming-only flow: an empty output
      // defers it to the operator.
      const listen = audioOutputDevices.some((device) => device.url === output) ? output : "";
      if (listen !== "") await routeWhenReady(name, youLang, listen, signal);
    } catch (cause) {
      await rollbackSessions(created, cause);
    }
  }

  // Shared submission scaffolding: a single in-flight controller, a daemon
  // refresh and a worded failure toast.
  async function withSubmission(run: (signal: AbortSignal) => Promise<void>) {
    busy = true;
    const controller = new AbortController();
    routingAbort?.abort();
    routingAbort = controller;
    try {
      await run(controller.signal);
      resetWizard();
      await daemon.refresh();
    } catch (e) {
      await daemon.refresh();
      toasts.failure(e, i18n.m.wizard.createFailed);
    } finally {
      if (routingAbort === controller) routingAbort = null;
      busy = false;
    }
  }
</script>

<section aria-labelledby="wizard-title" class="rounded-xl border border-line bg-panel p-5">
  <h2 id="wizard-title" class="mb-4 text-base font-semibold">{i18n.m.wizard.title}</h2>

  <form
    onsubmit={submit}
    autocomplete="off"
    aria-busy={busy}
    class="flex flex-col gap-4"
  >
    <fieldset disabled={busy} class="contents">
    {#if step === 1}
      <h3 bind:this={stepHeading} tabindex="-1" class="text-sm font-medium text-ink-dim">
        {i18n.m.wizard.chooseUseCase}
      </h3>
      <label class="flex max-w-xl flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.tokenLabel}</span>
        <input
          value={token.value}
          oninput={(event) => token.set(event.currentTarget.value)}
          type="password"
          autocomplete="off"
          spellcheck="false"
          autocapitalize="none"
          maxlength="64"
          placeholder={i18n.m.wizard.token}
          class="rounded-lg border border-line bg-panel-2 px-3 py-2 font-mono
                 outline-none focus:border-brand"
        />
      </label>
      {#if !isControlToken(token.value)}
        <p class="text-sm text-ink-dim">{i18n.m.wizard.tokenRequired}</p>
      {:else if setupFailed}
        <div role="alert" class="flex flex-wrap items-center gap-3 text-sm text-danger">
          <p>{i18n.m.wizard.unavailable}</p>
          <button
            type="button"
            onclick={() => daemon.reloadSetup(token.value)}
            class="rounded-lg border border-line px-3 py-1.5 text-ink hover:border-brand"
          >
            {i18n.m.wizard.retry}
          </button>
        </div>
      {:else if !ready}
        <p class="text-sm text-ink-dim" role="status">{i18n.m.wizard.loading}</p>
      {/if}
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <button
          type="button"
          disabled={!ready}
          onclick={() => chooseProfile("call")}
          class="rounded-lg border border-line bg-panel-2 p-4 text-left hover:border-brand
                 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand
                 disabled:cursor-wait disabled:opacity-50"
        >
          <span class="block font-semibold">{i18n.m.wizard.profileCall}</span>
          <span class="mt-1 block text-sm text-ink-dim">{i18n.m.wizard.callDesc}</span>
        </button>
        <button
          type="button"
          disabled={!ready}
          onclick={() => chooseProfile("broadcast")}
          class="rounded-lg border border-line bg-panel-2 p-4 text-left hover:border-brand
                 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand
                 disabled:cursor-wait disabled:opacity-50"
        >
          <span class="block font-semibold">{i18n.m.wizard.profileBroadcast}</span>
          <span class="mt-1 block text-sm text-ink-dim">{i18n.m.wizard.broadcastDesc}</span>
        </button>
      </div>
    {:else if step === 2}
      <h3 bind:this={stepHeading} tabindex="-1" class="text-sm font-medium text-ink-dim">
        {i18n.m.wizard.devicesStep}
      </h3>
      {#if daemon.devicesError}
        <p role="status" class="rounded-lg border border-line bg-panel-2 p-3 text-sm text-warn">
          {i18n.m.wizard.devicesUnavailable}
        </p>
      {/if}
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
      {#if profile === "call"}
        <div class="flex flex-col gap-2 sm:col-span-2 lg:col-span-4">
          <p class="rounded-lg border border-line bg-panel-2 p-3 text-sm text-ink-dim">
            {i18n.m.wizard.twoWayIntro}
          </p>
          <p class="rounded-lg border border-line bg-panel-2 p-3 text-sm text-ink-dim">
            {i18n.m.wizard.callHowTo}
          </p>
        </div>
      {/if}
      <label class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.name}</span>
        <!-- A call appends "-in"/"-out"; both slugs must fit the 63-char limit. -->
        <input
          bind:value={slug}
          type="text"
          pattern="[a-z0-9](?:[a-z0-9-]*[a-z0-9])?"
          maxlength={profile === "call" ? 59 : 63}
          placeholder="my-stream"
          required
          class="rounded-lg border border-line bg-panel-2 px-3 py-2 font-mono
                 outline-none focus:border-brand"
        />
      </label>

      {#if profile === "call"}
        <div class="flex flex-col gap-1 text-sm">
          <span class="text-ink-dim">{i18n.m.wizard.remoteSource}</span>
          {#if captureDevices.length > 0}
            <Select
              bind:value={sourceChoice}
              label={i18n.m.wizard.sourcePicker}
              options={[
                ...captureDevices.map((d) => ({ value: d.url, label: d.label })),
                { value: "custom", label: i18n.m.wizard.sourceCustom },
              ]}
            />
          {/if}
          {#if captureDevices.length === 0 || sourceChoice === "custom"}
            <input
              bind:value={sourceUrl}
              type="text"
              spellcheck="false"
              autocapitalize="none"
              placeholder="device://audio/<id>"
              required
              aria-label={i18n.m.wizard.remoteSource}
              class="rounded-lg border border-line bg-panel-2 px-3 py-2 font-mono
                     outline-none focus:border-brand"
            />
          {/if}
          <span class="text-xs text-ink-dim">{i18n.m.wizard.remoteSourceHint}</span>
        </div>

        {#if audioOutputDevices.length > 0}
          <div class="flex flex-col gap-1 text-sm">
            <span class="text-ink-dim">{i18n.m.wizard.listenOutput}</span>
            <Select
              bind:value={output}
              label={i18n.m.wizard.listenOutput}
              options={[
                { value: "", label: i18n.m.wizard.outputNone },
                ...audioOutputDevices.map((d) => ({ value: d.url, label: d.label })),
              ]}
            />
            <span class="text-xs text-ink-dim">{i18n.m.wizard.listenOutputHint}</span>
          </div>
        {/if}

        {#if captureDevices.length > 0}
          <div class="flex flex-col gap-1 text-sm">
            <span class="text-ink-dim">{i18n.m.wizard.yourMic}</span>
            <Select
              bind:value={callMic}
              label={i18n.m.wizard.yourMic}
              options={[
                { value: "", label: i18n.m.wizard.outputNone },
                ...captureDevices.map((d) => ({ value: d.url, label: d.label })),
              ]}
            />
            <span class="text-xs text-ink-dim">{i18n.m.wizard.yourMicHint}</span>
          </div>
        {/if}

        {#if audioOutputDevices.length > 0}
          <div class="flex flex-col gap-1 text-sm">
            <span class="text-ink-dim">{i18n.m.wizard.outgoing}</span>
            <Select
              bind:value={callOutgoing}
              label={i18n.m.wizard.outgoing}
              options={[
                { value: "", label: i18n.m.wizard.outputNone },
                ...audioOutputDevices.map((d) => ({ value: d.url, label: d.label })),
              ]}
            />
            <span class="text-xs text-ink-dim">{i18n.m.wizard.outgoingHint}</span>
          </div>
        {/if}
      {:else}
        <div class="flex flex-col gap-1 text-sm">
          <span class="text-ink-dim">{i18n.m.wizard.source}</span>
          {#if captureDevices.length > 0}
            <Select
              bind:value={sourceChoice}
              label={i18n.m.wizard.sourcePicker}
              options={[
                ...captureDevices.map((d) => ({ value: d.url, label: d.label })),
                ...cameraDevices.map((device) => ({ value: device.url, label: device.label })),
                { value: "custom", label: i18n.m.wizard.sourceCustom },
              ]}
            />
          {/if}
          {#if captureDevices.length === 0 || sourceChoice === "custom"}
            <input
              bind:value={sourceUrl}
              type="text"
              spellcheck="false"
              autocapitalize="none"
              placeholder="rtmp://0.0.0.0:1935/in/my-stream"
              required
              aria-label={i18n.m.wizard.source}
              class="rounded-lg border border-line bg-panel-2 px-3 py-2 font-mono
                     outline-none focus:border-brand"
            />
          {/if}
        </div>

        {#if sourceIsCamera && captureDevices.length > 0}
          <div class="flex flex-col gap-1 text-sm">
            <span class="text-ink-dim">{i18n.m.wizard.pairedMic}</span>
            <Select
              bind:value={pairedMic}
              label={i18n.m.wizard.pairedMic}
              options={[
                { value: "", label: "—" },
                ...captureDevices.map((d) => ({ value: d.url, label: d.label })),
              ]}
            />
          </div>
        {/if}

        {#if outputDevices.length > 0}
          <div class="flex flex-col gap-1 text-sm">
            <span class="text-ink-dim">{i18n.m.wizard.videoOutput}</span>
            <Select
              bind:value={output}
              label={i18n.m.wizard.videoOutput}
              options={[
                { value: "", label: i18n.m.wizard.outputNone },
                ...outputDevices.map((d) => ({ value: d.url, label: d.label })),
              ]}
            />
          </div>
        {/if}
      {/if}

    </div>

      <div class="flex items-center justify-between gap-3">
        <button
          type="button"
          onclick={() => moveToStep(1)}
          class="rounded-lg border border-line px-4 py-2 text-sm text-ink-dim hover:border-ink-dim"
        >
          {i18n.m.wizard.back}
        </button>
        <button
          type="button"
          onclick={(event) => continueToLanguages(event.currentTarget.form!)}
          class="rounded-lg bg-brand-dim px-4 py-2 text-sm font-semibold text-white hover:brightness-110"
        >
          {i18n.m.wizard.next}
        </button>
      </div>
    {:else}
      <h3 bind:this={stepHeading} tabindex="-1" class="text-sm font-medium text-ink-dim">
        {i18n.m.wizard.languagesStep}
      </h3>
      {#if profile === "call"}
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div class="flex flex-col gap-1 text-sm">
          <span class="text-ink-dim">{i18n.m.wizard.youLang}</span>
          <Select
            bind:value={youLang}
            label={i18n.m.wizard.youLang}
            options={languages.map((l) => ({ value: l.tag, label: l.label }))}
          />
        </div>
        <div class="flex flex-col gap-1 text-sm">
          <span class="text-ink-dim">{i18n.m.wizard.themLang}</span>
          <Select
            bind:value={themLang}
            label={i18n.m.wizard.themLang}
            options={languages.map((l) => ({ value: l.tag, label: l.label }))}
          />
        </div>
        <div class="flex flex-col gap-1 text-sm sm:col-span-2">
          <span class="text-ink-dim">{i18n.m.wizard.subtitles}</span>
          <Select
            bind:value={subs}
            label={i18n.m.wizard.subtitles}
            options={[
              { value: "vtt", label: i18n.m.wizard.subsOn },
              { value: "off", label: i18n.m.wizard.subsOff },
              { value: "burn", label: i18n.m.wizard.subsBurn },
            ]}
          />
        </div>
      </div>

      {#if !voiceEnabled}
        <p role="status" class="rounded-lg border border-line bg-panel-2 p-3 text-sm text-warn">
          {i18n.m.wizard.dubCapabilityOff}
        </p>
      {:else if dubbedLangs.length === 0}
        <p role="status" class="rounded-lg border border-line bg-panel-2 p-3 text-sm text-warn">
          {i18n.m.wizard.dubCapabilityUnknown}
        </p>
      {:else if !callDiffers}
        <p role="status" class="rounded-lg border border-line bg-panel-2 p-3 text-sm text-ink-dim">
          {i18n.m.wizard.callSameLang}
        </p>
      {:else if twoWayAvailable}
        <p role="status" class="rounded-lg border border-line bg-panel-2 p-3 text-sm text-ink-dim">
          {callReadyNote()}
        </p>
      {:else if oneWayAvailable}
        <p role="status" class="rounded-lg border border-line bg-panel-2 p-3 text-sm text-warn">
          {callFallbackNote()}
        </p>
      {:else}
        <p role="status" class="rounded-lg border border-line bg-panel-2 p-3 text-sm text-warn">
          {callUnavailableNote()}
        </p>
      {/if}
      {:else}
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <div class="flex flex-col gap-1 text-sm">
        <span class="text-ink-dim">{i18n.m.wizard.sourceLang}</span>
        <Select
          bind:value={sourceLang}
          onchange={setSourceLanguage}
          label={i18n.m.wizard.sourceLang}
          options={[
            { value: "auto", label: i18n.m.wizard.autoDetect },
            ...languages.map((l) => ({ value: l.tag, label: l.label })),
          ]}
        />
      </div>

      <div class="flex flex-wrap gap-4">
        <div class="flex min-w-0 flex-1 flex-col gap-1 text-sm">
          <span class="text-ink-dim">{i18n.m.wizard.subtitles}</span>
          <Select
            bind:value={subs}
            label={i18n.m.wizard.subtitles}
            options={[
              { value: "vtt", label: i18n.m.wizard.subsOn },
              { value: "off", label: i18n.m.wizard.subsOff },
              { value: "burn", label: i18n.m.wizard.subsBurn },
            ]}
          />
        </div>

        <label class="flex flex-col gap-1 text-sm">
          <span class="text-ink-dim">{i18n.m.wizard.dubbing}</span>
          <span class="flex h-[38px] items-center">
            <input
              type="checkbox"
              aria-label={i18n.m.wizard.dubbing}
              checked={dub}
              onchange={(event) => setDubbing(event.currentTarget.checked)}
              disabled={!voiceEnabled}
              class="h-4 w-4 accent-brand disabled:opacity-50"
            />
          </span>
          {#if !voiceEnabled}
            <span class="text-xs text-ink-dim">{i18n.m.wizard.dubCapabilityOff}</span>
          {/if}
        </label>
      </div>

      <p id="translation-capability" class="text-xs text-ink-dim sm:col-span-2">
        {sourceLang === "auto"
          ? i18n.m.wizard.translationAuto
          : sourceMessage(i18n.m.wizard.translationConcrete)}
      </p>
    </div>

    <fieldset class="rounded-lg border border-line p-3">
      <legend class="px-1 text-sm text-ink-dim">
        {dub ? i18n.m.wizard.dubTargets : i18n.m.wizard.targets}
      </legend>
      {#if dub}
        <p id="dub-capability" class="mb-2 text-xs text-ink-dim">
          {!voiceEnabled
            ? i18n.m.wizard.dubCapabilityOff
            : dubbedLangs.length === 0
              ? i18n.m.wizard.dubCapabilityUnknown
            : voiceMessage(i18n.m.wizard.dubCapability)}
        </p>
      {/if}
      <div
        class="flex max-h-44 flex-wrap gap-2 overflow-y-auto"
        role="group"
        aria-label={dub ? i18n.m.wizard.dubTargets : i18n.m.wizard.targets}
      >
        {#each languages as l (l.tag)}
          {@const suffix = targetSuffix(l.tag, dub)}
          <button
            type="button"
            onclick={() => toggleTarget(l.tag)}
            aria-pressed={targets.includes(l.tag)}
            aria-describedby={dub
              ? "translation-capability dub-capability"
              : "translation-capability"}
            disabled={!translationTargetSupported(l.tag) || (dub && !voiceSupports(l.tag))}
            class={`rounded-full border px-3 py-1 text-sm transition-colors
                    focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand ${
                      targets.includes(l.tag)
                        ? "border-brand bg-brand/15 text-brand"
                        : "border-line bg-panel-2 text-ink-dim hover:border-ink-dim disabled:cursor-not-allowed disabled:opacity-55"
                    }`}
          >
            {#if targets.includes(l.tag)}<span aria-hidden="true">✓ </span>{/if}
            {l.label}{suffix ? ` · ${suffix}` : ""}
          </button>
        {/each}
      </div>
    </fieldset>

    <fieldset class="rounded-lg border border-line p-3">
      <legend class="px-1 text-sm text-ink-dim">{i18n.m.wizard.captionTargets}</legend>
      <p class="mb-2 text-xs text-ink-dim">{i18n.m.wizard.captionTargetsDesc}</p>
      <div
        class="flex max-h-44 flex-wrap gap-2 overflow-y-auto"
        role="group"
        aria-label={i18n.m.wizard.captionTargets}
      >
        {#each languages as language (language.tag)}
          {@const suffix = targetSuffix(language.tag, false)}
          <button
            type="button"
            onclick={() => toggleCaptionTarget(language.tag)}
            aria-pressed={captionTargets.includes(language.tag)}
            aria-describedby="translation-capability"
            disabled={!translationTargetSupported(language.tag)}
            class={`rounded-full border px-3 py-1 text-sm transition-colors
                    focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand ${
                      captionTargets.includes(language.tag)
                        ? "border-brand bg-brand/15 text-brand"
                        : "border-line bg-panel-2 text-ink-dim hover:border-ink-dim disabled:cursor-not-allowed disabled:opacity-55"
                    }`}
          >
            {#if captionTargets.includes(language.tag)}<span aria-hidden="true">✓ </span>{/if}
            {language.label}{suffix ? ` · ${suffix}` : ""}
          </button>
        {/each}
      </div>
    </fieldset>
    {/if}

    <p class="rounded-lg border border-line bg-panel-2 p-3 text-xs text-ink-dim">
      {i18n.m.wizard.participantNotice}
    </p>

    <div class="flex flex-wrap items-center gap-3">
      <button
        type="button"
        onclick={() => moveToStep(2)}
        class="rounded-lg border border-line px-4 py-2 text-sm text-ink-dim hover:border-ink-dim"
      >
        {i18n.m.wizard.back}
      </button>
      <label class="flex min-w-0 flex-1 flex-col gap-1">
        <span class="text-xs text-ink-dim">{i18n.m.wizard.tokenLabel}</span>
        <input
          value={token.value}
          oninput={(e) => token.set(e.currentTarget.value)}
          type="password"
          autocomplete="off"
          spellcheck="false"
          autocapitalize="none"
          maxlength="64"
          placeholder={i18n.m.wizard.token}
          class="w-full rounded-lg border border-line bg-panel-2 px-3 py-2
                 font-mono text-sm outline-none focus:border-brand"
        />
      </label>
      <button
        type="submit"
        disabled={busy || (profile === "call" && !callSubmittable)}
        class="rounded-lg bg-brand-dim px-4 py-2 text-sm font-semibold text-white
               transition-colors hover:brightness-110 disabled:opacity-50
               focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand"
      >
        {busy ? i18n.m.wizard.creating : i18n.m.wizard.create}
      </button>
    </div>
    {/if}
    </fieldset>
  </form>
</section>
