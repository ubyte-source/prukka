// Package doctor runs the environment checks behind `prukka doctor` and
// the Doctor RPC.
package doctor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// Status grades one probe result.
type Status string

// The three probe outcomes.
const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// Check is one probe result.
type Check struct {
	Name   string
	Status Status
	Detail string
}

// Probe names.
const (
	checkSpeechEngine = "speech-engine"
	checkStateDir     = "state-dir"
)

const (
	engineManifestName     = "prukka-engine-manifest.json"
	engineManifestSchema   = "prukka.engine.bundle"
	engineManifestKind     = "native"
	engineManifestVersion  = 1
	engineManifestMaxBytes = 4 << 10
	windowsOS              = "windows"
)

type engineManifest struct {
	Schema  string `json:"schema"`
	Kind    string `json:"kind"`
	Version int    `json:"version"`
}

// Run executes every local probe against the loaded configuration.
func Run(cfg *config.Config) []Check {
	return []Check{
		configCheck(cfg),
		speechEngineCheck(cfg),
		ffmpegCheck(),
		stateDirCheck(),
	}
}

func speechEngineCheck(cfg *config.Config) Check {
	local := &cfg.Providers.Local
	if strings.TrimSpace(local.Bin) == "" {
		return Check{
			Name:   checkSpeechEngine,
			Status: StatusWarn,
			Detail: "providers.local.bin is unset — build the engine bundle and configure its executable",
		}
	}

	bin, err := resolveEngineBinary(local.Bin)
	if err != nil {
		return Check{Name: checkSpeechEngine, Status: StatusFail, Detail: err.Error()}
	}

	dir := filepath.Dir(bin)
	declared, err := engineBundleDeclared(dir)
	if err != nil {
		return Check{Name: checkSpeechEngine, Status: StatusFail, Detail: err.Error()}
	}
	if !declared {
		return Check{
			Name:   checkSpeechEngine,
			Status: StatusWarn,
			Detail: fmt.Sprintf(
				"binary %q resolved; %s is absent, so native tools and model readiness are not declared",
				bin,
				engineManifestName,
			),
		}
	}

	missingModels := missingEngineModels(dir, cfg)
	missingExecutables := missingEngineExecutables(dir, cfg)
	if len(missingModels) != 0 || len(missingExecutables) != 0 {
		details := make([]string, 0, 2)
		if len(missingModels) != 0 {
			details = append(details, "missing model files: "+strings.Join(missingModels, ", "))
		}
		if len(missingExecutables) != 0 {
			details = append(details, "missing or unusable runtime executables: "+strings.Join(missingExecutables, ", "))
		}

		return Check{
			Name:   checkSpeechEngine,
			Status: StatusFail,
			Detail: strings.Join(details, "; "),
		}
	}

	return Check{
		Name:   checkSpeechEngine,
		Status: StatusWarn,
		Detail: fmt.Sprintf(
			"static bundle layout is complete at %q; helper execution and model loading were not tested",
			bin,
		),
	}
}

func engineBundleDeclared(dir string) (bool, error) {
	path := filepath.Join(dir, engineManifestName)
	info, err := os.Stat(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("inspect engine bundle manifest %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return true, fmt.Errorf("engine bundle manifest %q is not a regular file", path)
	}
	if info.Size() > engineManifestMaxBytes {
		return true, fmt.Errorf(
			"engine bundle manifest %q exceeds %d bytes",
			path,
			engineManifestMaxBytes,
		)
	}

	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return true, fmt.Errorf("open engine bundle manifest %q: %w", path, err)
	}

	data, readErr := io.ReadAll(io.LimitReader(file, engineManifestMaxBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return true, fmt.Errorf("read engine bundle manifest %q: %w", path, err)
	}
	if len(data) > engineManifestMaxBytes {
		return true, fmt.Errorf(
			"engine bundle manifest %q exceeds %d bytes",
			path,
			engineManifestMaxBytes,
		)
	}
	if err := decodeEngineManifest(data); err != nil {
		return true, fmt.Errorf("invalid engine bundle manifest %q: %w", path, err)
	}

	return true, nil
}

func decodeEngineManifest(data []byte) error {
	if err := rejectDuplicateManifestFields(data); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest engineManifest
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}

		return fmt.Errorf("trailing JSON data: %w", err)
	}

	if manifest.Schema != engineManifestSchema {
		return fmt.Errorf("schema %q, want %q", manifest.Schema, engineManifestSchema)
	}
	if manifest.Version != engineManifestVersion {
		return fmt.Errorf("version %d, want %d", manifest.Version, engineManifestVersion)
	}
	if manifest.Kind != engineManifestKind {
		return fmt.Errorf("kind %q, want %q", manifest.Kind, engineManifestKind)
	}

	return nil
}

func rejectDuplicateManifestFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errors.New("manifest must be a JSON object")
	}

	seen := make(map[string]struct{}, 3)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return fmt.Errorf("decode JSON field: %w", err)
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("manifest field name must be a string")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate field %q", name)
		}
		seen[name] = struct{}{}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode field %q: %w", name, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode JSON object: %w", err)
	}

	return nil
}

