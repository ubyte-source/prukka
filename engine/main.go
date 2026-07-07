// Command prukka implements the native provider's STT, MT, and TTS stdio
// protocols using helpers and models resolved relative to its executable.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: prukka stt|mt|tts [flags]")
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "stt":
		err = runSTT(os.Args[2:])
	case "mt":
		err = runMT(os.Args[2:])
	case "tts":
		err = runTTS(os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "prukka:", err)
		os.Exit(1)
	}
}
