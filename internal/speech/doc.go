// Package speech installs and inventories the managed local speech-engine
// bundle: the per-platform runtime (whisper-server, mt, piper and their
// libraries) plus arch-independent model packs. Every artifact the catalog
// lists carries its own SHA-256, verified before the download is staged and
// atomically published under the daemon state directory.
//
// Containment is the package's one security invariant, and it is stated only
// here. Every path under the engine root is reached through an *os.Root opened
// on that root — publishing, carrying, deleting and merely probing alike — so
// the kernel refuses a component that leaves the root instead of following it.
// Only the kernel can enforce it: a bundle legitimately holds symlinks the
// runtime shipped, and filepath.Clean cancels "x/.." textually while the kernel
// resolves it against whatever x points at. The string checks on archive
// members are policy, not containment.
package speech
