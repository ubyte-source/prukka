package native

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/besteffort"
	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/core/realtime"

	"github.com/ubyte-source/prukka/internal/nativewire"
	"github.com/ubyte-source/prukka/internal/procio"

	"github.com/ubyte-source/prukka/internal/testkit"
)

// Stub identifiers that turn a re-executed test binary into a fake engine.
const (
	fakeModel      = "fake-stt"
	fakeBadSTT     = "__fake_bad_stt__"
	fakeEOFSTT     = "__fake_eof_stt__"
	fakeRejectSTT  = "__fake_reject_stt_input__"
	fakeNoProtoSTT = "__fake_no_proto_stt__"
	fakeVoice      = "fake-voice"
	fakeLang       = "zz"
	fakeRejectMT   = "__fake_reject_mt_input__"
	fakeRejectTTS  = "__fake_reject_tts_input__"
	fakeTreeModel  = "__fake_tree_stt__"
	fakeExitTree   = "__fake_exit_tree_stt__"

	fakeHang       = "__fake_hang__"
	fakeCrash      = "__fake_crash__"
	fakeExitAfter  = "__fake_exit_after_reply__"
	fakeBadBase64  = "__fake_bad_base64__"
	fakeOddPCM     = "__fake_odd_pcm__"
	fakeBadJSON    = "__fake_bad_json__"
	fakeOversized  = "__fake_oversized_line__"
	fakeTreeParent = "__fake_tree_parent__"
	fakeTreeChild  = "__fake_tree_child__"
	fakeHangReady  = "fake helper handling hanging request"
	fakeSTTReject  = "fake stt closed its input"
	fakeMTReject   = "fake mt closed its input"
	fakeTTSReject  = "fake tts closed its input"
)

// fakeSamples is the PCM every fake synthesis returns per clause.
var fakeSamples = []int16{1, 2, 3, 4}

func discardTestLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

type failingReleaseTree struct {
	procio.Tree

	err error
}

func (t *failingReleaseTree) Release() error {
	return errors.Join(t.Tree.Release(), t.err)
}

type inertProcessTree struct{}

func (inertProcessTree) Kill() error    { return nil }
func (inertProcessTree) Release() error { return nil }

// runFakeEngine replays the stub subcommand a re-executed test binary was asked
// for, reporting whether it impersonated the engine.
func runFakeEngine(args []string) bool {
	runFakeProcessTree(args)
	if len(args) < 2 {
		return false
	}

	switch args[1] {
	case nativewire.SubSTT:
		return runFakeSTTCommand(flagValue(args, flagModel))
	case nativewire.SubTTS:
		return runFakeTTSCommand(flagValue(args, flagModel))
	case nativewire.SubMT:
		return runFakeMTCommand(flagValue(args, flagFrom))
	}

	return false
}

func runFakeSTTCommand(model string) bool {
	if !isFakeSTTModel(model) {
		return false
	}
	if model == fakeNoProtoSTT {
		if _, err := fmt.Fprintln(os.Stderr, "flag provided but not defined: -protocol-version"); err != nil {
			os.Exit(1)
		}

		return true
	}
	if _, err := fmt.Fprintln(os.Stdout, `{"ready":true}`); err != nil {
		os.Exit(1)
	}
	runFakeSTTModel(model)

	return true
}

func isFakeSTTModel(model string) bool {
	switch model {
	case fakeModel, fakeTreeModel, fakeExitTree, fakeBadSTT, fakeEOFSTT, fakeRejectSTT, fakeNoProtoSTT:
		return true
	default:
		return false
	}
}

func runFakeSTTModel(model string) {
	switch model {
	case fakeModel:
		runFakeSTT()
	case fakeTreeModel:
		runFakeTreeSTT()
	case fakeExitTree:
		runFakeExitTreeSTT()
	case fakeBadSTT:
		runFakeBadSTT()
	case fakeEOFSTT:
		runFakeEOFSTT()
	case fakeRejectSTT:
		runFakeInputRejector(fakeSTTReject)
	}
}

func runFakeTTSCommand(model string) bool {
	if model == fakeRejectTTS {
		runFakeInputRejector(fakeTTSReject)

		return true
	}
	if !strings.HasPrefix(model, fakeVoice) {
		return false
	}
	runFakeTTS()

	return true
}

func runFakeMTCommand(from string) bool {
	if from == fakeRejectMT {
		runFakeInputRejector(fakeMTReject)

		return true
	}
	if from != fakeLang {
		return false
	}
	runFakeMT()

	return true
}

// runFakeInputRejector closes the read side before publishing its marker, so the
// parent deterministically observes a failed pipe write, then stays alive.
func runFakeInputRejector(marker string) {
	if err := os.Stdin.Close(); err != nil {
		os.Exit(1)
	}
	if _, err := fmt.Fprintln(os.Stderr, marker); err != nil {
		os.Exit(1)
	}

	for {
		time.Sleep(time.Hour)
	}
}

