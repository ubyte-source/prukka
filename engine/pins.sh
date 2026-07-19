# shellcheck shell=bash
#
# pins.sh is the single source of truth for every upstream pin the engine
# recipes consume: immutable git commits, release versions, SHA-256 content
# checksums, download URLs and model/voice names. It is sourced — never
# executed — by build.sh (runtime + full local bundle), packs.sh (model
# packs) and notice.sh (third-party notice), so the three scripts can never
# drift apart on what exactly ships.
#
# Every git source is pinned to an immutable commit (tags can be force-moved),
# and every downloaded artifact is checksum-verified by the consuming script,
# so an upstream that changes cannot silently alter the bundle. The build-time
# Python wheels (used only to convert the Marian MT models, never shipped) are
# version-pinned but not yet checksum-pinned; that remains the documented
# supply-chain gap.

# ---- STT: whisper.cpp ------------------------------------------------------
WHISPER_CPP_REPO="https://github.com/ggml-org/whisper.cpp"
WHISPER_CPP_COMMIT="080bbbe85230f624f0b52127f1ae1218247989f9" # ggml-org/whisper.cpp

# Whisper model artifacts are served from a pinned revision of the
# ggerganov/whisper.cpp Hugging Face repository.
WHISPER_MODELS_COMMIT="c521a4b02f422512d734391fdf08bb08c0862f68" # ggerganov/whisper.cpp models
WHISPER_MODELS_URL="https://huggingface.co/ggerganov/whisper.cpp/resolve/$WHISPER_MODELS_COMMIT"
WHISPER_MODEL="ggml-base.bin"           # broadcast-quality default
WHISPER_MODEL_SHA256="60ed5bc3dd14eea856493d334349b405782ddcaf0028d4b5df4088345fba2efe"
WHISPER_CALL_MODEL="ggml-tiny-q5_1.bin" # multilingual low-latency override
WHISPER_CALL_MODEL_SHA256="818710568da3ca15689e31a743197b520007872ff9576237bda97bd1b469c3d7"

# ---- MT engine: CTranslate2 ------------------------------------------------
CT2_VERSION="4.8.1"
CT2_REPO="https://github.com/OpenNMT/CTranslate2"
CT2_COMMIT="399239a790ad0da4e4363e0dcbb83495b5abd742" # OpenNMT/CTranslate2 v4.8.1

# ---- MT tokenizer: SentencePiece -------------------------------------------
SENTENCEPIECE_VERSION="0.2.0"
SENTENCEPIECE_REPO="https://github.com/google/sentencepiece"
SENTENCEPIECE_COMMIT="17d7580d6407802f85855d2cc9190634e2c95624" # google/sentencepiece v0.2.0

# ---- MT models: Helsinki-NLP Opus-MT (Tatoeba, Marian format) ---------------
# SentencePiece (spm32k) tokenization — no torch needed for conversion, and no
# Moses/BPE/perl at runtime. Both directions ship so a lane can dub either way
# and a two-way call both ways.
MT_MODEL_URL="https://object.pouta.csc.fi/Tatoeba-MT-models/ita-eng/opus-2021-02-18.zip"
MT_MODEL_SHA256="d776d13be1e20d965118ca28a28c1f30e6616b327f0d92cfa7aa0f2b47b5e6e7"
MT_EN_IT_MODEL_URL="https://object.pouta.csc.fi/Tatoeba-MT-models/eng-ita/opus+bt-2021-04-14.zip"
MT_EN_IT_MODEL_SHA256="c122e572b85a31a8a6f7e52690a266768840fd4df81b50599bb6f7e489f8bc0d"

# Build-time converter wheels (isolated venv, never shipped). Version-pinned
# to match the compiled libraries above; not yet checksum-pinned.
MT_CONVERTER_CT2_WHEEL="ctranslate2==$CT2_VERSION"
MT_CONVERTER_SPM_WHEEL="sentencepiece==$SENTENCEPIECE_VERSION"

