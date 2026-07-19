// Package pipeline hosts the real-time audio stages the engine lays onto each
// language timeline: scheduling takes onto a track, mixing, ducking and PCM
// coding. Every stage works in one canonical format, so adapters convert only
// at the edges and no stage allocates per frame.
package pipeline
