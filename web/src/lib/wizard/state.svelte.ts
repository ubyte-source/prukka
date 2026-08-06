// The guided wizard's whole state and behavior in one class. The shell
// (sections/Wizard.svelte) instantiates it per mount and runs the lifecycle
// effects; each step component receives it as a prop and renders one slice.

import { ApiError, createSession, deleteSession, push } from "../api/client";
import type { Session } from "../api/types";
import {
  autoTranslationTargetSupported,
  sameBaseLanguage,
  translationSupported,
} from "../capabilities";
import {
  languagePlans,
  packsToAdd,
  planForLanguage,
  totalSizeBytes,
  type LanguagePlan,
} from "../enginePacks";
import { i18n } from "../i18n/index.svelte";
import { daemon } from "../state/daemon.svelte";
import { toasts } from "../state/toasts.svelte";
import { isControlToken, token } from "../state/token.svelte";
import {
  avSourceUrl,
  detectCallRoles,
  profileFor,
  suggestSlug,
  slugMax,
  twoWaySlugMax,
  validSlug,
  type DeviceRoles,
  type SourceKind,
} from "./model";

export interface AddablePack {
  tag: string;
  plan: LanguagePlan;
  needVoice: boolean;
  addBytes: number;
}

/** TargetIssue is why a language cannot be a target right now: no installed
 *  translation route, or no voice where one is required. */
export type TargetIssue = "" | "route" | "voice";

/** WizardDraft is every answer the user gives. Keeping them in one object is
 *  what makes resetWizard total: a new question cannot be left behind. */
export interface WizardDraft {
  sourceKind: SourceKind | "";
  micChoice: string;
  cameraChoice: string;
  pairedMic: string;
  streamUrl: string;
  /** callRemote is where Prukka hears the other side; "custom" opens the URL
   *  field. The other three roles live in roles and are auto-detected. */
  callRemote: string;
  callRemoteUrl: string;
  roles: DeviceRoles;
  output: string;
  sourceLang: string;
  subs: string;
  dub: boolean;
  twoWayWanted: boolean;
  targets: string[];
  captionTargets: string[];
  /** A call runs as two audio lanes: youLang is what you speak, themLang is
   *  what the other party speaks. */
  youLang: string;
  themLang: string;
  slug: string;
  delaySeconds: number | null;
  /** broadcastAdd is a language the audience wants that needs a download
   *  first; while it is set the picker steps aside for the download panel. */
  broadcastAdd: AddablePack | null;
  /** callAdded holds languages added inline during the call flow. Their
   *  download panel vanishes the moment the packs land, so the confirmation
   *  lives here, in the owner that survives the panel. */
  callAdded: string[];
}

function freshDraft(): WizardDraft {
  return {
    sourceKind: "",
    micChoice: "",
    cameraChoice: "",
    pairedMic: "",
    streamUrl: "",
    callRemote: "",
    callRemoteUrl: "",
    roles: { remote: "", listen: "", mic: "", outgoing: "" },
    output: "",
    sourceLang: "auto",
    subs: "",
    dub: true,
    twoWayWanted: true,
    targets: [],
    captionTargets: [],
    youLang: "",
    themLang: "",
    slug: "",
    delaySeconds: null,
    broadcastAdd: null,
    callAdded: [],
  };
}

const pushReadyTimeoutMs = 30_000;
const pushReadyInitialDelayMs = 100;
const pushReadyMaxDelayMs = 1_000;

function routeTimeoutMessage(): string {
  return i18n.m.wizard.routeTimeout.replace("{seconds}", String(pushReadyTimeoutMs / 1000));
}