# ---- TTS: Piper + piper-phonemize runtime -----------------------------------
PIPER_REPO="https://github.com/rhasspy/piper"
PIPER_VERSION="2023.11.14-2" # rhasspy/piper (bin 1.2.0)
PIPER_RELEASE_URL="$PIPER_REPO/releases/download/$PIPER_VERSION"
PIPER_SHA256_X64="ced85c0a3df13945b1e623b878a48fdc2854d5c485b4b67f62857cf551deaf8b"
PIPER_SHA256_AARCH64="6b1eb03b3735946cb35216e063e7eebcc33a6bbf5dd96ec0217959bf1cdcb0cc"
# Unlike macOS, the Linux and Windows piper archives are self-contained: they
# already ship piper(.exe) plus every runtime library it links (espeak-ng,
# piper_phonemize, onnxruntime, tashkeel), so no separate piper-phonemize
# download is needed on those platforms.
PIPER_SHA256_LINUX_X86_64="a50cb45f355b7af1f6d758c1b360717877ba0a398cc8cbe6d2a7a3a26e225992"
PIPER_SHA256_LINUX_AARCH64="fea0fd2d87c54dbc7078d0f878289f404bd4d6eea6e7444a77835d1537ab88eb"
PIPER_SHA256_WINDOWS_AMD64="f3c58906402b24f3a96d92145f58acba6d86c9b5db896d207f78dc80811efcea"
# The piper macOS tarball ships no dylibs (upstream packaging gap): the piper
# binary references @rpath/libespeak-ng, libpiper_phonemize and libonnxruntime
# that only the piper-phonemize release tarball provides.
PIPER_PHONEMIZE_REPO="https://github.com/rhasspy/piper-phonemize"
PIPER_PHONEMIZE_VERSION="2023.11.14-4" # rhasspy/piper-phonemize
PIPER_PHONEMIZE_RELEASE_URL="$PIPER_PHONEMIZE_REPO/releases/download/$PIPER_PHONEMIZE_VERSION"
PIPER_PHONEMIZE_SHA256_X64="9ec6e300c0d012a663758bc45a097b47ee759761a3b91c7742de042af789d84b"
PIPER_PHONEMIZE_SHA256_AARCH64="78a9c28b3c94baf6e9526b2e386ce547909abaec4f31aadd7e16b01fbfe5f322"

# ---- TTS voices: rhasspy/piper-voices ---------------------------------------
# Voice payloads are checksum-pinned below regardless of the (mutable) branch
# ref they are served from. Each voice directory also carries its MODEL_CARD,
# which states that voice's own license terms.
PIPER_VOICES_URL="https://huggingface.co/rhasspy/piper-voices/resolve/main"
PIPER_VOICE="en_US-lessac-medium" # rhasspy/piper-voices (English)
PIPER_VOICE_PATH="en/en_US/lessac/medium"
PIPER_VOICE_SHA256="5efe09e69902187827af646e1a6e9d269dee769f9877d17b16b1b46eeaaf019f"
PIPER_VOICE_JSON_SHA256="efe19c417bed055f2d69908248c6ba650fa135bc868b0e6abb3da181dab690a0"
PIPER_VOICE_IT="it_IT-paola-medium" # rhasspy/piper-voices (Italian)
PIPER_VOICE_IT_PATH="it/it_IT/paola/medium"
PIPER_VOICE_IT_SHA256="6fc918b5a0ea6137382833dddfa567bffbe6a5060c02043c87192ee59c04210c"
PIPER_VOICE_IT_JSON_SHA256="aea19c0a7fce29fbc359b93f10e7902854401e4c95ae2ea328ae516b15d296cf"

