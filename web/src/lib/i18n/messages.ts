// The dashboard's message catalog. Messages is the single shape both
// locales must satisfy, so a missing translation fails the type check.

export interface Messages {
  tagline: string;
  stats: { sessions: string; uptime: string; version: string };
  wizard: {
    title: string;
    name: string;
    source: string;
    sourceLang: string;
    autoDetect: string;
    subtitles: string;
    subsOn: string;
    subsOff: string;
    dubbing: string;
    targets: string;
    token: string;
    create: string;
    needTarget: string;
    createFailed: string;
  };
  sessions: {
    title: string;
    empty: string;
    session: string;
    source: string;
    languages: string;
    budget: string;
    delay: string;
    push: string;
    pushTitle: string;
    remove: string;
    removeSession: string;
    addLanguage: string;
    removeLang: string;
    liveSubs: string;
    dubbedAudio: string;
  };
  pushDialog: {
    title: string;
    language: string;
    target: string;
    subtitles: string;
    subsOff: string;
    subsVtt: string;
    subsBurn: string;
    cancel: string;
    start: string;
    failed: string;
  };
  doctor: { title: string; unreachable: string };
  events: { title: string; empty: string };
  footer: { privacy: string; source: string };
  settings: {
    title: string;
    backend: string;
    backendOpenrouter: string;
    backendOpenrouterDesc: string;
    backendLocal: string;
    backendLocalDesc: string;
    clone: string;
    cloneOff: string;
    cloneOffDesc: string;
    clonePitch: string;
    clonePitchDesc: string;
    cloneCartesia: string;
    cloneCartesiaDesc: string;
    baseUrl: string;
    sttModel: string;
    sttBaseUrl: string;
    mtModel: string;
    mtBaseUrl: string;
    ttsModel: string;
    ttsBaseUrl: string;
    ttsVoice: string;
    temperature: string;
    eurPerUsd: string;
    timeout: string;
    model: string;
    apiKey: string;
    keySet: string;
    keyUnset: string;
    keyPlaceholder: string;
    saveKey: string;
    keySaved: string;
    keyFailed: string;
    defaults: string;
    defaultLangs: string;
    subs: string;
    subsOff: string;
    subsVtt: string;
    subsBurn: string;
    bed: string;
    delay: string;
    budgets: string;
    budgetPerSession: string;
    hardStop: string;
    hardStopDesc: string;
    privacy: string;
    transcripts: string;
    storeAudio: string;
    storeAudioDesc: string;
    save: string;
    saved: string;
    saveFailed: string;
    loadFailed: string;
    restartNote: string;
  };
}

export const locales = ["en", "it"] as const;

export type Locale = (typeof locales)[number];

