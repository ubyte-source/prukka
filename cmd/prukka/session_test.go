package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/control"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

func TestCreateSessionRequestLeavesDefaultsToDaemon(t *testing.T) {
	t.Parallel()

	req, err := createSessionRequest(&sessionAddFlags{
		in: "file:///tmp/x.wav", profile: "broadcast", source: "auto", dubLangs: "all", delay: 0,
	}, "demo")
	if err != nil {
		t.Fatalf("createSessionRequest returned error: %v", err)
	}
	wire := req.GetSession()
	if wire.DelaySeconds != nil || len(wire.GetLangs()) != 0 {
		t.Fatalf("omitted defaults were serialized: %+v", wire)
	}
	if _, present := wire.GetFlags()["subs"]; present {
		t.Fatalf("default subtitle mode was duplicated in request flags: %v", wire.GetFlags())
	}
}

func TestPrintSessionsShowsRuntimeStateAndFailure(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	err := printSessions(cmd, []*v1.Session{
		{
			Slug: "demo", Status: "failed", Error: "provider unavailable", Profile: "broadcast",
			Langs: []string{"it"}, SourceLabel: "rtmp://live.example",
			SourceUrl: "rtmp://user:se" + "cret@live.example/in/private",
		},
		{
			Slug: "legacy", Profile: "broadcast", Langs: []string{"de"},
			SourceUrl: "rtmp://user:se" + "cret@legacy.example/private?token=hidden",
		},
	})
	if err != nil {
		t.Fatalf("printSessions returned error: %v", err)
	}
	for _, want := range []string{
		"STATUS", "ERROR", "failed", "provider unavailable", "rtmp://live.example", "rtmp://legacy.example",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output does not contain %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "hidden") ||
		strings.Contains(out.String(), "/private") || strings.Contains(out.String(), "/in/private") {
		t.Fatalf("output preferred write-only source_url over source_label:\n%s", out.String())
	}
}

// TestSessionAddDelayDefaultIsAValidDelay: pflag prints the registered
// default on every `session add --help`, so a not-set sentinel publishes a
// value the daemon rejects — internal/core/session refuses a negative delay
// with InvalidArgument. Changed() already carries the not-set signal.
func TestSessionAddDelayDefaultIsAValidDelay(t *testing.T) {
	t.Parallel()

	delay := newSessionAddCmd(&rootFlags{}).Flags().Lookup("delay")
	if delay == nil {
		t.Fatal("--delay is not registered")
	}
	advertised, err := time.ParseDuration(delay.DefValue)
	if err != nil {
		t.Fatalf("--delay default %q is not a duration: %v", delay.DefValue, err)
	}
	if advertised < 0 {
		t.Fatalf("--delay advertises the default %q, which the daemon rejects", delay.DefValue)
	}
}