func resolveEngineBinary(configured string) (string, error) {
	bin, err := exec.LookPath(configured)
	if err != nil {
		return "", fmt.Errorf("binary %q: %w", configured, err)
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return "", fmt.Errorf("resolve binary %q: %w", bin, err)
	}
	bin, err = filepath.EvalSymlinks(bin)
	if err != nil {
		return "", fmt.Errorf("resolve binary %q: %w", bin, err)
	}
	info, err := os.Stat(bin)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("binary %q is not a regular file", bin)
	}
	if runtime.GOOS != windowsOS && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("binary %q is not executable", bin)
	}

	return bin, nil
}

// missingEngineExecutables validates the native tools used by the bundled
// helper for the configured stages. Merely finding the Go orchestrator and its
// models is insufficient: each enabled lane would otherwise fail only after it
// has been admitted.
func missingEngineExecutables(dir string, cfg *config.Config) []string {
	required := []string{"whisper-server"}
	if len(cfg.Providers.Local.MT.Pairs) != 0 {
		required = append(required, "mt")
	}
	if cfg.Providers.Voices == config.VoicesLocal {
		required = append(required, filepath.Join("piper", "piper"))
	}

	missing := make([]string, 0, len(required))
	for _, name := range required {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if runtime.GOOS == windowsOS && err != nil {
			info, err = os.Stat(path + ".exe")
		}
		if err != nil || !info.Mode().IsRegular() ||
			(runtime.GOOS != windowsOS && info.Mode().Perm()&0o111 == 0) {
			missing = append(missing, name)
		}
	}

	return missing
}

func missingEngineModels(dir string, cfg *config.Config) []string {
	models := []string{cfg.Providers.Local.STT.Model}
	for _, pair := range cfg.Providers.Local.MT.Pairs {
		modelDir := filepath.Join("models", "mt-"+string(pair.From)+"-"+string(pair.To))
		models = append(models,
			filepath.Join(modelDir, "config.json"),
			filepath.Join(modelDir, "model.bin"),
			filepath.Join(modelDir, "shared_vocabulary.json"),
			filepath.Join(modelDir, "source.spm"),
			filepath.Join(modelDir, "target.spm"),
		)
	}
	if cfg.Providers.Voices == config.VoicesLocal {
		models = append(models, cfg.Providers.Local.TTS.Voice, cfg.Providers.Local.TTS.Voice+".json")
	}

	missing := make([]string, 0, len(models))
	for _, model := range models {
		path := model
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if info, err := os.Stat(filepath.Clean(path)); err != nil || !info.Mode().IsRegular() {
			missing = append(missing, model)
		}
	}

	return missing
}

func configCheck(cfg *config.Config) Check {
	deprecated := cfg.Deprecations()
	if len(deprecated) == 0 {
		return Check{Name: "config", Status: StatusOK, Detail: "schema is current"}
	}

	return Check{
		Name:   "config",
		Status: StatusWarn,
		Detail: "remove retired fields: " + strings.Join(deprecated, ", "),
	}
}

// ffmpegCheck looks for the ffmpeg binary the media plane runs on: PATH
// first, then the managed state-dir install.
func ffmpegCheck() Check {
	path, err := ffmpeg.Resolve(config.StateDir())
	if err != nil {
		return Check{
			Name:   "ffmpeg",
			Status: StatusWarn,
			Detail: "not found — run `prukka setup` to install it automatically",
		}
	}

	return Check{Name: "ffmpeg", Status: StatusOK, Detail: path}
}

// stateDirCheck verifies the state directory exists and is writable.
func stateDirCheck() Check {
	dir := config.StateDir()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Check{Name: checkStateDir, Status: StatusFail, Detail: fmt.Sprintf("cannot create %s: %v", dir, err)}
	}

	probe, err := createStateProbe(dir)
	if err != nil {
		return Check{Name: checkStateDir, Status: StatusFail, Detail: fmt.Sprintf("cannot write in %s: %v", dir, err)}
	}
	probePath := probe.Name()

	written, writeErr := probe.WriteString("ok")
	if writeErr == nil && written != len("ok") {
		writeErr = io.ErrShortWrite
	}
	var syncErr error
	if writeErr == nil {
		syncErr = probe.Sync()
	}
	closeErr := probe.Close()
	removeErr := os.Remove(filepath.Clean(probePath))
	probeErr := errors.Join(
		wrapProbeError("write", writeErr),
		wrapProbeError("sync", syncErr),
		wrapProbeError("close", closeErr),
	)
	if probeErr != nil {
		if removeErr != nil {
			probeErr = errors.Join(probeErr, wrapProbeError("remove", removeErr))
		}

		return Check{
			Name: checkStateDir, Status: StatusFail,
			Detail: fmt.Sprintf("cannot write in %s: %v", dir, probeErr),
		}
	}

	if removeErr != nil {
		return Check{
			Name: checkStateDir, Status: StatusWarn,
			Detail: fmt.Sprintf("probe cleanup failed: %v", wrapProbeError("remove", removeErr)),
		}
	}

	return Check{Name: checkStateDir, Status: StatusOK, Detail: dir}
}

func createStateProbe(dir string) (*os.File, error) {
	probe, err := os.CreateTemp(dir, ".doctor-probe-*")
	if err != nil {
		return nil, fmt.Errorf("create private probe: %w", err)
	}

	return probe, nil
}

func wrapProbeError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s private probe: %w", operation, err)
}