// runFakeBadSTT emits a protocol violation and then waits for input.
func runFakeBadSTT() {
	w := bufio.NewWriter(os.Stdout)
	mustWriteFake(w, []byte("{"))

	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(1)
	}
}

func runFakeEOFSTT() {
	if err := os.Stdout.Close(); err != nil {
		os.Exit(1)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(1)
	}
}

func runFakeProcessTree(args []string) {
	if len(args) < 2 {
		return
	}

	switch args[1] {
	case fakeTreeChild:
		for {
			time.Sleep(time.Hour)
		}
	case fakeTreeParent:
		runFakeTreeParent()
	}
}

func runFakeTreeParent() {
	// Re-executes the fixed test binary; no user-controlled executable or args.
	child := exec.CommandContext(context.Background(), os.Args[0], fakeTreeChild)
	if err := child.Start(); err != nil {
		os.Exit(1)
	}
	if _, err := fmt.Fprintln(os.Stdout, child.Process.Pid); err != nil {
		os.Exit(1)
	}

	for {
		time.Sleep(time.Hour)
	}
}

// runFakeTreeSTT starts a descendant, reports its PID as a transcript, and
// deliberately survives stdin closure so the adapter's timed tree kill runs.
func runFakeTreeSTT() {
	childPID := startFakeTreeChild()
	writeFakeTreeTranscript(childPID)

	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(1)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func runFakeExitTreeSTT() {
	writeFakeTreeTranscript(startFakeTreeChild())
}

func startFakeTreeChild() int {
	// Re-executes the fixed test binary; no user-controlled executable or args.
	child := exec.CommandContext(context.Background(), os.Args[0], fakeTreeChild)
	if err := child.Start(); err != nil {
		os.Exit(1)
	}

	return child.Process.Pid
}

func writeFakeTreeTranscript(pid int) {
	line, err := json.Marshal(struct {
		Text       string `json:"text"`
		Final      bool   `json:"final"`
		EndSamples int64  `json:"end_samples"`
	}{Text: strconv.Itoa(pid), Final: true})
	if err != nil {
		os.Exit(1)
	}
	mustWriteFake(bufio.NewWriter(os.Stdout), line)
}

// flagValue returns the argument following flag, or "" when absent.
func flagValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}

	return ""
}

// fakeSTTChunkSamples is the mono sample count the fake consumes per reply; a
// fixed frame keeps the cumulative offsets independent of how a pipe read splits.
const fakeSTTChunkSamples = 320

// fakeSTTChunkSpan is the source time one such chunk covers at the reference
// rate.
const fakeSTTChunkSpan = fakeSTTChunkSamples * time.Second / 16000

// pushFakeSTTChunks feeds count chunks of contiguous source audio, one per
// scripted line the fake helper answers.
func pushFakeSTTChunks(t *testing.T, transcription realtime.Transcription, count int) {
	t.Helper()

	for i := range count {
		frame := core.PCM{
			Data: make([]int16, fakeSTTChunkSamples),
			Rate: 16000,
			Ch:   1,
			PTS:  time.Duration(i) * fakeSTTChunkSpan,
		}
		if err := transcription.Push(t.Context(), frame); err != nil {
			t.Fatalf("push chunk %d: %v", i, err)
		}
	}
}

// runFakeSTT answers each consumed PCM chunk with one scripted transcript line
// stamped with the cumulative mono samples it has ACTUALLY read. Emitting only
// after a read drives the adapter's frame walk instead of the end_samples==0
// shortcut.
func runFakeSTT() {
	lines := []string{
		`{"partial":"buon","inference_ms":12.5,"end_samples":%d}`,
		`{"partial":"buongiorno","end_samples":%d}`,
		`{"text":"Buongiorno a tutti.","final":true,"inference_ms":20,"end_samples":%d}`,
		`{"partial":"","end_samples":%d}`,
		`{"text":"","final":true,"end_samples":%d}`,
		`{"partial":"il ponte","end_samples":%d}`,
		`{"text":"Il ponte è aperto.","final":true,"end_samples":%d}`,
	}

	w := bufio.NewWriter(os.Stdout)
	chunk := make([]byte, fakeSTTChunkSamples*2)
	consumed := 0
	for _, line := range lines {
		if _, err := io.ReadFull(os.Stdin, chunk); err != nil {
			return // the producer stopped sending audio; there is nothing to say
		}
		consumed += fakeSTTChunkSamples
		mustWriteFake(w, fmt.Appendf(nil, line, consumed))
	}

	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(1)
	}
}

