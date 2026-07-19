// Package speech installs and inventories the managed local speech-engine
// bundle: the per-platform runtime (orchestrator, whisper-server, mt, piper
// and their libraries) plus arch-independent model packs (STT models, one
// directed MT route per pack, one voice per pack). Artifacts are described by
// a signed-by-checksum catalog pinned to a release URL; every download is
// SHA-256 verified before it is staged and atomically published under the
// daemon state directory, mirroring the managed ffmpeg installer.
package speech
