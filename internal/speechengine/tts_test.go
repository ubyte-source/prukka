package speechengine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseTTSOptions(t *testing.T) {
	t.Parallel()

	voice, rate, parseErr := parseTTSOptions([]string{"--model", "voice.onnx"})
	if parseErr != nil || voice != "voice.onnx" || rate != 16000 {
		t.Fatalf("parseTTSOptions = (%q, %d, %v)", voice, rate, parseErr)
	}
	for _, rateArg := range []string{"nope", "1", "192001"} {
		if _, _, invalidErr := parseTTSOptions(
			[]string{"--model", "voice", "--rate", rateArg},
		); invalidErr == nil {
			t.Fatalf("parseTTSOptions accepted invalid rate %q", rateArg)
		}
	}
}

func TestSynthesizeLineBlankRequestClosesResponse(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	writer := bufio.NewWriter(&output)
	encoder := json.NewEncoder(writer)

	if err := synthesizeLine(nil, 16000, []byte(`{"text":"  "}`), encoder, writer); err != nil {
		t.Fatalf("synthesizeLine: %v", err)
	}
	if got, want := output.String(), "{\"done\":true}\n"; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
}

func TestDecodeTextRequestIsStrict(t *testing.T) {
	t.Parallel()

	got, err := decodeTextRequest([]byte(`{"text":"hello"}`))
	if err != nil || got != "hello" {
		t.Fatalf("decodeTextRequest = %q, %v", got, err)
	}
	for _, raw := range []string{
		`{}`, `null`, `{"text":null}`, `{"text":1}`, `{"text":"ok","extra":true}`,
	} {
		if _, err := decodeTextRequest([]byte(raw)); err == nil {
			t.Errorf("decodeTextRequest(%s) succeeded", raw)
		}
	}
}

