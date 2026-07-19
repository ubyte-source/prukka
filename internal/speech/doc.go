// Package speech installs and inventories the managed local speech-engine
// bundle: the per-platform runtime (whisper-server, mt, piper and their
// libraries) plus arch-independent model packs (STT models, one directed MT
// route per pack, one voice per pack). Artifacts are described by a catalog
// pinned to a release URL and trusted by transport alone, because every
// artifact it lists carries its own SHA-256, verified before the download is
// staged and atomically published under the daemon state directory, mirroring
// the managed ffmpeg installer.
package speech
