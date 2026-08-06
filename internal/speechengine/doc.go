// Package speechengine implements the native speech provider's STT, MT and TTS
// stdio protocols, run as hidden subcommands of the single prukka binary. The
// bundle root reaches them through PRUKKA_ENGINE_ROOT, since a self-executed
// helper's os.Executable resolves to the daemon, not the bundle.
package speechengine