func TestSynthesizeLineUsesWarmSynthesizer(t *testing.T) {
	t.Parallel()

	synth := &fakeTTSSynth{pcm: []int16{0, 1000}, rate: 2}
	var output bytes.Buffer
	writer := bufio.NewWriter(&output)
	if err := synthesizeLine(
		synth, 4, []byte(`{"text":"hello"}`), json.NewEncoder(writer), writer,
	); err != nil {
		t.Fatalf("synthesizeLine: %v", err)
	}
	if synth.text != "hello" {
		t.Fatalf("synthesized text = %q", synth.text)
	}

	var messages []map[string]any
	decoder := json.NewDecoder(&output)
	for {
		var message map[string]any
		err := decoder.Decode(&message)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	done, doneOK := messages[1]["done"].(bool)
	if messages[0]["audio"] == nil || !doneOK || !done {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestPiperRequestUsesJSONLAndValidatesAcknowledgement(t *testing.T) {
	t.Parallel()

	var input bytes.Buffer
	acks := make(chan piperAck, 2)
	acks <- piperAck{path: "/tmp/one.wav"}
	acks <- piperAck{path: "/tmp/two.wav"}
	close(acks)
	proc := &piperProc{
		stdioProc: &stdioProc{},
		input:     json.NewEncoder(&input),
		acks:      acks,
	}
	if err := proc.request("one", "/tmp/one.wav"); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if err := proc.request("two", "/tmp/two.wav"); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if got, want := input.String(),
		"{\"text\":\"one\",\"output_file\":\"/tmp/one.wav\"}\n"+
			"{\"text\":\"two\",\"output_file\":\"/tmp/two.wav\"}\n"; got != want {
		t.Fatalf("requests = %q, want %q", got, want)
	}

	wrong := make(chan piperAck, 1)
	wrong <- piperAck{path: "/tmp/wrong.wav"}
	close(wrong)
	mismatch := &piperProc{stdioProc: &stdioProc{}, input: json.NewEncoder(io.Discard), acks: wrong}
	if err := mismatch.request("one", "/tmp/one.wav"); err == nil {
		t.Fatal("mismatched acknowledgement succeeded")
	}
}

func TestPiperRequestTimeoutUnblocksAStalledWrite(t *testing.T) {
	t.Parallel()

	writer := newBlockingWriteCloser()
	acks := make(chan piperAck)
	proc := &piperProc{stdioProc: &stdioProc{stdin: writer}, input: json.NewEncoder(writer), acks: acks}

	start := time.Now()
	err := proc.requestWithin("hello", "/tmp/out.wav", 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "write timed out") {
		t.Fatalf("request error = %v, want write timeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stalled write took %v to abort", elapsed)
	}
	select {
	case <-writer.closed:
	case <-time.After(time.Second):
		t.Fatal("timeout did not close the blocked pipe")
	}
}

func TestPiperProcessStaysWarmAcrossRequests(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "audio")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	proc := startFakePiperProcess(t, root, workDir)
	closed := false
	t.Cleanup(closePiperOnCleanup(t, proc, &closed))

	for _, text := range []string{"first", "second"} {
		assertPiperSynthesis(t, proc, text)
	}
	if closeErr := proc.close(); closeErr != nil {
		t.Fatalf("close Piper: %v", closeErr)
	}
	closed = true

	loadData := readTestFile(t, root, "loads")
	if got := len(strings.Fields(string(loadData))); got != 1 {
		t.Fatalf("Piper load count = %d, want 1", got)
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary outputs survived: %v", entries)
	}
}

func startFakePiperProcess(t *testing.T, root, workDir string) *piperProc {
	t.Helper()

	symlinkTestExecutable(t, root)
	cmd := fakePiperCommand(root)
	cmd.Env = append(os.Environ(), "PRUKKA_FAKE_PIPER=1", "PRUKKA_FAKE_PIPER_ROOT="+root)
	cmd.Stderr = os.Stderr
	proc, err := startPiperCommand(cmd, workDir)
	if err != nil {
		t.Fatalf("startPiperCommand: %v", err)
	}

	return proc
}

// symlinkTestExecutable exposes the running test binary under a fixed name so
// the fake helper is spawned with a constant command argument. A symlink (not a
// hardlink) is used deliberately: it is an independent filesystem object, so
// t.TempDir cleanup can unlink it on Windows even though the target test binary
// is still running and mapped. A hardlink shares the running image's file and
// is refused with "Access is denied".
func symlinkTestExecutable(t *testing.T, root string) {
	t.Helper()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	name := "fake-piper"
	if runtime.GOOS == goosWindows {
		name += ".exe"
	}
	if err := os.Symlink(executable, filepath.Join(root, name)); err != nil {
		t.Fatalf("symlink test executable: %v", err)
	}
}

func fakePiperCommand(root string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == goosWindows {
		cmd = exec.CommandContext(context.Background(), `.\fake-piper.exe`, "-test.run=^TestPiperHelperProcess$")
	} else {
		cmd = exec.CommandContext(context.Background(), "./fake-piper", "-test.run=^TestPiperHelperProcess$")
	}
	cmd.Dir = root

	return cmd
}

func closePiperOnCleanup(t *testing.T, proc *piperProc, closed *bool) func() {
	t.Helper()

	return func() {
		if !*closed {
			if err := proc.close(); err != nil {
				t.Errorf("close Piper during cleanup: %v", err)
			}
		}
	}
}

func assertPiperSynthesis(t *testing.T, proc *piperProc, text string) {
	t.Helper()

	pcm, rate, err := proc.synthesize(text)
	if err != nil || rate != 22050 || !reflect.DeepEqual(pcm, []int16{1, -1}) {
		t.Fatalf("synthesize(%q) = %v, %d, %v", text, pcm, rate, err)
	}
}

func readTestFile(t *testing.T, dir, name string) []byte {
	t.Helper()

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := root.ReadFile(name)
	if err := errors.Join(readErr, root.Close()); err != nil {
		t.Fatal(err)
	}

	return data
}

func TestPiperHelperProcess(_ *testing.T) {
	if os.Getenv("PRUKKA_FAKE_PIPER") != "1" {
		return
	}
	if err := runFakePiper(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runFakePiper() (err error) {
	root, err := os.OpenRoot(os.Getenv("PRUKKA_FAKE_PIPER_ROOT"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	if err := recordFakePiperLoad(root); err != nil {
		return err
	}

	return serveFakePiper(root)
}

func recordFakePiperLoad(root *os.Root) error {
	loads, err := root.OpenFile("loads", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintln(loads, os.Getpid())

	return errors.Join(writeErr, loads.Close())
}

func serveFakePiper(root *os.Root) error {
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		if err := serveFakePiperRequest(root, input.Bytes()); err != nil {
			return err
		}
	}

	return input.Err()
}

func serveFakePiperRequest(root *os.Root, line []byte) error {
	var request struct {
		Text       string `json:"text"`
		OutputFile string `json:"output_file"`
	}
	if err := json.Unmarshal(line, &request); err != nil {
		return err
	}
	if request.Text == "" || request.OutputFile == "" {
		return errors.New("fake Piper received an incomplete request")
	}
	outputName, err := rootedTestOutputName(root, request.OutputFile)
	if err != nil {
		return err
	}
	if writeErr := root.WriteFile(outputName, encodeWAV([]int16{1, -1}, 22050), 0o600); writeErr != nil {
		return writeErr
	}
	_, err = fmt.Fprintln(os.Stdout, request.OutputFile)

	return err
}

func rootedTestOutputName(root *os.Root, outputPath string) (string, error) {
	name, err := filepath.Rel(root.Name(), outputPath)
	if err != nil {
		return "", err
	}
	if !filepath.IsLocal(name) || name == "." {
		return "", fmt.Errorf("fake Piper output escapes test root: %q", outputPath)
	}

	return name, nil
}

func TestSynthesizeLineChunksLargeAudioBelowProtocolLimit(t *testing.T) {
	t.Parallel()

	pcm := make([]int16, 600_000)
	for i := range pcm {
		pcm[i] = int16(i)
	}
	synth := &fakeTTSSynth{pcm: pcm, rate: 16000}
	var output bytes.Buffer
	writer := bufio.NewWriter(&output)
	if err := synthesizeLine(
		synth, 16000, []byte(`{"text":"long clause"}`), json.NewEncoder(writer), writer,
	); err != nil {
		t.Fatalf("synthesizeLine: %v", err)
	}

	var reconstructed []byte
	scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
	for scanner.Scan() {
		if len(scanner.Bytes()) >= 1<<20 {
			t.Fatalf("protocol line is %d bytes", len(scanner.Bytes()))
		}
		var message struct {
			Audio string `json:"audio"`
			Done  bool   `json:"done"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		if message.Audio != "" {
			chunk, err := base64.StdEncoding.DecodeString(message.Audio)
			if err != nil {
				t.Fatal(err)
			}
			reconstructed = append(reconstructed, chunk...)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if want := int16ToBytes(pcm); !bytes.Equal(reconstructed, want) {
		t.Fatalf("reconstructed %d bytes, want %d", len(reconstructed), len(want))
	}
}
