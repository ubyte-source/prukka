package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/paths"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// failWriter fails every write.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("pipe closed") }

func TestRowJoinsColumnsWithTabs(t *testing.T) {
	t.Parallel()

	out := &bytes.Buffer{}
	if err := row(out, "a", "b", "c"); err != nil {
		t.Fatalf("row returned error: %v", err)
	}

	if out.String() != "a\tb\tc\n" {
		t.Fatalf("row wrote %q", out.String())
	}
}

func TestRowSurfacesWriteFailures(t *testing.T) {
	t.Parallel()

	if err := row(failWriter{}, "a"); err == nil {
		t.Fatal("a failed write went unreported")
	}
}

func TestCLIErrorTranslatesOnlyTheUnreachedDaemon(t *testing.T) {
	t.Parallel()

	if got := cliError(connectivity.Ready, nil); got != nil {
		t.Fatalf("cliError(nil) = %v, want a nil error", got)
	}

	plain := errors.New("read control token: permission denied")
	if got := cliError(connectivity.Idle, plain); !errors.Is(got, plain) {
		t.Fatalf("cliError rewrote a non-gRPC error: %v", got)
	}

	unreached := cliError(connectivity.TransientFailure, status.Error(codes.Unavailable,
		`connection error: desc = "transport: Error while dialing: dial control socket: `+
			`connect: no such file or directory"`))
	if unreached == nil {
		t.Fatal("cliError swallowed an unreachable daemon")
	}
	for _, want := range []string{paths.IPCPath(), "prukka up", "prukka service install --now"} {
		if !strings.Contains(unreached.Error(), want) {
			t.Fatalf("unreached daemon = %q, want it to name %q", unreached, want)
		}
	}
	if strings.Contains(unreached.Error(), "transport:") {
		t.Fatalf("unreached daemon = %q, want the gRPC plumbing dropped", unreached)
	}

	answered := cliError(connectivity.Ready,
		status.Error(codes.Unavailable, "engine catalog unavailable: boom"))
	if answered.Error() != "engine catalog unavailable: boom" {
		t.Fatalf("daemon answer = %q, want it kept as the daemon worded it", answered)
	}

	missing := cliError(connectivity.Ready, status.Error(codes.NotFound, `session not found: "demo"`))
	if missing.Error() != `session not found: "demo"` {
		t.Fatalf("not-found = %q, want the bare daemon message", missing)
	}
}

// daemonUnavailableMessage is a live daemon's own Unavailable.
const daemonUnavailableMessage = "engine catalog unavailable: boom"

// answeringDaemon serves the control API and refuses Stats as the daemon does.
type answeringDaemon struct {
	v1.UnimplementedControlServer
}

func (answeringDaemon) Stats(context.Context, *v1.StatsRequest) (*v1.StatsResponse, error) {
	return nil, status.Error(codes.Unavailable, daemonUnavailableMessage)
}

// control.Dial builds a LAZY connection, so before the call the state is
// always Idle — indistinguishable from a socket with nothing behind it. Only
// reading it after the call separates the two.
func TestControlCommandKeepsTheWordingOfADaemonThatAnsweredUnavailable(t *testing.T) {
	cfgPath := serveAnsweringDaemon(t)

	err := runStats(t, cfgPath)
	if err == nil {
		t.Fatal("stats succeeded against a daemon that refused it")
	}
	if err.Error() != daemonUnavailableMessage {
		t.Fatalf("answered Unavailable = %q, want the daemon's own wording %q", err, daemonUnavailableMessage)
	}
}

// serveAnsweringDaemon points the CLI at a private state directory and answers
// its control endpoint. The directory is a short MkdirTemp, not t.TempDir: the
// socket lives in it and t.TempDir embeds the full test name, enough to overrun
// Darwin's sockaddr_un limit.
func serveAnsweringDaemon(t *testing.T) string {
	t.Helper()

	state, err := os.MkdirTemp("", "prukka-client-")
	if err != nil {
		t.Fatalf("create short state dir: %v", err)
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(state); removeErr != nil {
			t.Errorf("remove short state dir: %v", removeErr)
		}
	})
	t.Setenv("PRUKKA_STATE", state)
	t.Setenv("XDG_RUNTIME_DIR", state)

	socket := paths.IPCPath()
	if !strings.HasPrefix(socket, state) {
		t.Skipf("control endpoint %s is machine-global here; a test must not bind one a live daemon may own", socket)
	}
	if err = os.MkdirAll(filepath.Dir(socket), 0o750); err != nil {
		t.Fatalf("create socket dir: %v", err)
	}

	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatalf("listen on the control socket: %v", err)
	}
	server := grpc.NewServer()
	v1.RegisterControlServer(server, answeringDaemon{})
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if serveErr := <-served; serveErr != nil {
			t.Errorf("control server: %v", serveErr)
		}
	})

	return seedControlClient(t, state)
}

// seedControlClient writes the token and config a client subcommand reads
// before it dials.
func seedControlClient(t *testing.T, state string) string {
	t.Helper()

	if err := os.WriteFile(filepath.Join(state, "control.token"),
		[]byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatalf("seed control token: %v", err)
	}
	cfgPath := filepath.Join(state, "config.yaml")
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	return cfgPath
}

// runStats drives one control subcommand end to end.
func runStats(t *testing.T, cfgPath string) error {
	t.Helper()

	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--config", cfgPath, "stats"})

	return root.Execute()
}

func TestControlCommandWithoutADaemonNamesTheStartCommands(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PRUKKA_STATE", state)

	err := runStats(t, seedControlClient(t, state))
	if err == nil {
		t.Fatal("stats succeeded with no daemon listening")
	}
	if strings.Contains(err.Error(), "transport:") || strings.Contains(err.Error(), "rpc error") {
		t.Fatalf("raw gRPC transport plumbing reached the user: %v", err)
	}
	for _, want := range []string{"prukka up", "prukka service install --now"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("stopped-daemon error %q does not name %q", err, want)
		}
	}
}