// TestPrintSessionsKeepsTheGridReadableWhenASessionFails: the failure detail
// is unbounded and the daemon's messages are chained, so an ERROR column in
// the middle of the grid pushes PROFILE/LANGS/SOURCE/DELAY past an 80-column
// terminal exactly when a session has failed and you need to read them.
func TestPrintSessionsKeepsTheGridReadableWhenASessionFails(t *testing.T) {
	t.Parallel()

	const failure = "start lane: open source rtmp://live.example/in/demo: " +
		"connect: connection refused (retrying in 4s, 3 attempts left)"

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	err := printSessions(cmd, []*v1.Session{
		{
			Slug: "demo", Status: "failed", Error: failure, Profile: "broadcast",
			Langs: []string{"it"}, SourceLabel: "rtmp://live.example", DelaySeconds: new(8.0),
		},
		{
			Slug: "second", Status: "running", Profile: "broadcast",
			Langs: []string{"de"}, SourceLabel: "rtmp://live.example", DelaySeconds: new(6.0),
		},
	})
	if err != nil {
		t.Fatalf("printSessions returned error: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if header := strings.Fields(lines[0]); header[len(header)-1] != "ERROR" {
		t.Fatalf("header = %v, want the unbounded ERROR column last", header)
	}

	failed := lines[1]
	delayAt, failureAt := strings.Index(failed, "8s"), strings.Index(failed, failure)
	if delayAt < 0 || failureAt < 0 {
		t.Fatalf("failed row lost its delay or its failure detail:\n%s", failed)
	}
	if delayAt > failureAt {
		t.Fatalf("the unbounded failure detail precedes the fixed columns:\n%s", failed)
	}
	if delayAt >= 80 {
		t.Fatalf("the delay column starts at column %d, past an 80-column terminal:\n%s", delayAt, failed)
	}
}

func TestCreateSessionRequestPreservesExplicitZero(t *testing.T) {
	t.Parallel()

	req, err := createSessionRequest(&sessionAddFlags{
		in: "file:///tmp/x.wav", profile: "broadcast", source: "auto", dubLangs: "all",
		delaySet: true,
	}, "zero")
	if err != nil {
		t.Fatalf("createSessionRequest returned error: %v", err)
	}
	wire := req.GetSession()
	if wire.DelaySeconds == nil || wire.GetDelaySeconds() != 0 {
		t.Fatalf("explicit zero presence lost: delay=%v", wire.DelaySeconds)
	}
}

func TestCreateSessionRequestPreservesCallPairTarget(t *testing.T) {
	t.Parallel()

	req, err := createSessionRequest(&sessionAddFlags{
		in: "device://audio/microphone", profile: "call", source: "it",
		dubLangs: "en", langs: "en", pair: "meeting-in",
	}, "meeting-out")
	if err != nil {
		t.Fatalf("createSessionRequest returned error: %v", err)
	}
	if got := req.GetSession().GetFlags()["pair"]; got != "meeting-in" {
		t.Fatalf("pair flag = %q, want meeting-in", got)
	}
}

func TestCreateSessionRequestRejectsSelfPair(t *testing.T) {
	t.Parallel()

	_, err := createSessionRequest(&sessionAddFlags{
		in: "device://audio/microphone", profile: "call", source: "it",
		dubLangs: "en", langs: "en", pair: "meeting",
	}, "meeting")
	if err == nil || !strings.Contains(err.Error(), "different session") {
		t.Fatalf("self-pair error = %v, want an actionable rejection", err)
	}
}

// TestSessionLangsKeepsInheritedFlagsAndHelp: the sigil grammar (-de as a
// removal) must not cost the inherited persistent flags or --help, which
// DisableFlagParsing silently swallowed.
func TestSessionLangsKeepsInheritedFlagsAndHelp(t *testing.T) {
	t.Parallel()

	const sessionCmd = "session"
	root := newRootCmd()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{sessionCmd, "langs", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--help returned error: %v", err)
	}
	if !strings.Contains(out.String(), "+tag") {
		t.Fatalf("help output %q lacks the sigil grammar", out.String())
	}

	root = newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--config", "/nonexistent/prukka.yaml", sessionCmd, "langs", "demo", "+fr", "-de"})
	err := root.Execute()
	if err == nil || strings.Contains(err.Error(), "must start with") {
		t.Fatalf("inherited --config was eaten by the sigil parser: %v", err)
	}
}

func TestSplitLangArgs(t *testing.T) {
	t.Parallel()

	add, remove, err := splitLangArgs([]string{"+fr", "-de", "+en"})
	if err != nil {
		t.Fatalf("splitLangArgs returned error: %v", err)
	}

	if len(add) != 2 || add[0] != "fr" || add[1] != "en" {
		t.Fatalf("add = %v, want [fr en]", add)
	}

	if len(remove) != 1 || remove[0] != "de" {
		t.Fatalf("remove = %v, want [de]", remove)
	}

	// A change without +/- is rejected.
	if _, _, err := splitLangArgs([]string{"fr"}); err == nil {
		t.Fatal("splitLangArgs accepted an unprefixed change")
	}

	// An invalid tag surfaces the registry error.
	if _, _, err := splitLangArgs([]string{"+nope"}); err == nil {
		t.Fatal("splitLangArgs accepted an invalid language")
	}
}

func TestApplyDubFlagValidatesTheTargetSubset(t *testing.T) {
	t.Parallel()

	flags := map[string]string{}
	if err := applyDubFlag(flags, "de,it", []string{"it", "de", "en"}); err != nil {
		t.Fatalf("applyDubFlag returned error: %v", err)
	}
	if flags["dub_langs"] != "de,it" {
		t.Fatalf("dub_langs = %q, want de,it", flags["dub_langs"])
	}

	err := applyDubFlag(map[string]string{}, "fr", []string{"it", "de"})
	if err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("out-of-session dub language error = %v", err)
	}

	off := map[string]string{}
	if err := applyDubFlag(off, "none", []string{"it"}); err != nil || off["dub"] != "off" {
		t.Fatalf("none = (%v, %q), want dub off", err, off["dub"])
	}

	serverDefaultTargets := map[string]string{}
	if err := applyDubFlag(serverDefaultTargets, "de", nil); err != nil ||
		serverDefaultTargets["dub_langs"] != "de" {
		t.Fatalf("subset over daemon-default targets = (%v, %q)", err, serverDefaultTargets["dub_langs"])
	}
}

// TestPushRetryableWaitsOnlyOnNotReady: the push wait loop retries exactly the
// daemon's TAGGED not-ready answer. A bare Unavailable is a transport failure
// (stopped daemon), and everything else surfaces immediately.
func TestPushRetryableWaitsOnlyOnNotReady(t *testing.T) {
	t.Parallel()

	tagged, err := status.New(codes.Unavailable, "media output not ready").
		WithDetails(&errdetails.ErrorInfo{Reason: control.PushNotReadyReason})
	if err != nil {
		t.Fatalf("build tagged status: %v", err)
	}
	if !pushRetryable(tagged.Err()) {
		t.Error("tagged not-ready must be retried")
	}
	// The CLI replaces the message a user reads, so it has to leave the
	// daemon's status reachable: otherwise the wait loop stops recognizing
	// the tagged answer and gives up on a lane that is still warming.
	if !pushRetryable(cliError(connectivity.Ready, tagged.Err())) {
		t.Error("the CLI translation hid the daemon's tagged not-ready answer")
	}
	for _, cause := range []error{
		nil,
		status.Error(codes.Unavailable, "connection refused"), // transport, not media
		status.Error(codes.FailedPrecondition, "session failed"),
		status.Error(codes.NotFound, "session not found"),
		errors.New("plain error"),
	} {
		if pushRetryable(cause) {
			t.Errorf("retryable(%v) = true, want false", cause)
		}
	}
}
