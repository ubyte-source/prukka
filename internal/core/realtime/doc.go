// Package realtime is the streaming dubbing core: one Lane per session,
// driving that session's audio through transcription, incremental translation
// and synthesis with the three stages overlapping in time. It defines the
// provider ports a lane consumes and knows nothing of WebSockets, HTTP, ffmpeg
// or devices.
package realtime
