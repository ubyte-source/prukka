// Package doctor runs the environment checks behind `prukka doctor` and
// the Doctor RPC.
package doctor

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/enginebundle"
	"github.com/ubyte-source/prukka/internal/hostos"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/paths"
	"github.com/ubyte-source/prukka/internal/speech"
	"github.com/ubyte-source/prukka/internal/strictjson"
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
	checkFFmpeg       = "ffmpeg"
	checkSpeechEngine = "speech-engine"
	checkStateDir     = "state-dir"
)

const (
	engineManifestName     = "prukka-engine-manifest.json"
	engineManifestSchema   = "prukka.engine.bundle"
	engineManifestKind     = "native"
	engineManifestVersion  = 2
	engineManifestMaxBytes = 4 << 10
)

type engineManifest struct {
	Schema  string `json:"schema"`
	Kind    string `json:"kind"`
	Version int    `json:"version"`
}

// Run executes every local probe against the loaded configuration.
func Run(cfg *config.Config) []Check {
	return []Check{
		speechEngineCheck(cfg),
		ffmpegCheck(),
		stateDirCheck(),
	}
}

func speechEngineCheck(cfg *config.Config) Check {
	dir, failed := effectiveEngineDir(cfg)
	if failed != nil {
		return *failed
	}

	declared, err := engineBundleDeclared(dir)
	if err != nil {
		return Check{Name: checkSpeechEngine, Status: StatusFail, Detail: err.Error()}
	}
	if !declared {
		return Check{
			Name:   checkSpeechEngine,
			Status: StatusWarn,
			Detail: fmt.Sprintf(
				"engine bundle resolved at %q; %s is absent, so native tools and model readiness are not declared",
				dir,
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
			dir,
		),
	}
}

// effectiveEngineDir resolves the bundle directory to inspect: the directory of
// the operator's configured binary, or the managed bundle root. A non-nil Check
// is the terminal probe result.
func effectiveEngineDir(cfg *config.Config) (string, *Check) {
	if configured := strings.TrimSpace(cfg.Providers.Local.Bin); configured != "" {
		bin, err := resolveEngineBinary(configured)
		if err != nil {
			return "", &Check{Name: checkSpeechEngine, Status: StatusFail, Detail: err.Error()}
		}

		return filepath.Dir(bin), nil
	}

	root, err := speech.Resolve(paths.StateDir())
	if errors.Is(err, speech.ErrNotInstalled) {
		return "", &Check{
			Name:   checkSpeechEngine,
			Status: StatusWarn,
			Detail: "no speech engine installed — run `prukka setup` to download the managed bundle",
		}
	}
	if err != nil {
		return "", &Check{Name: checkSpeechEngine, Status: StatusFail, Detail: err.Error()}
	}

	return root, nil
}

func engineBundleDeclared(dir string) (bool, error) {
	path := filepath.Join(dir, engineManifestName)
	info, err := os.Stat(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
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
	var manifest engineManifest
	if err := strictjson.Decode(data, &manifest); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
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
	if err != nil || !hostos.Executable(info) {
		return "", fmt.Errorf("binary %q is not an executable file", bin)
	}

	return bin, nil
}

// missingEngineExecutables validates the native tools the bundled helper runs
// for the configured stages; without it a lane fails only after admission.
func missingEngineExecutables(dir string, cfg *config.Config) []string {
	required := []string{enginebundle.WhisperServer}
	if len(cfg.Providers.Local.MT.Pairs) != 0 {
		required = append(required, enginebundle.MT)
	}
	if cfg.Providers.Voices == config.VoicesLocal {
		required = append(required, enginebundle.Piper())
	}

	missing := make([]string, 0, len(required))
	for _, name := range required {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if runtime.GOOS == hostos.Windows && err != nil {
			info, err = os.Stat(path + ".exe")
		}
		if err != nil || !hostos.Executable(info) {
			missing = append(missing, name)
		}
	}

	return missing
}

func missingEngineModels(dir string, cfg *config.Config) []string {
	models := []string{cfg.Providers.Local.STT.Model}
	callModel := cfg.Providers.Local.STT.CallModel
	if strings.TrimSpace(callModel) != "" && callModel != cfg.Providers.Local.STT.Model {
		models = append(models, callModel)
	}
	for _, pair := range cfg.Providers.Local.MT.Pairs {
		modelDir := enginebundle.MTModelDir(string(pair.From), string(pair.To))
		models = append(models,
			filepath.Join(modelDir, "config.json"),
			filepath.Join(modelDir, "model.bin"),
			filepath.Join(modelDir, "shared_vocabulary.json"),
			filepath.Join(modelDir, "source.spm"),
			filepath.Join(modelDir, "target.spm"),
		)
	}
	if cfg.Providers.Voices == config.VoicesLocal {
		for _, voice := range cfg.Providers.Local.TTS.Voices {
			models = append(models, voice.Voice, voice.Voice+".json")
		}
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

// ffmpegCheck looks for the ffmpeg binary the media plane runs on.
func ffmpegCheck() Check {
	path, err := ffmpeg.Resolve(paths.StateDir())
	if err != nil {
		return Check{
			Name:   checkFFmpeg,
			Status: StatusWarn,
			Detail: "not found — run `prukka setup` to install it automatically",
		}
	}

	return Check{Name: checkFFmpeg, Status: StatusOK, Detail: path}
}

// stateDirCheck verifies the state directory exists and is writable.
func stateDirCheck() Check {
	dir := paths.StateDir()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Check{Name: checkStateDir, Status: StatusFail, Detail: fmt.Sprintf("cannot create %s: %v", dir, err)}
	}

	probe, err := os.CreateTemp(dir, ".doctor-probe-*")
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
			Name:   checkStateDir,
			Status: StatusFail,
			Detail: fmt.Sprintf("cannot write in %s: %v", dir, probeErr),
		}
	}

	if removeErr != nil {
		return Check{
			Name:   checkStateDir,
			Status: StatusWarn,
			Detail: fmt.Sprintf("probe cleanup failed: %v", wrapProbeError("remove", removeErr)),
		}
	}

	return Check{Name: checkStateDir, Status: StatusOK, Detail: dir}
}

func wrapProbeError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s private probe: %w", operation, err)
}