export const messages: Record<Locale, Messages> = {
  en: {
    tagline: "Every stream, every language — one bridge.",
    stats: { sessions: "sessions", uptime: "uptime", version: "version" },
    wizard: {
      title: "New session",
      name: "Name",
      source: "Source URL",
      sourceLang: "Source language",
      autoDetect: "Auto-detect",
      subtitles: "Subtitles",
      subsOn: "On (WebVTT)",
      subsOff: "Off",
      dubbing: "Dubbing",
      targets: "Target languages",
      token: "control token (auto-filled by `prukka up`)",
      create: "Create session",
      needTarget: "pick at least one target language",
      createFailed: "create failed",
    },
    sessions: {
      title: "Sessions",
      empty: "No sessions yet — use the wizard above or",
      session: "Session",
      source: "Source",
      languages: "Languages",
      budget: "Budget €/h",
      delay: "Delay",
      push: "Push",
      pushTitle: "push a language to RTMP/SRT or a device",
      remove: "remove session",
      removeSession: "remove session",
      addLanguage: "add language to",
      removeLang: "remove",
      liveSubs: "live subtitles (WebVTT)",
      dubbedAudio: "dubbed audio (MPEG-TS)",
    },
    pushDialog: {
      title: "to a live target",
      language: "Language",
      target: "Target URL (RTMP/SRT or device://)",
      subtitles: "Subtitles",
      subsOff: "Off",
      subsVtt: "Sidecar (WebVTT)",
      subsBurn: "Burned into video",
      cancel: "Cancel",
      start: "Start push",
      failed: "push failed",
    },
    doctor: { title: "Environment", unreachable: "daemon unreachable" },
    events: { title: "Events", empty: "No events yet." },
    footer: {
      privacy:
        "Audio segments and transcripts are sent to OpenRouter and its routed providers; nothing else leaves this machine. No audio is stored by default.",
      source: "Source & docs",
    },
    settings: {
      title: "Settings",
      backend: "Inference backend",
      backendOpenrouter: "OpenRouter (hosted)",
      backendOpenrouterDesc:
        "Audio and text go to the hosted API. No GPU needed; per-use cost.",
      backendLocal: "Local (OpenAI-compatible)",
      backendLocalDesc:
        "Everything runs on your own machine — Ollama, whisper.cpp, LocalAI, LM Studio, vLLM. Nothing leaves the host.",
      clone: "Voice adaptation",
      cloneOff: "Preset voices",
      cloneOffDesc: "Each speaker gets a distinct register-matched preset.",
      clonePitch: "Register matching",
      clonePitchDesc:
        "Every take is re-pitched onto the speaker's own fundamental. In-engine, any backend, no key.",
      cloneCartesia: "Timbre cloning (Cartesia)",
      cloneCartesiaDesc:
        "Each speaker is cloned from their own audio and dubbed in their own voice. Cloud; requires consent of the person cloned.",
      baseUrl: "Base URL",
      sttModel: "Transcription model",
      sttBaseUrl: "Transcription server (empty = base URL)",
      mtModel: "Translation model",
      mtBaseUrl: "Translation server (empty = base URL)",
      ttsModel: "Voice model",
      ttsBaseUrl: "Voice server (empty = base URL)",
      ttsVoice: "Default voice",
      temperature: "Translation temperature",
      eurPerUsd: "EUR per USD",
      timeout: "Timeout (seconds)",
      model: "Model",
      apiKey: "API key",
      keySet: "key configured",
      keyUnset: "no key yet",
      keyPlaceholder: "paste a new key (stored in the OS keychain)",
      saveKey: "Save key",
      keySaved: "key stored in the OS keychain",
      keyFailed: "storing the key failed",
      defaults: "Session defaults",
      defaultLangs: "Default target languages",
      subs: "Subtitles",
      subsOff: "Off",
      subsVtt: "On (WebVTT)",
      subsBurn: "Burned into video",
      bed: "Background bed level",
      delay: "Delay (seconds)",
      budgets: "Budgets",
      budgetPerSession: "Per-session budget (€/h)",
      hardStop: "Hard stop",
      hardStopDesc: "Stop paid stages entirely when the budget is exhausted.",
      privacy: "Privacy",
      transcripts: "Keep transcripts (hours)",
      storeAudio: "Store audio",
      storeAudioDesc: "Keep session audio on disk (off by default).",
      save: "Save settings",
      saved: "Settings saved and applied.",
      saveFailed: "save failed",
      loadFailed: "could not load the configuration",
      restartNote: "Applies after a daemon restart:",
    },
  },
  it: {
    tagline: "Ogni stream, ogni lingua — un ponte.",
    stats: { sessions: "sessioni", uptime: "attività", version: "versione" },
    wizard: {
      title: "Nuova sessione",
      name: "Nome",
      source: "URL sorgente",
      sourceLang: "Lingua sorgente",
      autoDetect: "Rilevamento automatico",
      subtitles: "Sottotitoli",
      subsOn: "Attivi (WebVTT)",
      subsOff: "Disattivi",
      dubbing: "Doppiaggio",
      targets: "Lingue di destinazione",
      token: "token di controllo (compilato da `prukka up`)",
      create: "Crea sessione",
      needTarget: "scegli almeno una lingua di destinazione",
      createFailed: "creazione fallita",
    },
    sessions: {
      title: "Sessioni",
      empty: "Nessuna sessione — usa la procedura qui sopra oppure",
      session: "Sessione",
      source: "Sorgente",
      languages: "Lingue",
      budget: "Budget €/h",
      delay: "Ritardo",
      push: "Push",
      pushTitle: "invia una lingua a RTMP/SRT o a un dispositivo",
      remove: "rimuovi sessione",
      removeSession: "rimuovi sessione",
      addLanguage: "aggiungi lingua a",
      removeLang: "rimuovi",
      liveSubs: "sottotitoli live (WebVTT)",
      dubbedAudio: "audio doppiato (MPEG-TS)",
    },
    pushDialog: {
      title: "verso una destinazione live",
      language: "Lingua",
      target: "URL di destinazione (RTMP/SRT o device://)",
      subtitles: "Sottotitoli",
      subsOff: "Disattivi",
      subsVtt: "A parte (WebVTT)",
      subsBurn: "Impressi nel video",
      cancel: "Annulla",
      start: "Avvia push",
      failed: "push fallito",
    },
    doctor: { title: "Ambiente", unreachable: "demone non raggiungibile" },
    events: { title: "Eventi", empty: "Ancora nessun evento." },
    footer: {
      privacy:
        "Segmenti audio e trascrizioni vengono inviati a OpenRouter e ai provider instradati; nient'altro lascia questa macchina. Nessun audio viene salvato di default.",
      source: "Sorgenti e documentazione",
    },
    settings: {
      title: "Impostazioni",
      backend: "Backend di inferenza",
      backendOpenrouter: "OpenRouter (cloud)",
      backendOpenrouterDesc:
        "Audio e testo vanno all'API cloud. Nessuna GPU richiesta; costo a consumo.",
      backendLocal: "Locale (OpenAI-compatibile)",
      backendLocalDesc:
        "Tutto gira sulla tua macchina — Ollama, whisper.cpp, LocalAI, LM Studio, vLLM. Nulla lascia l'host.",
      clone: "Adattamento della voce",
      cloneOff: "Voci preset",
      cloneOffDesc: "Ogni parlante riceve un preset distinto, adatto al registro.",
      clonePitch: "Adattamento di registro",
      clonePitchDesc:
        "Ogni battuta viene risintonizzata sulla fondamentale del parlante. Integrato, qualsiasi backend, nessuna chiave.",
      cloneCartesia: "Clonazione del timbro (Cartesia)",
      cloneCartesiaDesc:
        "Ogni parlante viene clonato dal proprio audio e doppiato con la propria voce. Cloud; richiede il consenso della persona clonata.",
      baseUrl: "URL di base",
      sttModel: "Modello di trascrizione",
      sttBaseUrl: "Server di trascrizione (vuoto = URL di base)",
      mtModel: "Modello di traduzione",
      mtBaseUrl: "Server di traduzione (vuoto = URL di base)",
      ttsModel: "Modello voce",
      ttsBaseUrl: "Server voce (vuoto = URL di base)",
      ttsVoice: "Voce predefinita",
      temperature: "Temperatura di traduzione",
      eurPerUsd: "EUR per USD",
      timeout: "Timeout (secondi)",
      model: "Modello",
      apiKey: "Chiave API",
      keySet: "chiave configurata",
      keyUnset: "nessuna chiave",
      keyPlaceholder: "incolla una nuova chiave (salvata nel portachiavi di sistema)",
      saveKey: "Salva chiave",
      keySaved: "chiave salvata nel portachiavi di sistema",
      keyFailed: "salvataggio della chiave fallito",
      defaults: "Valori predefiniti di sessione",
      defaultLangs: "Lingue di destinazione predefinite",
      subs: "Sottotitoli",
      subsOff: "Disattivi",
      subsVtt: "Attivi (WebVTT)",
      subsBurn: "Impressi nel video",
      bed: "Livello del sottofondo",
      delay: "Ritardo (secondi)",
      budgets: "Budget",
      budgetPerSession: "Budget per sessione (€/h)",
      hardStop: "Stop rigido",
      hardStopDesc: "Ferma del tutto le fasi a pagamento a budget esaurito.",
      privacy: "Privacy",
      transcripts: "Conserva trascrizioni (ore)",
      storeAudio: "Salva l'audio",
      storeAudioDesc: "Conserva l'audio delle sessioni su disco (disattivo di default).",
      save: "Salva impostazioni",
      saved: "Impostazioni salvate e applicate.",
      saveFailed: "salvataggio fallito",
      loadFailed: "impossibile caricare la configurazione",
      restartNote: "Si applica dopo il riavvio del demone:",
    },
  },
};