// runFakeTTS answers each clause request with one PCM chunk and a turn boundary,
// until the adapter's process is killed.
func runFakeTTS() {
	raw := pipeline.EncodeS16LE(fakeSamples)

	chunk, err := json.Marshal(nativewire.AudioReply{Audio: base64.StdEncoding.EncodeToString(raw)})
	if err != nil {
		os.Exit(1)
	}
	done, err := json.Marshal(nativewire.AudioReply{Done: true})
	if err != nil {
		os.Exit(1)
	}

	in := bufio.NewScanner(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	for in.Scan() {
		var req nativewire.TextLine
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			os.Exit(1)
		}

		switch fakeTTSDirective(w, req.Text) {
		case fakeStop:
			return
		case fakeHandled:
			continue
		case fakeNormal:
		}

		mustWriteFake(w, chunk, done)
		if req.Text == fakeExitAfter {
			return
		}
	}
}

type fakeAction uint8

const (
	fakeNormal fakeAction = iota
	fakeHandled
	fakeStop
)

func fakeTTSDirective(w *bufio.Writer, text string) fakeAction {
	switch text {
	case fakeHang:
		return fakeHangDirective()
	case fakeCrash:
		besteffort.Linef(os.Stderr, "fake tts crash")
		os.Exit(23)
	case fakeBadJSON:
		mustWriteFake(w, []byte("{"))
	case fakeBadBase64:
		mustWriteFake(w, mustMarshalFake(nativewire.AudioReply{Audio: "%%%"}))
	case fakeOddPCM:
		mustWriteFake(w, mustMarshalFake(nativewire.AudioReply{
			Audio: base64.StdEncoding.EncodeToString([]byte{1}),
		}))
	case fakeOversized:
		return fakeOversizedTTSDirective(w)
	default:
		return fakeNormal
	}

	return fakeHandled
}

func fakeHangDirective() fakeAction {
	besteffort.Linef(os.Stderr, fakeHangReady)
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(1)
	}

	return fakeStop
}

func fakeOversizedTTSDirective(w *bufio.Writer) fakeAction {
	if _, err := w.WriteString(strings.Repeat("x", scanLineMax+1) + "\n"); err != nil {
		os.Exit(1)
	}
	if err := w.Flush(); err != nil {
		os.Exit(1)
	}

	return fakeHangDirective()
}

// runFakeMT answers each translation request with a marked echo, until the
// adapter's process is killed.
func runFakeMT() {
	in := bufio.NewScanner(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	for in.Scan() {
		var req nativewire.TextLine
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			os.Exit(1)
		}

		switch fakeMTDirective(w, req.Text) {
		case fakeStop:
			return
		case fakeHandled:
			continue
		case fakeNormal:
		}

		resp, err := json.Marshal(nativewire.TextLine{Text: "mt:" + req.Text})
		if err != nil {
			os.Exit(1)
		}

		if writeLine(w, resp) != nil || w.Flush() != nil {
			os.Exit(1)
		}
		if req.Text == fakeExitAfter {
			return
		}
	}
}

func fakeMTDirective(w *bufio.Writer, text string) fakeAction {
	switch text {
	case fakeHang:
		besteffort.Linef(os.Stderr, fakeHangReady)
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			os.Exit(1)
		}

		return fakeStop
	case fakeCrash:
		besteffort.Linef(os.Stderr, "fake mt crash")
		os.Exit(23)
	case fakeBadJSON:
		mustWriteFake(w, []byte("{"))

		return fakeHandled
	case fakeOversized:
		if _, err := w.WriteString(strings.Repeat("x", scanLineMax+1) + "\n"); err != nil {
			os.Exit(1)
		}
		if err := w.Flush(); err != nil {
			os.Exit(1)
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			os.Exit(1)
		}

		return fakeStop
	default:
		return fakeNormal
	}

	return fakeStop
}

func mustMarshalFake(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		os.Exit(1)
	}

	return b
}

func mustWriteFake(w *bufio.Writer, lines ...[]byte) {
	for _, line := range lines {
		if err := writeLine(w, line); err != nil {
			os.Exit(1)
		}
	}
	if err := w.Flush(); err != nil {
		os.Exit(1)
	}
}

func waitStderrMarker(t *testing.T, stderr *procio.TailBuffer, marker string) {
	t.Helper()

	testkit.Eventually(t, 3*time.Second,
		func() bool { return strings.Contains(stderr.String(), marker) },
		"helper never published stderr marker "+marker)
}

func writeLine(w *bufio.Writer, b []byte) error {
	if _, err := w.Write(b); err != nil {
		return err
	}

	return w.WriteByte('\n')
}

func collect(events <-chan realtime.Transcript) []realtime.Transcript {
	var got []realtime.Transcript
	for event := range events {
		got = append(got, event)
	}

	return got
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for helper process to exit")
	}
}