// A missing language a quick download would make usable, or null when it is
// already installed or genuinely unavailable. Powers the inline "add this
// language" panels so a basic user never hits a dead end.
//
// Deliberately narrower than languageAvailability.languageInfo: the call
// flow must keep offering a download while ANY pack is missing (a two-way
// call needs both hub routes plus a voice, where languageInfo would already
// answer "ready" once one direction works), and the broadcast picker offers
// a download only for languages with no route at all — one that has a route
// but no voice is already selectable as a caption target.
function addablePack(tag: string, needVoice: boolean): AddablePack | null {
  const plans = daemon.engine === null ? [] : languagePlans(daemon.engine);
  const catalogOk = (daemon.engine?.catalogError ?? "") === "";
  const plan = planForLanguage(plans, tag);
  if (plan === undefined || plan.state === "installed" || !daemon.engineSupported || !catalogOk) {
    return null;
  }
  const packs = packsToAdd(plan, needVoice);
  if (packs.length === 0) return null;
  return { tag, plan, needVoice, addBytes: totalSizeBytes(packs) };
}

export class WizardState {
  // The wizard walks four steps: source, languages, output, review. The
  // call/broadcast profile is derived from the source, never asked.
  step = $state(1);
  busy = $state(false);
  draft = $state<WizardDraft>(freshDraft());

  private routingAbort: AbortController | null = null;

  readonly languages = $derived(daemon.languages);
  readonly setupLoaded = $derived(
    daemon.languagesLoaded && daemon.devicesLoaded && daemon.configLoaded,
  );
  readonly setupFailed = $derived(
    this.setupLoaded
      && (daemon.languagesError || daemon.configError || this.languages.length === 0),
  );
  readonly ready = $derived(this.setupLoaded && !this.setupFailed);
  // Real device names beat raw device:// URLs; an empty enumeration
  // keeps the manual paths (call audio and stream URL) usable.
  readonly captureDevices = $derived(
    daemon.devices.filter((device) => device.kind === "audio-in"),
  );
  readonly audioOutputDevices = $derived(
    daemon.devices.filter((device) => device.kind === "audio-out"),
  );
  readonly videoOutputDevices = $derived(
    daemon.devices.filter((device) => device.kind === "video-out"),
  );
  readonly cameraDevices = $derived(
    daemon.devices.filter((device) => device.kind === "video-in"),
  );

  readonly voiceEnabled = $derived(daemon.config.providers?.voices !== "off");
  readonly translationPairs = $derived(daemon.config.providers?.local?.mt?.pairs ?? []);
  // The daemon reports the full set of languages the local voices can dub.
  readonly dubbedLangs = $derived(
    this.voiceEnabled ? (daemon.config.providers?.local?.dubbedLangs ?? []) : [],
  );
  readonly dubbedLangLabels = $derived(
    this.dubbedLangs
      .map((tag) =>
        this.languages.find((language) => sameBaseLanguage(language.tag, tag))?.label ?? tag)
      .join(", "),
  );

  // A two-way call needs both directions; the incoming-only fallback needs
  // the remote → you direction and a voice for the language you hear.
  readonly callDiffers = $derived(
    this.draft.youLang !== ""
      && this.draft.themLang !== ""
      && !sameBaseLanguage(this.draft.youLang, this.draft.themLang),
  );
  readonly forwardMT = $derived(
    this.callDiffers
      && translationSupported(this.translationPairs, this.draft.themLang, this.draft.youLang),
  );
  readonly reverseMT = $derived(
    this.callDiffers
      && translationSupported(this.translationPairs, this.draft.youLang, this.draft.themLang),
  );
  readonly youDub = $derived(this.voiceSupports(this.draft.youLang));
  readonly themDub = $derived(this.voiceSupports(this.draft.themLang));
  readonly twoWayAvailable = $derived(
    this.callDiffers && this.forwardMT && this.reverseMT && this.youDub && this.themDub,
  );
  readonly oneWayAvailable = $derived(this.callDiffers && this.forwardMT && this.youDub);
  readonly callSubmittable = $derived(this.twoWayAvailable || this.oneWayAvailable);
  readonly twoWay = $derived(this.draft.twoWayWanted && this.twoWayAvailable);

