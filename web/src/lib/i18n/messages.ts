// The dashboard's message catalog. Messages is the single shape both
// locales must satisfy, so a missing translation fails the type check.

export interface Messages {
  tagline: string;
  language: string;
  skipToContent: string;
  daemonStatus: { label: string; idle: string; live: string; degraded: string };
  stats: { sessions: string; uptime: string; version: string };
  stepper: { progress: string };
  wizard: {
    title: string;
    chooseUseCase: string;
    loading: string;
    unavailable: string;
    retry: string;
    devicesUnavailable: string;
    languagesStep: string;
    outputStep: string;
    reviewStep: string;
    back: string;
    next: string;
    name: string;
    nameTwoWayHint: string;
    source: string;
    sourceRequired: string;
    sourceMic: string;
    sourceMicDesc: string;
    sourceCamera: string;
    sourceCameraDesc: string;
    sourceCall: string;
    sourceCallDesc: string;
    sourceStream: string;
    sourceStreamDesc: string;
    micPicker: string;
    cameraPicker: string;
    pairedMic: string;
    needMic: string;
    callHowTo: string;
    twoWaySwitch: string;
    twoWaySwitchHint: string;
    twoWayReady: string;
    twoWayUnavailable: string;
    callUnavailable: string;
    callSameLang: string;
    reasonNoRoute: string;
    reasonNoVoice: string;
    youLang: string;
    themLang: string;
    remoteSource: string;
    remoteSourceUrl: string;
    remoteSourceHint: string;
    remoteSourceRequired: string;
    listenOutput: string;
    listenOutputHint: string;
    yourMic: string;
    yourMicHint: string;
    outgoing: string;
    outgoingHint: string;
    needCallDevices: string;
    nameTooLong: string;
    sourceCustom: string;
    outputAudio: string;
    videoOutput: string;
    outputNone: string;
    sourceLang: string;
    autoDetect: string;
    subtitles: string;
    subsOn: string;
    subsOff: string;
    subsBurn: string;
    dubbing: string;
    targets: string;
    dubTargets: string;
    dubCapability: string;
    dubCapabilityUnknown: string;
    dubCapabilityOff: string;
    captionOnly: string;
    translationAuto: string;
    translationConcrete: string;
    translationUnavailable: string;
    captionTargets: string;
    captionTargetsDesc: string;
    voiceUnavailable: string;
    unknownVoice: string;
    disabledVoice: string;
    participantNotice: string;
    create: string;
    creating: string;
    needTarget: string;
    createFailed: string;
    routeTimeout: string;
    delay: string;
    delayHint: string;
    summarySource: string;
    summaryLanguages: string;
    summaryOutput: string;
    stepSource: string;
    stepLanguages: string;
    stepOutput: string;
    stepStart: string;
    connectFirst: string;
    callNeedsDownload: string;
    addMissingBroadcast: string;
  };
  sessions: {
    title: string;
    empty: string;
    session: string;
    source: string;
    status: string;
    statusStarting: string;
    statusRunning: string;
    statusFinished: string;
    statusFailed: string;
    statusUnknown: string;
    languages: string;
    delay: string;
    actions: string;
    push: string;
    pushTitle: string;
    remove: string;
    removeSession: string;
    addLanguage: string;
    addMissing: string;
    removeLang: string;
    liveSubs: string;
    dubbedAudio: string;
  };
  pushDialog: {
    push: string;
    title: string;
    language: string;
    target: string;
    targetPicker: string;
    customTarget: string;
    subtitles: string;
    subsOff: string;
    subsBurn: string;
    cancel: string;
    start: string;
    starting: string;
    targetNotice: string;
    failed: string;
  };
  doctor: {
    title: string;
    loading: string;
    tokenRequired: string;
    unreachable: string;
    retry: string;
  };
  events: { title: string; empty: string };
  footer: {
    privacy: string;
    resources: string;
    dataProtection: string;
    accessibility: string;
    source: string;
  };
  toasts: { notifications: string; dismiss: string; unauthorized: string };
  settings: {
    title: string;
    loading: string;
    retry: string;
    voices: string;
    voicesLabel: string;
    voicesHint: string;
    defaults: string;
    defaultLangs: string;
    defaultLangsHint: string;
    subs: string;
    subsOff: string;
    subsVtt: string;
    subsBurn: string;
    bed: string;
    delay: string;
    save: string;
    saving: string;
    saved: string;
    saveFailed: string;
    loadFailed: string;
    tokenRequired: string;
    restartNote: string;
  };
  languages: {
    title: string;
    lead: string;
    engineCore: string;
    ready: string;
    installed: string;
    partial: string;
    available: string;
    install: string;
    installing: string;
    remove: string;
    removing: string;
    phaseDownload: string;
    phaseVerify: string;
    phaseInstall: string;
    lastLanguageHint: string;
    catalogUnreachable: string;
    retry: string;
    tokenRequired: string;
    loading: string;
    loadFailed: string;
    actionFailed: string;
    sizeMiB: string;
  };
  add: {
    button: string;
    sub: string;
    connect: string;
    unsupported: string;
    catalogError: string;
    retry: string;
    busyOther: string;
    preparing: string;
    phaseDownload: string;
    phaseVerify: string;
    phaseInstall: string;
    step: string;
    size: string;
    cancel: string;
    done: string;
    errorTitle: string;
    tryAgain: string;
  };
  home: { ready: string; translating: string; attention: string };
  getStarted: {
    title: string;
    lead: string;
    leadUnsized: string;
    install: string;
    installing: string;
  };
  unlock: { connect: string; connected: string; explain: string; field: string; apply: string; disconnect: string };
}

