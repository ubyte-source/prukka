// Package engine is the streaming dubbing core. It drives one session's
// audio through transcription, incremental translation and synthesis with
// the three stages overlapping in time, so speech is heard translated within
// a second or two of being spoken.
//
// The engine defines the provider ports it consumes — Transcriber, Translator
// and Synthesizer — and knows nothing of WebSockets, HTTP, ffmpeg or devices;
// adapters implement those ports around it.
package engine