  readonly callDownloadList = $derived(
    this.draft.sourceKind === "call" ? this.callDownloads() : [],
  );
  readonly nameMax = $derived(
    this.draft.sourceKind === "call" && this.twoWay ? twoWaySlugMax : slugMax,
  );
  readonly nameValid = $derived(validSlug(this.draft.slug.trim(), this.nameMax));
  readonly stepTitles = $derived([
    i18n.m.wizard.stepSource,
    i18n.m.wizard.stepLanguages,
    i18n.m.wizard.stepOutput,
    i18n.m.wizard.stepStart,
  ]);
  /** devicePollActive gates the periodic hardware re-enumeration to the steps
   *  where a device picker is actually in front of the user. */
  readonly devicePollActive = $derived(
    this.draft.sourceKind !== "" && (this.step === 1 || this.step === 3),
  );

  /** dispose aborts any in-flight routing; the shell calls it on unmount. */
  dispose(): void {
    this.routingAbort?.abort();
  }

  /** seedDefaults fills language and subtitle defaults from the live config
   *  until the user commits to a source; the shell runs it as an effect. */
  seedDefaults(): void {
    if (!this.ready || this.draft.sourceKind !== "") return;

    const available = new Set(this.languages.map((language) => language.tag));
    const configured = (daemon.config.defaults?.langs ?? [])
      .filter((tag) => available.has(tag));
    this.draft.dub = this.voiceEnabled;
    this.draft.targets = configured.filter((tag) =>
      this.translationTargetSupported(tag) && (!this.draft.dub || this.voiceSupports(tag)));
    this.draft.captionTargets = this.draft.dub
      ? configured.filter((tag) => this.translationTargetSupported(tag) && !this.voiceSupports(tag))
      : [];

    const configuredSubs = daemon.config.defaults?.subs ?? "vtt";
    this.draft.subs = ["off", "vtt", "burn"].includes(configuredSubs) ? configuredSubs : "vtt";
  }

  applyCallDefaults(): void {
    this.draft.roles = detectCallRoles(daemon.devices, this.draft.roles);
    if (this.draft.callRemote === "" && this.draft.roles.remote !== "") {
      this.draft.callRemote = this.draft.roles.remote;
    }
  }

  /** seedCallLanguages picks the call's two languages from the daemon's
   *  dubbing capability and installed routes: you hear a language a voice can
   *  synthesize, and the other side speaks one that translates into it. The
   *  shell runs it as an effect while the call flow is active. */
  seedCallLanguages(): void {
    if (!this.ready || this.draft.sourceKind !== "call") return;
    if (this.draft.youLang === "") {
      this.draft.youLang =
        this.languages.find((language) => this.voiceSupports(language.tag))?.tag ?? "";
    }
    if (this.draft.themLang === "" && this.draft.youLang !== "") {
      const candidates = [
        ...(daemon.config.defaults?.langs ?? []),
        ...this.languages.map((language) => language.tag),
      ];
      this.draft.themLang =
        candidates.find(
          (tag) =>
            !sameBaseLanguage(tag, this.draft.youLang)
            && translationSupported(this.translationPairs, tag, this.draft.youLang),
        ) ??
        candidates.find((tag) => !sameBaseLanguage(tag, this.draft.youLang)) ??
        "";
    }
  }

  // Convenience defaults for the local capture pickers: the first real
  // device is almost always the right one, and staying overridable costs
  // nothing.
  applyCaptureDefaults(): void {
    if (this.draft.micChoice === "") {
      this.draft.micChoice = this.captureDevices.find((device) => !device.virtual)?.url ?? "";
    }
    if (this.draft.cameraChoice === "") this.draft.cameraChoice = this.cameraDevices[0]?.url ?? "";
    if (this.draft.pairedMic === "" && this.draft.sourceKind === "camera") {
      this.draft.pairedMic = this.captureDevices.find((device) => !device.virtual)?.url ?? "";
    }
  }

  chooseSource(kind: SourceKind): void {
    this.draft.sourceKind = kind;
    if (kind === "call") {
      this.applyCallDefaults();
      // No detected virtual speaker: open the manual URL path rather than
      // presenting an empty picker.
      if (this.draft.callRemote === "") this.draft.callRemote = "custom";
      return;
    }
    this.applyCaptureDefaults();
  }