export const locales = ["en", "it"] as const;

export type Locale = (typeof locales)[number];

export const messages: Record<Locale, Messages> = {
  en: {
    tagline: "Every stream, every language — one bridge.",
    language: "Language",
    skipToContent: "Skip to main content",
    daemonStatus: {
      label: "Daemon status",
      idle: "Ready",
      live: "Active",
      degraded: "Attention needed",
    },
    stats: { sessions: "sessions", uptime: "uptime", version: "version" },
    stepper: { progress: "Progress" },
    wizard: {
      title: "New session",
      chooseUseCase: "What do you want to translate?",
      loading: "Loading languages and local devices…",
      unavailable: "Languages or configuration could not be loaded.",
      retry: "Retry",
      devicesUnavailable: "Device discovery is unavailable. Enter a source URL manually.",
      languagesStep: "Choose languages",
      outputStep: "Choose where it goes",
      reviewStep: "Review and start",
      back: "Back",
      next: "Continue",
      name: "Name",
      nameTwoWayHint: "Two lanes are created from this name: “-in” and “-out”.",
      source: "Source URL",
      sourceRequired: "choose or enter a source first",
      sourceMic: "Microphone",
      sourceMicDesc: "Translate what you say, live.",
      sourceCamera: "Camera + microphone",
      sourceCameraDesc: "A video feed with translated voice or subtitles.",
      sourceCall: "A call I'm on",
      sourceCallDesc: "Zoom, Meet, phone — hear the other person in your language.",
      sourceStream: "Stream or file",
      sourceStreamDesc: "RTMP, SRT or a local file URL.",
      micPicker: "Microphone",
      cameraPicker: "Camera",
      pairedMic: "Microphone to pair",
      needMic: "a camera source needs a microphone paired with it",
      callHowTo:
        "In the native call app (Zoom, Meet, …), select “Prukka Speaker” as its speaker and “Prukka Microphone” as its microphone. Prukka hears the other side through the virtual speaker and speaks your translated voice through the virtual microphone. Browser call apps cannot select this audio path reliably.",
      twoWaySwitch: "Also translate my voice",
      twoWaySwitchHint: "They hear you in their language through the virtual microphone.",
      twoWayReady:
        "Two-way call ready: they hear you in {them}, and you hear them in {you}.",
      twoWayUnavailable:
        "Two-way isn't available. {reason} Prukka will translate the incoming side only, so you'll hear the other person in {you}.",
      callUnavailable: "This call can't run yet. {reason}",
      callSameLang: "Pick two different languages for the two sides of the call.",
      reasonNoRoute: "There is no translation route from {from} to {to}.",
      reasonNoVoice: "There is no local voice for {lang}.",
      youLang: "I speak",
      themLang: "They speak",
      remoteSource: "Call audio source",
      remoteSourceUrl: "Call audio source URL",
      remoteSourceHint: "Where Prukka hears the other side: the virtual speaker your call app plays into.",
      remoteSourceRequired: "choose where Prukka hears the other side",
      listenOutput: "I listen on",
      listenOutputHint: "Where you hear the translation: your real headphones or speakers.",
      yourMic: "My microphone",
      yourMicHint: "Your real microphone. Prukka translates your voice for the other side.",
      outgoing: "Send my voice to",
      outgoingHint: "The virtual microphone your call app uses as its mic (Prukka Microphone).",
      needCallDevices:
        "a two-way call needs all four devices: the call audio source, the output you listen on, your microphone and the outgoing virtual microphone",
      nameTooLong:
        "keep the call name within 59 characters: Prukka appends \"-in\" and \"-out\" to the two lanes",
      sourceCustom: "Custom URL…",
      outputAudio: "Play the translation on",
      videoOutput: "Send video to",
      outputNone: "Don't route (choose later)",
      sourceLang: "Source language",
      autoDetect: "Auto-detect",
      subtitles: "Subtitles",
      subsOn: "On (WebVTT)",
      subsOff: "Off",
      subsBurn: "Burn into routed video",
      dubbing: "Dubbing",
      targets: "Target languages",
      dubTargets: "Dubbed languages",
      dubCapability: "The local voices dub {language}. Other languages are captions only.",
      dubCapabilityUnknown: "The daemon did not report a dubbable language. Dubbed targets are unavailable.",
      dubCapabilityOff: "Dubbing is disabled in the daemon configuration. All targets are captions only.",
      captionOnly: "captions only",
      translationAuto:
        "Auto-detect shows the union of languages in installed MT pairs. The daemon checks the detected source → target direction at runtime; choose a concrete source for an exact check.",
      translationConcrete:
        "Targets require an installed translation route from {source}; output in the same base language needs no MT model.",
      translationUnavailable: "translation unavailable",
      captionTargets: "Additional subtitle languages",
      captionTargetsDesc: "Captions only: no synthesized voice for these languages.",
      voiceUnavailable:
        "The daemon did not confirm dubbed audio for the requested language. The configured voice supports {language}; the session was rolled back.",
      unknownVoice: "no language reported",
      disabledVoice: "disabled",
      participantNotice:
        "Before starting, inform participants that AI will process their speech and identify external outputs or network-accessible media links. The operator must determine and document the lawful basis; Prukka does not make an announcement.",
      create: "Create session",
      creating: "Creating…",
      needTarget: "pick at least one target language",
      createFailed: "create failed",
      routeTimeout: "media output did not become ready within {seconds} seconds",
      delay: "Delay (seconds)",
      delayHint:
        "The output runs this far behind the source so translation lands in sync. Lower is snappier; higher is smoother.",
      summarySource: "Source",
      summaryLanguages: "Languages",
      summaryOutput: "Output",
      stepSource: "Source",
      stepLanguages: "Languages",
      stepOutput: "Output",
      stepStart: "Start",
      connectFirst: "Connect Prukka with the button in the top bar to begin.",
      callNeedsDownload: "These languages need a quick download for this call:",
      addMissingBroadcast: "Add a language you don't have yet",
    },
    sessions: {
      title: "Sessions",
      empty: "No sessions yet — use the wizard above or",
      session: "Session",
      source: "Source",
      status: "Status",
      statusStarting: "Starting",
      statusRunning: "Running",
      statusFinished: "Finished",
      statusFailed: "Failed",
      statusUnknown: "Unknown",
      languages: "Languages",
      delay: "Delay",
      actions: "Actions",
      push: "Push",
      pushTitle: "push a language to RTMP/SRT or a device",
      remove: "remove session",
      removeSession: "remove session",
      addLanguage: "add language to",
      addMissing: "Add a language you don't have yet to",
      removeLang: "remove",
      liveSubs: "live subtitles (WebVTT)",
      dubbedAudio: "dubbed audio (MPEG-TS)",
    },
    pushDialog: {
      push: "Push",
      title: "to a live target",
      language: "Language",
      target: "Target URL (RTMP/SRT or device://)",
      targetPicker: "Target destination",
      customTarget: "Custom target URL",
      subtitles: "Subtitles",
      subsOff: "Off",
      subsBurn: "Burned into video",
      cancel: "Cancel",
      start: "Start push",
      starting: "Starting…",
      targetNotice:
        "This action sends media to the destination you choose. Treat stream keys in target URLs as secrets.",
      failed: "push failed",
    },
    doctor: {
      title: "Environment",
      loading: "Checking the environment…",
      tokenRequired:
        "Connect Prukka with the button in the top bar to run environment checks.",
      unreachable: "The daemon health check is unavailable.",
      retry: "Retry",
    },
    events: { title: "Events", empty: "No events yet." },
    toasts: {
      notifications: "Error notifications",
      dismiss: "Dismiss",
      unauthorized:
        "Control token missing or invalid — reopen the dashboard with `prukka up`, or paste the access code using the button in the top bar.",
    },
    footer: {
      privacy:
        "Processing is designed to stay on this device. Configured routes and any network-reachable media listener can expose content. Rolling HLS files stay local and are removed on session deletion or clean shutdown; the next start clears crash debris.",
      resources: "Project resources",
      dataProtection: "Data protection",
      accessibility: "Accessibility",
      source: "Source & docs",
    },
    settings: {
      title: "Settings",
      loading: "Loading settings…",
      retry: "Retry",
      voices: "Voices",
      voicesLabel: "Synthesized voices (dubbing)",
      voicesHint: "Off makes every target captions-only.",
      defaults: "Session defaults",
      defaultLangs: "Default target languages",
      defaultLangsHint:
        "Automatic detection offers only languages declared by installed MT pairs. Existing unsupported defaults stay visible so you can remove them.",
      subs: "Subtitles",
      subsOff: "Off",
      subsVtt: "On (WebVTT)",
      subsBurn: "Burned into video",
      bed: "Background bed level",
      delay: "Delay (seconds)",
      save: "Save settings",
      saving: "Saving…",
      saved: "Settings saved and applied.",
      saveFailed: "save failed",
      loadFailed: "could not load the configuration",
      tokenRequired: "Enter a valid control token to view and edit settings.",
      restartNote: "Applies after a daemon restart:",
    },
    languages: {
      title: "Languages",
      lead:
        "Install the speech engine and the language packs it uses for transcription, translation and local voices. Everything downloads to this machine and runs locally.",
      engineCore: "Engine core",
      ready: "Ready",
      installed: "Installed",
      partial: "Partially installed",
      available: "Available",
      install: "Install",
      installing: "Installing…",
      remove: "Remove",
      removing: "Removing…",
      phaseDownload: "Downloading",
      phaseVerify: "Verifying",
      phaseInstall: "Installing",
      lastLanguageHint: "The last installed language cannot be removed.",
      catalogUnreachable: "The pack catalog is unreachable — check your connection.",
      retry: "Retry",
      tokenRequired: "Enter a valid control token to manage languages.",
      loading: "Loading language packs…",
      loadFailed: "could not load the language packs",
      actionFailed: "language pack operation failed",
      sizeMiB: "{size} MiB",
    },
    add: {
      button: "Add {language} · {size} MB",
      sub: "Downloads {language} so you can use it here.",
      connect: "Connect Prukka to add languages.",
      unsupported: "This daemon doesn't offer language downloads.",
      catalogError: "Can't reach the language catalog — check your connection.",
      retry: "Retry",
      busyOther: "Please wait — finishing the current download.",
      preparing: "Getting Prukka ready…",
      phaseDownload: "Downloading",
      phaseVerify: "Checking",
      phaseInstall: "Installing",
      step: "Step {index} of {count}",
      size: "{done} MB of {total} MB",
      cancel: "Cancel",
      done: "{language} added — it's ready to use.",
      errorTitle: "We couldn't add {language}.",
      tryAgain: "Try again",
    },
    home: {
      ready: "Prukka is ready.",
      translating: "Prukka is translating now.",
      attention: "Prukka needs attention.",
    },
    getStarted: {
      title: "Get Prukka ready",
      lead:
        "Installs the local speech engine: its runtime plus the {size} MiB core model. Transcription, translation and voices then run on this machine.",
      leadUnsized:
        "Installs the local speech engine: its runtime plus the core model. Transcription, translation and voices then run on this machine.",
      install: "Install the speech engine",
      installing: "Installing the speech engine…",
    },
    unlock: {
      connect: "Connect Prukka",
      connected: "Connected",
      explain:
        "Run `prukka up` to open the dashboard already connected, or paste the access code from Prukka's control.token file.",
      field: "Access code",
      apply: "Unlock",
      disconnect: "Disconnect",
    },
  },
  it: {
    tagline: "Ogni stream, ogni lingua — un ponte.",
    language: "Lingua",
    skipToContent: "Vai al contenuto principale",
    daemonStatus: {
      label: "Stato del demone",
      idle: "Pronto",
      live: "Attivo",
      degraded: "Richiede attenzione",
    },
    stats: { sessions: "sessioni", uptime: "attività", version: "versione" },
    stepper: { progress: "Avanzamento" },
    wizard: {
      title: "Nuova sessione",
      chooseUseCase: "Cosa vuoi tradurre?",
      loading: "Caricamento di lingue e dispositivi locali…",
      unavailable: "Impossibile caricare le lingue o la configurazione.",
      retry: "Riprova",
      devicesUnavailable: "Rilevamento dispositivi non disponibile. Inserisci manualmente l'URL sorgente.",
      languagesStep: "Scegli le lingue",
      outputStep: "Scegli dove va",
      reviewStep: "Rivedi e avvia",
      back: "Indietro",
      next: "Continua",
      name: "Nome",
      nameTwoWayHint: "Da questo nome nascono due canali: “-in” e “-out”.",
      source: "URL sorgente",
      sourceRequired: "prima scegli o inserisci una sorgente",
      sourceMic: "Microfono",
      sourceMicDesc: "Traduci quello che dici, dal vivo.",
      sourceCamera: "Fotocamera + microfono",
      sourceCameraDesc: "Un flusso video con voce tradotta o sottotitoli.",
      sourceCall: "Una chiamata a cui partecipo",
      sourceCallDesc: "Zoom, Meet, telefono — senti l'altra persona nella tua lingua.",
      sourceStream: "Stream o file",
      sourceStreamDesc: "RTMP, SRT o l'URL di un file locale.",
      micPicker: "Microfono",
      cameraPicker: "Fotocamera",
      pairedMic: "Microfono da abbinare",
      needMic: "una sorgente camera richiede un microfono abbinato",
      callHowTo:
        "Nell'app nativa di chiamata (Zoom, Meet, …), scegli “Prukka Speaker” come altoparlante e “Prukka Microphone” come microfono. Prukka ascolta l'altra persona tramite lo speaker virtuale e parla con la tua voce tradotta attraverso il microfono virtuale. Le app di chiamata nel browser non selezionano questo percorso audio in modo affidabile.",
      twoWaySwitch: "Traduci anche la mia voce",
      twoWaySwitchHint: "L'altra persona ti sente nella sua lingua attraverso il microfono virtuale.",
      twoWayReady:
        "Chiamata bidirezionale pronta: l'altra persona ti sente in {them} e tu la senti in {you}.",
      twoWayUnavailable:
        "Il bidirezionale non è disponibile. {reason} Prukka tradurrà solo il lato in arrivo, quindi sentirai l'altra persona in {you}.",
      callUnavailable: "Questa chiamata non può ancora partire. {reason}",
      callSameLang: "Scegli due lingue diverse per i due lati della chiamata.",
      reasonNoRoute: "Non c'è una rotta di traduzione da {from} a {to}.",
      reasonNoVoice: "Non c'è una voce locale per {lang}.",
      youLang: "Io parlo",
      themLang: "L'altra persona parla",
      remoteSource: "Audio proveniente dalla chiamata",
      remoteSourceUrl: "URL dell'audio della chiamata",
      remoteSourceHint: "Da dove Prukka sente gli altri: lo speaker virtuale su cui suona l'app di chiamata.",
      remoteSourceRequired: "scegli da dove Prukka sente l'altra persona",
      listenOutput: "Io ascolto su",
      listenOutputHint: "Dove senti tu la traduzione: le tue cuffie o casse reali.",
      yourMic: "Il mio microfono",
      yourMicHint: "Il tuo microfono reale. Prukka traduce la tua voce per l'altra persona.",
      outgoing: "Invia la mia voce a",
      outgoingHint: "Il microfono virtuale che la tua app di chiamata usa come microfono (Prukka Microphone).",
      needCallDevices:
        "una chiamata bidirezionale richiede tutti e quattro i dispositivi: l'audio della chiamata, l'uscita su cui ascolti, il tuo microfono e il microfono virtuale in uscita",
      nameTooLong:
        "il nome della chiamata può avere al massimo 59 caratteri: Prukka aggiunge \"-in\" e \"-out\" ai due canali",
      sourceCustom: "URL manuale…",
      outputAudio: "Riproduci la traduzione su",
      videoOutput: "Invia il video a",
      outputNone: "Non instradare (scelgo dopo)",
      sourceLang: "Lingua sorgente",
      autoDetect: "Rilevamento automatico",
      subtitles: "Sottotitoli",
      subsOn: "Attivi (WebVTT)",
      subsOff: "Disattivi",
      subsBurn: "Impressi nel video instradato",
      dubbing: "Doppiaggio",
      targets: "Lingue di destinazione",
      dubTargets: "Lingue doppiate",
      dubCapability: "Le voci locali doppiano {language}. Le altre lingue sono disponibili solo come sottotitoli.",
      dubCapabilityUnknown: "Il demone non ha indicato una lingua doppiabile. Le destinazioni doppiate non sono disponibili.",
      dubCapabilityOff: "Il doppiaggio è disattivato nella configurazione del demone. Tutte le destinazioni sono disponibili solo come sottotitoli.",
      captionOnly: "solo sottotitoli",
      translationAuto:
        "Il rilevamento automatico mostra l'unione delle lingue presenti nelle coppie MT installate. Il demone verifica a runtime la direzione sorgente rilevata → destinazione; scegli una sorgente concreta per un controllo esatto.",
      translationConcrete:
        "Le destinazioni richiedono una rotta di traduzione installata da {source}; l'uscita nella stessa lingua di base non richiede un modello MT.",
      translationUnavailable: "traduzione non disponibile",
      captionTargets: "Lingue aggiuntive per i sottotitoli",
      captionTargetsDesc: "Solo sottotitoli: nessuna voce sintetizzata per queste lingue.",
      voiceUnavailable:
        "Il demone non ha confermato l'audio doppiato per la lingua richiesta. La voce configurata supporta {language}; la sessione è stata annullata.",
      unknownVoice: "nessuna lingua indicata",
      disabledVoice: "disabilitata",
      participantNotice:
        "Prima di iniziare, informa i partecipanti che la loro voce sarà elaborata da un sistema di IA e indica le uscite esterne o i collegamenti multimediali accessibili in rete. Spetta all'operatore individuare e documentare la base giuridica; Prukka non riproduce un annuncio.",
      create: "Crea sessione",
      creating: "Creazione…",
      needTarget: "scegli almeno una lingua di destinazione",
      createFailed: "creazione fallita",
      routeTimeout: "l'uscita multimediale non è diventata pronta entro {seconds} secondi",
      delay: "Ritardo (secondi)",
      delayHint:
        "L'uscita segue la sorgente con questo scarto, così la traduzione arriva in sincrono. Più basso: più reattivo; più alto: più fluido.",
      summarySource: "Sorgente",
      summaryLanguages: "Lingue",
      summaryOutput: "Uscita",
      stepSource: "Sorgente",
      stepLanguages: "Lingue",
      stepOutput: "Uscita",
      stepStart: "Avvio",
      connectFirst: "Connetti Prukka con il pulsante in alto per iniziare.",
      callNeedsDownload: "Queste lingue richiedono un breve download per questa chiamata:",
      addMissingBroadcast: "Aggiungi una lingua che non hai",
    },
    sessions: {
      title: "Sessioni",
      empty: "Nessuna sessione — usa la procedura qui sopra oppure",
      session: "Sessione",
      source: "Sorgente",
      status: "Stato",
      statusStarting: "Avvio",
      statusRunning: "In esecuzione",
      statusFinished: "Terminata",
      statusFailed: "Errore",
      statusUnknown: "Sconosciuto",
      languages: "Lingue",
      delay: "Ritardo",
      actions: "Azioni",
      push: "Invia",
      pushTitle: "invia una lingua a RTMP/SRT o a un dispositivo",
      remove: "rimuovi sessione",
      removeSession: "rimuovi sessione",
      addLanguage: "aggiungi lingua a",
      addMissing: "Aggiungi una lingua che non hai a",
      removeLang: "rimuovi",
      liveSubs: "sottotitoli live (WebVTT)",
      dubbedAudio: "audio doppiato (MPEG-TS)",
    },
    pushDialog: {
      push: "Invia",
      title: "verso una destinazione live",
      language: "Lingua",
      target: "URL di destinazione (RTMP/SRT o device://)",
      targetPicker: "Destinazione",
      customTarget: "URL di destinazione manuale",
      subtitles: "Sottotitoli",
      subsOff: "Disattivi",
      subsBurn: "Impressi nel video",
      cancel: "Annulla",
      start: "Avvia push",
      starting: "Avvio…",
      targetNotice:
        "Questa azione invia i contenuti alla destinazione scelta. Tratta come segreti le chiavi di streaming presenti negli URL.",
      failed: "push fallito",
    },
    doctor: {
      title: "Ambiente",
      loading: "Verifica dell'ambiente…",
      tokenRequired:
        "Connetti Prukka con il pulsante in alto per verificare l'ambiente.",
      unreachable: "La verifica dello stato del demone non è disponibile.",
      retry: "Riprova",
    },
    events: { title: "Eventi", empty: "Ancora nessun evento." },
    toasts: {
      notifications: "Notifiche di errore",
      dismiss: "Chiudi",
      unauthorized:
        "Token di controllo mancante o non valido — riapri la dashboard con `prukka up` oppure incolla il codice d'accesso usando il pulsante in alto.",
    },
    footer: {
      privacy:
        "L'elaborazione è progettata per restare su questo dispositivo. Le rotte configurate e un listener multimediale raggiungibile in rete possono esporre i contenuti. I file HLS restano locali e vengono rimossi cancellando la sessione o con un arresto regolare; il successivo avvio elimina i residui di un arresto anomalo.",
      resources: "Risorse del progetto",
      dataProtection: "Protezione dei dati",
      accessibility: "Accessibilità",
      source: "Sorgenti e documentazione",
    },
    settings: {
      title: "Impostazioni",
      loading: "Caricamento delle impostazioni…",
      retry: "Riprova",
      voices: "Voci",
      voicesLabel: "Voci sintetizzate (doppiaggio)",
      voicesHint: "Disattivate: tutte le destinazioni diventano solo sottotitoli.",
      defaults: "Valori predefiniti di sessione",
      defaultLangs: "Lingue di destinazione predefinite",
      defaultLangsHint:
        "Il rilevamento automatico offre solo le lingue dichiarate dalle coppie MT installate. I valori predefiniti non supportati restano visibili per consentirne la rimozione.",
      subs: "Sottotitoli",
      subsOff: "Disattivi",
      subsVtt: "Attivi (WebVTT)",
      subsBurn: "Impressi nel video",
      bed: "Livello del sottofondo",
      delay: "Ritardo (secondi)",
      save: "Salva impostazioni",
      saving: "Salvataggio…",
      saved: "Impostazioni salvate e applicate.",
      saveFailed: "salvataggio fallito",
      loadFailed: "impossibile caricare la configurazione",
      tokenRequired: "Inserisci un token di controllo valido per vedere e modificare le impostazioni.",
      restartNote: "Si applica dopo il riavvio del demone:",
    },
    languages: {
      title: "Lingue",
      lead:
        "Installa il motore vocale e i pacchetti lingua che usa per trascrizione, traduzione e voci locali. Tutto viene scaricato su questa macchina e funziona in locale.",
      engineCore: "Motore di base",
      ready: "Pronto",
      installed: "Installata",
      partial: "Installazione parziale",
      available: "Disponibile",
      install: "Installa",
      installing: "Installazione…",
      remove: "Rimuovi",
      removing: "Rimozione…",
      phaseDownload: "Scaricamento",
      phaseVerify: "Verifica",
      phaseInstall: "Installazione",
      lastLanguageHint: "L'ultima lingua installata non può essere rimossa.",
      catalogUnreachable: "Il catalogo dei pacchetti non è raggiungibile — controlla la connessione.",
      retry: "Riprova",
      tokenRequired: "Inserisci un token di controllo valido per gestire le lingue.",
      loading: "Caricamento dei pacchetti lingua…",
      loadFailed: "impossibile caricare i pacchetti lingua",
      actionFailed: "operazione sui pacchetti lingua non riuscita",
      sizeMiB: "{size} MiB",
    },
    add: {
      button: "Aggiungi {language} · {size} MB",
      sub: "Scarica {language} così puoi usarla qui.",
      connect: "Connetti Prukka per aggiungere le lingue.",
      unsupported: "Questo demone non offre il download dei pacchetti lingua.",
      catalogError: "Catalogo delle lingue non raggiungibile — controlla la connessione.",
      retry: "Riprova",
      busyOther: "Attendi — sto completando il download in corso.",
      preparing: "Sto preparando Prukka…",
      phaseDownload: "Scaricamento",
      phaseVerify: "Verifica",
      phaseInstall: "Installazione",
      step: "Passo {index} di {count}",
      size: "{done} MB di {total} MB",
      cancel: "Annulla",
      done: "{language} aggiunta — è pronta all'uso.",
      errorTitle: "Non siamo riusciti ad aggiungere {language}.",
      tryAgain: "Riprova",
    },
    home: {
      ready: "Prukka è pronto.",
      translating: "Prukka sta traducendo ora.",
      attention: "Prukka richiede attenzione.",
    },
    getStarted: {
      title: "Prepara Prukka",
      lead:
        "Installa il motore vocale locale: il suo runtime più il modello di base da {size} MiB. Trascrizione, traduzione e voci girano poi su questa macchina.",
      leadUnsized:
        "Installa il motore vocale locale: il suo runtime più il modello di base. Trascrizione, traduzione e voci girano poi su questa macchina.",
      install: "Installa il motore vocale",
      installing: "Installazione del motore vocale…",
    },
    unlock: {
      connect: "Connetti Prukka",
      connected: "Connesso",
      explain:
        "Esegui `prukka up` per aprire la dashboard già connessa, oppure incolla il codice d'accesso dal file control.token di Prukka.",
      field: "Codice d'accesso",
      apply: "Sblocca",
      disconnect: "Disconnetti",
    },
  },
};
