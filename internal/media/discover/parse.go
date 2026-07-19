package discover

import (
	"regexp"
	"strings"
)

// Section names shared by the ffmpeg listing parsers.
const (
	sectionAudio = "audio"
	sectionVideo = "video"
)

// entry is one parsed device line: the platform id and its display name.
type entry struct {
	id    string
	label string
}

// avfEntry matches one avfoundation listing line: `[3] Prukka Microphone`
// after ffmpeg's log prefix.
var avfEntry = regexp.MustCompile(`\[(\d+)\] (.+)$`)

// parseAVFoundation reads `ffmpeg -f avfoundation -list_devices true`
// output: two sections, video first, each entry an index and a name.
func parseAVFoundation(raw string) (audio, video []entry) {
	section := ""

	for line := range strings.Lines(raw) {
		switch {
		case strings.Contains(line, "AVFoundation video devices"):
			section = sectionVideo

			continue
		case strings.Contains(line, "AVFoundation audio devices"):
			section = sectionAudio

			continue
		}

		m := avfEntry.FindStringSubmatch(strings.TrimRight(line, "\n"))
		if m == nil {
			continue
		}

		switch section {
		case sectionAudio:
			audio = append(audio, entry{id: m[1], label: m[2]})
		case sectionVideo:
			video = append(video, entry{id: m[1], label: m[2]})
		}
	}

	return audio, video
}

// dshowEntry matches one dshow device line: a quoted friendly name.
var dshowEntry = regexp.MustCompile(`"([^"]+)"\s*$`)

// parseDShow reads `ffmpeg -list_devices true -f dshow -i dummy` output;
// dshow selects by friendly name, so the name is also the id. Lines
// carrying the moniker ("Alternative name …") are skipped.
func parseDShow(raw string) (audio, video []entry) {
	section := ""

	for line := range strings.Lines(raw) {
		switch {
		case strings.Contains(line, "DirectShow video devices"):
			section = sectionVideo

			continue
		case strings.Contains(line, "DirectShow audio devices"):
			section = sectionAudio

			continue
		case strings.Contains(line, "Alternative name"):
			continue
		}

		m := dshowEntry.FindStringSubmatch(strings.TrimRight(line, "\n"))
		if m == nil {
			continue
		}

		switch section {
		case sectionAudio:
			audio = append(audio, entry{id: m[1], label: m[1]})
		case sectionVideo:
			video = append(video, entry{id: m[1], label: m[1]})
		}
	}

	return audio, video
}

// pulseEntry matches one `ffmpeg -sources/-sinks pulse` line:
// `* alsa_input.pci… [Built-in Audio] (audio)` — the star marks the
// default and the trailing media types (absent on older builds) are
// ignored.
var pulseEntry = regexp.MustCompile(`^([* ]) (\S+) \[(.+?)\](?: \(.*\))?$`)

// parsePulse reads an ffmpeg pulse source/sink listing into entries.
func parsePulse(raw string) []entry {
	var out []entry

	for line := range strings.Lines(raw) {
		m := pulseEntry.FindStringSubmatch(strings.TrimRight(line, "\n"))
		if m == nil {
			continue
		}

		out = append(out, entry{id: m[2], label: m[3]})
	}

	return out
}