  /** refreshDevices re-enumerates hardware and re-runs the chosen source's
   *  device defaults; the shell calls it on the tick while devicePollActive. */
  refreshDevices(controlToken: string): void {
    daemon.refreshDevices(controlToken);
    if (this.draft.sourceKind === "call") this.applyCallDefaults();
    else if (this.draft.sourceKind !== "") this.applyCaptureDefaults();
  }

  moveToStep(next: number): void {
    this.step = next;
  }

  private sourceResolved(): boolean {
    switch (this.draft.sourceKind) {
      case "mic":
        return this.draft.micChoice !== "";
      case "camera":
        if (this.draft.cameraChoice === "") return false;
        if (this.draft.pairedMic === "") {
          toasts.error(i18n.m.wizard.needMic);
          return false;
        }
        return true;
      case "call":
        return this.draft.callRemote === "custom"
          ? this.draft.callRemoteUrl.trim() !== ""
          : this.draft.callRemote !== "";
      case "stream":
        return this.draft.streamUrl.trim() !== "";
      default:
        return false;
    }
  }

  continueFromSource(form: HTMLFormElement): void {
    if (!form.reportValidity()) return;
    if (this.draft.sourceKind === "") return;
    if (!this.sourceResolved()) {
      if (this.draft.sourceKind !== "camera") toasts.error(i18n.m.wizard.sourceRequired);
      return;
    }
    this.moveToStep(2);
  }

  continueFromOutput(): void {
    if (this.draft.slug === "" && this.draft.sourceKind !== "") {
      this.draft.slug = suggestSlug(this.draft.sourceKind);
    }
    if (this.draft.delaySeconds === null) {
      this.draft.delaySeconds = daemon.config.defaults?.delaySeconds ?? 8;
    }
    this.moveToStep(4);
  }

  /** handleSubmit is the form's one submit path: Enter inside an earlier
   *  step's field advances the wizard; only the final step creates sessions. */
  handleSubmit(form: HTMLFormElement): void {
    if (this.step === 1) {
      this.continueFromSource(form);
      return;
    }
    if (this.step === 2) {
      this.moveToStep(3);
      return;
    }
    if (this.step === 3) {
      this.continueFromOutput();
      return;
    }
    if (this.step !== 4 || this.draft.sourceKind === "") return;
    if (profileFor(this.draft.sourceKind) === "call") {
      void this.submitCall();
      return;
    }
    void this.submitBroadcast();
  }

  toggleTarget(tag: string): void {
    if (!this.translationTargetSupported(tag)) return;
    if (this.draft.dub && !this.voiceSupports(tag)) return;
    this.draft.targets = this.draft.targets.includes(tag)
      ? this.draft.targets.filter((t) => t !== tag)
      : [...this.draft.targets, tag];
    if (this.draft.targets.includes(tag)) {
      this.draft.captionTargets = this.draft.captionTargets.filter((target) => target !== tag);
    }
  }

  toggleCaptionTarget(tag: string): void {
    if (!this.translationTargetSupported(tag)) return;
    this.draft.captionTargets = this.draft.captionTargets.includes(tag)
      ? this.draft.captionTargets.filter((target) => target !== tag)
      : [...this.draft.captionTargets, tag];
    if (this.draft.captionTargets.includes(tag)) {
      this.draft.targets = this.draft.targets.filter((target) => target !== tag);
    }
  }

  sessionTargets(): string[] {
    return [...new Set([...this.draft.targets, ...this.draft.captionTargets])];
  }

  voiceSupports(tag: string): boolean {
    return this.dubbedLangs.some((language) => sameBaseLanguage(tag, language));
  }

  languageLabel(tag: string): string {
    return this.languages.find((language) => language.tag === tag)?.label ?? tag;
  }

