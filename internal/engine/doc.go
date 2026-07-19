// Package engine implements the native speech provider's STT, MT and TTS stdio
// protocols. It runs as hidden subcommands of the single prukka binary, which
// self-executes it against the downloaded runtime bundle. The bundle root — the
// directory holding the compiled helpers, shared libraries and models — is
// passed through the PRUKKA_ENGINE_ROOT environment variable, since a
// self-executed helper's os.Executable resolves to the daemon, not the bundle.
package engine