# ---- Pivot languages: Opus-MT en<->X (both directions) + one Piper voice -----
# Each language here translates to and from every other through the English hub
# (internal/providers/pivot), so en<->X plus one voice is enough for any-to-any;
# no N^2 matrix of direct pairs. Consumed by packs.sh as a whitespace-separated
# table, one row per language:
#   iso1 iso3 voice voice_dir enx_url enx_sha xen_url xen_sha onnx_sha json_sha
# Every URL is content-pinned by the sha256 beside it, exactly like the it<->en
# pins above. Voice payloads carry their own MODEL_CARD license, fetched by
# packs.sh. This table is generated by engine/pin-languages.py — rerun it to add
# a language or refresh a checksum, then paste its output between the markers.
PIVOT_LANGS="$(cat <<'TSV'
ar ara ar_JO-kareem-medium ar/ar_JO/kareem/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-ara/opus+bt-2021-04-13.zip cac8acad75d888ced1baa96e838fe12177ad975a194e32a3120fffdcb5f88df8 https://object.pouta.csc.fi/Tatoeba-MT-models/ara-eng/opus+bt-2021-04-30.zip 16efc0177e555fe99a96593610b32bea12f8353a69691400838654de65bbf6cd 9e95cab07b679da603bba17c4dec7ab3111320571964ee95c0379603c086491e ea6d9b9d9076dbdb6bf5c98c6a141ef154959d2359709b37855727964e7d6c4d
bg bul bg_BG-dimitar-medium bg/bg_BG/dimitar/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-bul/opus+bt-2021-04-13.zip 189d3767749a2680aca8b1b716b3a00c3917f78d05764cc65ea3863c186c5376 https://object.pouta.csc.fi/Tatoeba-MT-models/bul-eng/opus+bt-2021-04-30.zip eefb966dece8225dd1208f250f0203e1f7fde9d5f363cd89e82f0efd85e633dd 4972fe764468e8501416407ad81662de94cc6c9cdc680fcf807daef04e319f13 ec9a9abdd17384d3db225e83085b2f68b790b112f058417c3a8a2ac58b79e7f0
ca cat ca_ES-upc_ona-medium ca/ca_ES/upc_ona/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-cat/opus+bt-2021-04-10.zip 2506f114d78a4949072ab133408df846dedb54ca946af9c33e5fe953a3bd009a https://object.pouta.csc.fi/Tatoeba-MT-models/cat-eng/opus+bt-2021-04-30.zip 5c00adc950e08cd78fe01bcfadcb2101ff5083af7fd33f4cfa2f9a08a5ccf57e fdb652db8c11a4475527346cf3241cb064d1ba393cf370f3f2ec09a872d118fd 7f76acc9c06f4eda9e6aef2997b75782d97855aab48d4b401eb956a6e655eddc
cs ces cs_CZ-jirka-medium cs/cs_CZ/jirka/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-ces/opus+bt-2021-04-13.zip da1793f7882725dddd8fcbe029b392da194afe60eeab80df2125699d628df122 https://object.pouta.csc.fi/Tatoeba-MT-models/ces-eng/opus+bt-2021-04-30.zip 3fecfd90f5f0b4dff63b5cbb74da28f92ce0288d824b9d6285836cdbd029be63 cbd5c900acacc8e8cbecd64347abb8de39c00a9d3104bed06fee92e4f319efc8 fb38b1799b7354808227c065efa97b1ffa2b0cde59505babb56a36d35af9c637
da dan da_DK-talesyntese-medium da/da_DK/talesyntese/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-dan/opus+bt-2021-04-13.zip febad0f4b45505f768101e0fc8a07f31e7184722e78eb3598efe65ada6ae1859 https://object.pouta.csc.fi/Tatoeba-MT-models/dan-eng/opus+bt-2021-04-30.zip 373873994609fa23bc89e84d0c33fe936ed7e5240c5e992e6d2a2e8cd1fbfd2e b9271efd25f7b8494bbd28d48dd675c8c119daa284f3ee488008935f515f1241 89fe13bd251406cc0088570d103ea7ac35823211d8466faf913268ab8506f41b
de deu de_DE-mls-medium de/de_DE/mls/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-deu/opus+bt-2021-04-13.zip ca3aa1eb966745009887e842d488c45f3706a704613af2de5360874250b0ee89 https://object.pouta.csc.fi/Tatoeba-MT-models/deu-eng/opus+bt-2021-04-30.zip 53d2ce3c9ba04eadeaf83e52733cd35ef9f919bfbe033bbd870128d03747cc4d 69cd1d2aa5a35839a518966fcc4924b5f93e5f8c948ed0752b1a616ad53f65bf b0af1c89ddfdc72d32e015729b0e89b99eec13c2c8caa1db7488d98e9e570b40
el ell el_GR-joy-medium el/el_GR/joy/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-ell/opus+bt-2021-04-13.zip d89ed5ffce4ba6b7be8dd0869cbaab1abe57dc72e19aa0cc67b6e51e89f8f31a https://object.pouta.csc.fi/Tatoeba-MT-models/ell-eng/opus+bt-2021-04-30.zip 4b32049160ad8a04d3dfbb30fd9715d8ceaa55d4f196b02dd0f064fd68d260d1 31b1d51dc72dd43a5429b67798e27599f8cdcfce8a80265bc6f50d932bf144bc 451a8885a0ea72e4432fffe22e65a75bbd2e65f6757273d1a5a9ed8a3419852e
es spa es_ES-davefx-medium es/es_ES/davefx/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-spa/opus+bt-2021-04-10.zip 6eef47445566fe881c2ed4610ae241b77f18f13337acdd87e37213433bed1446 https://object.pouta.csc.fi/Tatoeba-MT-models/spa-eng/opus+bt-2021-04-30.zip 8afd6970e8f3c5e96e34979fa4886aa3030972f7a060203d3bebc7114d280821 6658b03b1a6c316ee4c265a9896abc1393353c2d9e1bca7d66c2c442e222a917 0e0dda87c732f6f38771ff274a6380d9252f327dca77aa2963d5fbdf9ec54842
fa fas fa_IR-amir-medium fa/fa_IR/amir/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-fas/opus+bt-2021-04-10.zip 568dc8f9d355632e76458c996d98ace54c7eab34ee4f24956a9f8571b67e4450 https://object.pouta.csc.fi/Tatoeba-MT-models/fas-eng/opus-2021-02-23.zip efe889f5cfc519448ee6f3f981f997f01408a24a533fe08e4c1f707577bbeed5 fb815380d969ea372b0b21b0de14421f58fe481047e153e69685d079b6e1a9d1 75f918a3bf0f57a9179abe725af529f2a5c79d6c899e2a84aec76c685d5dfb9a
fi fin fi_FI-harri-medium fi/fi_FI/harri/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-fin/opus+bt-2021-03-09.zip c128bda3aba5b4ad2161f80120c1079592db3dcb57be05a37fb55cfbff0ee085 https://object.pouta.csc.fi/Tatoeba-MT-models/fin-eng/opus+bt-2021-04-30.zip d5d930e302e9934d9bda2341db1f32df5cf4544a0bd799c06a1fef02e137dfc3 a44167faa34caed940e4fcad139fcc35922266b2593bcebe77701774c0fb2389 3f9c9f76f74adf1fbe7279e41eea17d6610757e45effd6808bbea6be74b8916d
fr fra fr_FR-mls-medium fr/fr_FR/mls/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-fra/opus-2021-02-15.zip 06e317fe4cd0975a7bc33fdbf6d0fffdbe4e10d295a06078c0ca264f8accdb7f https://object.pouta.csc.fi/Tatoeba-MT-models/fra-eng/opus+bt-2021-04-30.zip 2c960d031fbd26d77038ce55cf26ec4aba8b95307179b15a12f2ce90288885ae 0ed223f78466917f2bae05ee90096ce69ab1fdeb251f55590d0e7422d234e162 252b0b0a6e4cc4949e23eccb956f9c779986c32f934f2f7e2191e5fdc2edca61
hi hin hi_IN-pratham-medium hi/hi_IN/pratham/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-hin/opus+bt-2021-04-10.zip 48801b2a15e2ab9985e78154a1544a757895dbc339806cdccf06c1faf4e35a47 https://object.pouta.csc.fi/Tatoeba-MT-models/hin-eng/opus+bt-2021-04-30.zip 7abcbbd2602b058657ea290e90173710cc602265140151184947c439f7b50326 169964b0871667f6793416d4b35e97357a68ba1ad01df8580c28048989ee7693 b68edd2cd7950dd436314013b7cd12e9699e5a3f6fe5af5af94294cf6aa7b9fd
hu hun hu_HU-anna-medium hu/hu_HU/anna/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-hun/opus+bt-2021-04-13.zip 76cb518a6385b6742eef45ebd9f45c6eb9aaf429247333e0655eb131b4a365c9 https://object.pouta.csc.fi/Tatoeba-MT-models/hun-eng/opus+bt-2021-04-30.zip 3d1a96173e94c9c87a363ac99b900521372c9c0e840fff16f7de493258e6395a 968c0c3a66cb667811242cc88653bff9247395fc7a0517fbeef7d8c08cdae62a ccf967d8db8018c9d8ffdb0edc8814ffcb6b75273bb0d84337317240f710283a
is isl is_IS-bui-medium is/is_IS/bui/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-isl/opus+bt-2021-03-07.zip 8c98db429468cea01383eb7f219d91327ca50dfb2222a3d957315ad20ed94c5b https://object.pouta.csc.fi/Tatoeba-MT-models/isl-eng/opus+bt-2021-04-30.zip d00587ccd046f30ca3d1d6fb3f54199a9616528dc87048e0f46a295dff64ac7c 3a645b2d2850e4098f01f3765cece931836c03741e01a5cc514d09d39d37c05c 3cae728572fbb397713d047f2299247bb76b62639d9dfdcd65b26c578b8aba45
lv lav lv_LV-aivars-medium lv/lv_LV/aivars/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-lav/opus+bt-2021-04-14.zip 6bf783255eb37c3ba5f7630dd2ab32998115281051c5f659ee042eae7426504a https://object.pouta.csc.fi/Tatoeba-MT-models/lav-eng/opus+bt-2021-04-30.zip 569b438caf20e9be8215d24cf610f518eb44c3c07f4dcc95e90792d258d88b88 9d855a47c22e2b94795be9e0eb9e8c4c02ce251dc89461dede94de20ff08bd8e 08ae2c297be8aa04f15f3f97b7ffeae0146b30b0bd8f7baebcdc46bc2c2f33dc
nl nld nl_BE-nathalie-medium nl/nl_BE/nathalie/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-nld/opus+bt-2021-04-14.zip 5ed0d9226a915722d0666ada2fb83265d6b52dfcfb4a23e0186bb35932f1279b https://object.pouta.csc.fi/Tatoeba-MT-models/nld-eng/opus+bt-2021-04-30.zip 730aa9d3d0f19885091daeeece37711f783afc8e9458c7de7e090aae618f8996 49cf48023861f9fd42e13a8632f068fee67d1ce244a6ee38f29595afbf0a6be4 4704af2736022e910a3f32672480d5530dd39da5c2bcc079f315f604166ff0de
pl pol pl_PL-darkman-medium pl/pl_PL/darkman/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-pol/opus+bt-2021-04-14.zip 3b7b6311e0c0de40546f62b52baa8f64ac3384c4d3f25f7b5dc8c60a4d37dc99 https://object.pouta.csc.fi/Tatoeba-MT-models/pol-eng/opus+bt-2021-04-30.zip 94dc2b0cfa547c9b3dcd8cdab5ed6e41502848160d79ea480f25d4b2ed699a4f db505438a5364e8e2e0242c4324130a873ed660dfbe8d9689cef428ffb1b645f 70f999f11fa8ad13d3ef779041ee93c9f38be5abdbacdfad42449712fe91c81b
pt por pt_BR-cadu-medium pt/pt_BR/cadu/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-por/opus+bt-2021-04-14.zip 566ca26aa9e8a0d3a161511265a56915460b4589c5d4720c83a0b77de35e147b https://object.pouta.csc.fi/Tatoeba-MT-models/por-eng/opus+bt-2021-04-30.zip ead25b7241821c1d83978e8ec62abdeebf4e9c7ff1357ad58e4a915e5e050a5f 765f0809a6ea9035d4a6d0d008dbf8876e68b2dd32029312672fa8f405bdb535 5fe03aa3d4901880554905b12075713cd552598c8a350455a1ec73f8b4e6be19
ro ron ro_RO-mihai-medium ro/ro_RO/mihai/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-ron/opus+bt-2021-03-07.zip 49f6aff7bc8f866f030c29875b9e193386767be94a326f1adacd02247cea2730 https://object.pouta.csc.fi/Tatoeba-MT-models/ron-eng/opus+bt-2021-04-30.zip f968267742e8e7d5d83f802a17b2a6fcff9432ae13c0b37e61f9dadf1327b90e e0608bbbd53c80267c09ece681b09f5199f54e792356684c8073738e5f15d29f 8cc0c9f077dc0cec3c25a6a055ec8046db8e40a2510591582f2c9c869f4bc47e
ru rus ru_RU-denis-medium ru/ru_RU/denis/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-rus/opus+bt-2021-04-14.zip 12ef1f0f47df54b94892a9a3bf17ed822a10fcce9515f4769090c6feb492212f https://object.pouta.csc.fi/Tatoeba-MT-models/rus-eng/opus+bt-2021-04-30.zip f4bb765e6e5d42d755da01dbd370e3fc055819eea29404ce5a7d96f03b495451 15fab56e11a097858ee115545d0f697fc2a316c41a291a5362349fb870411b0a 831c860dac0b5073eaa81610a0a638ec23d90a6cf8e5f871b4485c2cec3767c8
sl slv sl_SI-artur-medium sl/sl_SI/artur/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-slv/opus+bt-2021-05-15.zip b4a9e14467b4841614761c6517f4e79f8983b8950245bac158deaa67b4bf600a https://object.pouta.csc.fi/Tatoeba-MT-models/slv-eng/opus-2021-02-19.zip 83abbec942a7a416ccad7c9f625671d150f0a5a19b5411423e9caaf77994dec5 9222ed93ef425524ad4be0b083369af8ea8db18455576a6016b154192f4ed38c 741283430f1fa2be5c61717c6f1fe795a7b9f537491927340dd12f90f3b3cc04
sv swe sv_SE-alma-medium sv/sv_SE/alma/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-swe/opus+bt-2021-04-14.zip 1446a6b6d6bcdc6e862eb0cedd733f781ed9d1ee88fbb0bdb2f324549f16b0f9 https://object.pouta.csc.fi/Tatoeba-MT-models/swe-eng/opus+bt-2021-04-30.zip 2c19bd94e20abc47fc8c58eefab1dcd5de7bd887c6f3eebe0aa15c809769cba1 f75455eb6afeb1db9e2905d5bd7e4115afcd3710e0d399b6a40981766d32fedc 6924380892f769afa92fc6b28ff91d558690d7fb4e3ef8cbf821cefadc8f38fe
sw swa sw_CD-lanfrica-medium sw/sw_CD/lanfrica/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-swa/opus+bt-2021-04-10.zip ca428548804ca7cd83c82c8c818be66588603aeaee7e75dcc0e2abcb201dca58 https://object.pouta.csc.fi/Tatoeba-MT-models/swa-eng/opus+bt-2021-04-30.zip eab2d5eade8da4e4bb6d706f8f91e210c8b2e3ac90ecc85fee88e629479bb29f 1f195ed12ca5e7875114618e5f00207af364602e21ca78c8a6d3d7674f9259fa 5bd6f6ad659aa8f1f89f414e23a3df84fc753eb9c066e91fe86729da2ad4c1fc
tr tur tr_TR-dfki-medium tr/tr_TR/dfki/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-tur/opus+bt-2021-04-10.zip f1f074185660501c977b523576775eef7438e35eb938712702ab33cf32061a3a https://object.pouta.csc.fi/Tatoeba-MT-models/tur-eng/opus+bt-2021-04-30.zip 2d12c995815916bd4669edab39b2065980a21d70cd134ade8defd92b241d73d2 2844717f524ab965d3fe86e60562cbb601d3e456836efcc2196cc3a14112a8fb 13ebd7810f1b61b5027583cf3131a0a233b6ea81c38f2200ebc4ff41c3cca039
uk ukr uk_UA-ukrainian_tts-medium uk/uk_UA/ukrainian_tts/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-ukr/opus+bt-2021-04-14.zip 2c62f71674dd3008a5377c108e66aeb06a66137caeacf14da18e09024ea2eb26 https://object.pouta.csc.fi/Tatoeba-MT-models/ukr-eng/opus+bt-2021-04-30.zip b624d11c212916984d8df47140cdaefc4d06ae92edf8e45df9a616e392c9ab13 7920419ac5f6fd8b6450520f24b52ed5a319cb53dd018fbcd71c9e079cbac84f 4e96e72917ca9b94edc77d6ccfee03a73f450ba2fc1ca93c2e562bc014e5aa55
vi vie vi_VN-vais1000-medium vi/vi_VN/vais1000/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-vie/opus+bt-2021-04-10.zip c8578043465eea7ec520e2b83c68f1b00b1df99a743fb27549c7d6ec763e7082 https://object.pouta.csc.fi/Tatoeba-MT-models/vie-eng/opus+bt-2021-04-30.zip d157f54d50c5ee934609f1a6dddeb3ec66afd95db9db86881bf969fff1b61a00 ec7c89e2c85f4d1edc24b6120c18aaf1bda614f06b511567eb9c7c0de15e2dab fafb9da1354ed4b77c31af228ed41fb41cd825c14cffa105454b25e6ae751ee0
zh zho zh_CN-chaowen-medium zh/zh_CN/chaowen/medium https://object.pouta.csc.fi/Tatoeba-MT-models/eng-zho/opus+bt-2021-04-19.zip 5fbee517df7f3a12e84f8081dfacf37decfebeb17d7e54229211ead6c11ab4c9 https://object.pouta.csc.fi/Tatoeba-MT-models/zho-eng/opus+bt-2021-04-30.zip 8b1c6716297270eaaebe15fa7ea2b8b93e6631d62c3bccf762fa8b3a8bca46dc 820d64ac16048fbcf38dd0823d37fab5f5e0c2bd71b01ca5a50f553fac19e746 a6bb2caafa0645642f13cbf7e2f6fbbb16fded66e51109fc26d622f6472fa16f
TSV
)"