  // Usable choices lead a chip list; the rest stays visible and explained
  // behind them, in registry order within each half (sort is stable).
  orderedLanguages(usable: (tag: string) => boolean) {
    return [...this.languages].sort((a, b) => Number(usable(b.tag)) - Number(usable(a.tag)));
  }

  private translationTargetSupportedFor(source: string, target: string): boolean {
    if (source === "auto") {
      return autoTranslationTargetSupported(this.translationPairs, target);
    }
    return translationSupported(this.translationPairs, source, target);
  }

  translationTargetSupported(tag: string): boolean {
    return this.translationTargetSupportedFor(this.draft.sourceLang, tag);
  }

  setSourceLanguage(value: string): void {
    this.draft.sourceLang = value;
    this.draft.targets = this.draft.targets.filter((tag) =>
      this.translationTargetSupportedFor(value, tag)
      && (!this.draft.dub || this.voiceSupports(tag)));
    this.draft.captionTargets = this.draft.captionTargets.filter((tag) =>
      this.translationTargetSupportedFor(value, tag));
  }

  /** targetIssue classifies a chip's blocker so the template can render an
   *  icon while assistive technology hears the worded reason. */
  targetIssue(tag: string, requireVoice: boolean): TargetIssue {
    if (!this.translationTargetSupported(tag)) return "route";
    if (requireVoice && !this.voiceSupports(tag)) return "voice";
    return "";
  }

  issueLabel(issue: TargetIssue): string {
    if (issue === "route") return i18n.m.wizard.translationUnavailable;
    if (issue === "voice") return i18n.m.wizard.captionOnly;
    return "";
  }

  voiceMessage(message: string): string {
    const capability = this.voiceEnabled
      ? (this.dubbedLangLabels || i18n.m.wizard.unknownVoice)
      : i18n.m.wizard.disabledVoice;
    return message.replace("{language}", capability);
  }

  sourceMessage(message: string): string {
    return message.replace("{source}", this.languageLabel(this.draft.sourceLang));
  }

  private reasonNoRoute(from: string, to: string): string {
    return i18n.m.wizard.reasonNoRoute
      .replace("{from}", this.languageLabel(from))
      .replace("{to}", this.languageLabel(to));
  }

  private reasonNoVoice(lang: string): string {
    return i18n.m.wizard.reasonNoVoice.replace("{lang}", this.languageLabel(lang));
  }

  // The call note is honest about which direction, if any, is missing and why.
  callReadyNote(): string {
    return i18n.m.wizard.twoWayReady
      .replace("{them}", this.languageLabel(this.draft.themLang))
      .replace("{you}", this.languageLabel(this.draft.youLang));
  }

  callFallbackNote(): string {
    const reason = !this.reverseMT
      ? this.reasonNoRoute(this.draft.youLang, this.draft.themLang)
      : this.reasonNoVoice(this.draft.themLang);
    return i18n.m.wizard.twoWayUnavailable
      .replace("{reason}", reason)
      .replace("{you}", this.languageLabel(this.draft.youLang));
  }

  callUnavailableNote(): string {
    const reason = !this.youDub
      ? this.reasonNoVoice(this.draft.youLang)
      : this.reasonNoRoute(this.draft.themLang, this.draft.youLang);
    return i18n.m.wizard.callUnavailable.replace("{reason}", reason);
  }

  // callFallbackHint names the first missing capability that blocks two-way,
  // shown under the disabled switch so the limit is explained where it bites.
  callFallbackHint(): string {
    if (!this.callDiffers) return "";
    if (!this.reverseMT) return this.reasonNoRoute(this.draft.youLang, this.draft.themLang);
    if (!this.themDub) return this.reasonNoVoice(this.draft.themLang);
    if (!this.forwardMT) return this.reasonNoRoute(this.draft.themLang, this.draft.youLang);
    if (!this.youDub) return this.reasonNoVoice(this.draft.youLang);
    return "";
  }

