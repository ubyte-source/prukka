// Package pipeline hosts the real-time audio stages the lane lays onto each
// language timeline: scheduling takes onto a track, mixing, ducking and PCM
// coding. Every stage works in one canonical format and allocates nothing per
// frame.
package pipeline