  setDubbing(enabled: boolean): void {
    if (!this.voiceEnabled) {
      this.draft.dub = false;
      return;
    }
    this.draft.dub = enabled;
    if (!enabled) return;

    const unsupported = this.draft.targets.filter((tag) => !this.voiceSupports(tag));
    this.draft.targets = this.draft.targets.filter((tag) => this.voiceSupports(tag));
    this.draft.captionTargets = [...new Set([...this.draft.captionTargets, ...unsupported])];
  }

  // The two chosen call languages that still need a download for the call to
  // run — offered inline so the user can add them without leaving the wizard.
  private callDownloads(): AddablePack[] {
    const seen = new Set<string>();
    const out: AddablePack[] = [];
    for (const tag of [this.draft.youLang, this.draft.themLang]) {
      if (tag === "" || seen.has(tag)) continue;
      seen.add(tag);
      const pack = addablePack(tag, true);
      if (pack !== null) out.push(pack);
    }
    return out;
  }

  broadcastAddable(): { tag: string; label: string }[] {
    if (this.draft.broadcastAdd !== null) return [];
    return this.languages
      .filter((language) =>
        !this.draft.targets.includes(language.tag)
        && !this.draft.captionTargets.includes(language.tag))
      .filter((language) => !this.translationTargetSupported(language.tag))
      .filter((language) => addablePack(language.tag, this.draft.dub) !== null)
      .map((language) => ({ tag: language.tag, label: language.label }));
  }

  beginBroadcastAdd(tag: string): void {
    const pack = addablePack(tag, this.draft.dub);
    if (pack !== null) this.draft.broadcastAdd = pack;
  }

  cancelBroadcastAdd(): void {
    this.draft.broadcastAdd = null;
  }

  finishBroadcastAdd(tag: string): void {
    // engineInstall.start awaited the config re-read before this callback, so
    // the capability guards inside the toggles already see the new language.
    if (this.draft.dub && this.voiceSupports(tag)) this.toggleTarget(tag);
    else this.toggleCaptionTarget(tag);
    this.draft.broadcastAdd = null;
  }

  finishCallAdd(tag: string): void {
    if (!this.draft.callAdded.includes(tag)) this.draft.callAdded = [...this.draft.callAdded, tag];
  }

  private assertEffectiveVoice(session: Session, expected: string): void {
    if ((session.effectiveDubbedLangs ?? []).includes(expected)) return;
    throw new Error(this.voiceMessage(i18n.m.wizard.voiceUnavailable));
  }

  private async routeWhenReady(
    name: string,
    lang: string,
    targetUrl: string,
    signal: AbortSignal,
  ): Promise<void> {
    const readiness = new AbortController();
    const cancel = () => readiness.abort(signal.reason);
    if (signal.aborted) cancel();
    else signal.addEventListener("abort", cancel, { once: true });
    // The one budget for the whole wait: it aborts the in-flight push and the
    // sleep between retries alike, so a timeout always reads as routeTimeout.
    const timeout = setTimeout(
      () => readiness.abort(new Error(routeTimeoutMessage())),
      pushReadyTimeoutMs,
    );
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
          if (!(e instanceof ApiError && e.status === 503)) throw e;
          await retryDelay(delay, readiness.signal);
          delay = Math.min(delay * 2, pushReadyMaxDelayMs);
        }
      }
    } finally {
      clearTimeout(timeout);
      signal.removeEventListener("abort", cancel);
    }
  }

  private async rollbackSessions(created: string[], cause: unknown): Promise<never> {
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

  private resetWizard(): void {
    this.draft = freshDraft();
    this.moveToStep(1);
  }

  private broadcastSourceUrl(): string {
    if (this.draft.sourceKind === "camera") {
      return avSourceUrl(this.draft.cameraChoice, this.draft.pairedMic);
    }
    if (this.draft.sourceKind === "mic") return this.draft.micChoice;
    return this.draft.streamUrl.trim();
  }

  private async submitBroadcast(): Promise<void> {
    const allTargets = this.sessionTargets();
    if (allTargets.length === 0) {
      toasts.error(i18n.m.wizard.needTarget);
      return;
    }
    const flags: Record<string, string> = { subs: this.draft.subs };
    if (this.draft.sourceLang !== "auto") flags.source = this.draft.sourceLang;
    if (this.draft.dub) flags.dub_langs = this.draft.targets.join(",");
    else flags.dub = "off";

    if (this.draft.sourceKind === "camera" && this.draft.pairedMic === "") {
      toasts.error(i18n.m.wizard.needMic);
      return;
    }

    const source = this.broadcastSourceUrl();
    const name = this.draft.slug.trim();
    const delay = this.draft.delaySeconds;
    const dub = this.draft.dub;
    const dubTargets = [...this.draft.targets];
    const output = this.draft.output;

    await this.withSubmission(async (signal) => {
      const created = await createSession(
        {
          slug: name,
          profile: "broadcast",
          sourceUrl: source,
          langs: allTargets,
          flags,
          ...(delay === null ? {} : { delaySeconds: delay }),
        },
        token.value,
      );

      try {
        if (dub) {
          for (const target of dubTargets) this.assertEffectiveVoice(created, target);
        }

        const routable = [...this.videoOutputDevices, ...this.audioOutputDevices];
        const routedOutput = routable.some((device) => device.url === output) ? output : "";
        const routedLanguage = allTargets[0];
        if (routedOutput !== "" && routedLanguage !== undefined) {
          await this.routeWhenReady(name, routedLanguage, routedOutput, signal);
        }
      } catch (cause) {
        await this.rollbackSessions([name], cause);
      }
    });
  }

  private async submitCall(): Promise<void> {
    const name = this.draft.slug.trim();
    const remote = this.draft.callRemote === "custom"
      ? this.draft.callRemoteUrl.trim()
      : this.draft.callRemote;
    // Validation failures return before anything is created, keeping the
    // form intact for correction.
    if (!this.callSubmittable) {
      toasts.error(this.callUnavailableNote());
      return;
    }
    if (remote === "") {
      toasts.error(i18n.m.wizard.remoteSourceRequired);
      return;
    }
    if (this.twoWay) {
      // Both lanes need every device role resolved: an empty source or
      // target would only fail after the first session already exists.
      const { mic, listen, outgoing } = this.draft.roles;
      if (mic === "" || listen === "" || outgoing === "") {
        toasts.error(i18n.m.wizard.needCallDevices);
        return;
      }
      // The "-in"/"-out" suffixes must keep both slugs within the server's
      // 63-character limit.
      if (name.length > twoWaySlugMax) {
        toasts.error(i18n.m.wizard.nameTooLong);
        return;
      }
      await this.withSubmission((signal) => this.createTwoWayCall(name, remote, signal));
      return;
    }
    await this.withSubmission((signal) => this.createOneWayCall(name, remote, signal));
  }

  // Two lanes, each created and voice-confirmed before the next, then both
  // routed. A failure at any point deletes the lanes already created — or
  // names the ones it could not — and rethrows.
  private async createTwoWayCall(name: string, remote: string, signal: AbortSignal): Promise<void> {
    const inSlug = `${name}-in`;
    const outSlug = `${name}-out`;
    const created: string[] = [];
    try {
      const incoming = await createSession(
        {
          slug: inSlug,
          profile: "call",
          sourceUrl: remote,
          langs: [this.draft.youLang],
          flags: {
            subs: this.draft.subs,
            source: this.draft.themLang,
            dub_langs: this.draft.youLang,
            pair: outSlug,
          },
        },
        token.value,
      );
      created.push(inSlug);
      this.assertEffectiveVoice(incoming, this.draft.youLang);

      const outgoing = await createSession(
        {
          slug: outSlug,
          profile: "call",
          sourceUrl: this.draft.roles.mic,
          langs: [this.draft.themLang],
          flags: {
            subs: this.draft.subs,
            source: this.draft.youLang,
            dub_langs: this.draft.themLang,
            pair: inSlug,
          },
        },
        token.value,
      );
      created.push(outSlug);
      this.assertEffectiveVoice(outgoing, this.draft.themLang);

      await this.routeWhenReady(inSlug, this.draft.youLang, this.draft.roles.listen, signal);
      await this.routeWhenReady(outSlug, this.draft.themLang, this.draft.roles.outgoing, signal);
    } catch (cause) {
      await this.rollbackSessions(created, cause);
    }
  }

  // Incoming-only fallback: hear the other side in your language on your
  // output.
  private async createOneWayCall(name: string, remote: string, signal: AbortSignal): Promise<void> {
    const created: string[] = [];
    try {
      const incoming = await createSession(
        {
          slug: name,
          profile: "call",
          sourceUrl: remote,
          langs: [this.draft.youLang],
          flags: {
            subs: this.draft.subs,
            source: this.draft.themLang,
            dub_langs: this.draft.youLang,
          },
        },
        token.value,
      );
      created.push(name);
      this.assertEffectiveVoice(incoming, this.draft.youLang);

      // Routing stays optional for the incoming-only flow: an empty output
      // defers it to the operator.
      const routed = this.draft.roles.listen;
      const listen = this.audioOutputDevices.some((device) => device.url === routed) ? routed : "";
      if (listen !== "") await this.routeWhenReady(name, this.draft.youLang, listen, signal);
    } catch (cause) {
      await this.rollbackSessions(created, cause);
    }
  }

  // Shared submission scaffolding: a single in-flight controller, a daemon
  // refresh and a worded failure toast.
  private async withSubmission(run: (signal: AbortSignal) => Promise<void>): Promise<void> {
    this.busy = true;
    const controller = new AbortController();
    this.routingAbort?.abort();
    this.routingAbort = controller;
    try {
      await run(controller.signal);
      this.resetWizard();
      await daemon.refresh();
    } catch (e) {
      await daemon.refresh();
      toasts.failure(e, i18n.m.wizard.createFailed);
    } finally {
      if (this.routingAbort === controller) this.routingAbort = null;
      this.busy = false;
    }
  }

  summarySource(): string {
    switch (this.draft.sourceKind) {
      case "mic":
        return this.deviceLabel(this.draft.micChoice) || i18n.m.wizard.sourceMic;
      case "camera": {
        const { cameraChoice, pairedMic } = this.draft;
        return `${this.deviceLabel(cameraChoice)} + ${this.deviceLabel(pairedMic)}`;
      }
      case "call":
        return this.draft.callRemote === "custom"
          ? this.draft.callRemoteUrl.trim()
          : this.deviceLabel(this.draft.callRemote);
      case "stream":
        return this.draft.streamUrl.trim();
      default:
        return "";
    }
  }

  summaryLanguages(): string {
    if (this.draft.sourceKind === "call") {
      const { themLang, youLang } = this.draft;
      return this.twoWay
        ? this.callReadyNote()
        : `${this.languageLabel(themLang)} → ${this.languageLabel(youLang)}`;
    }
    const from = this.draft.sourceLang === "auto"
      ? i18n.m.wizard.autoDetect
      : this.languageLabel(this.draft.sourceLang);
    const to = this.sessionTargets().map((tag) => this.languageLabel(tag)).join(", ");
    return `${from} → ${to || "—"}`;
  }

  summaryOutput(): string {
    const parts: string[] = [];
    if (this.draft.sourceKind === "call") {
      if (this.draft.roles.listen !== "") parts.push(this.deviceLabel(this.draft.roles.listen));
      if (this.twoWay && this.draft.roles.outgoing !== "") {
        parts.push(this.deviceLabel(this.draft.roles.outgoing));
      }
    } else if (this.draft.output !== "") {
      parts.push(this.deviceLabel(this.draft.output));
    }
    return parts.length > 0 ? parts.join(" · ") : i18n.m.wizard.outputNone;
  }

  deviceLabel(url: string): string {
    return daemon.devices.find((device) => device.url === url)?.label ?? url;
  }

  get connected(): boolean {
    return isControlToken(token.value);
  }
}

function retryDelay(ms: number, signal: AbortSignal): Promise<void> {
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
