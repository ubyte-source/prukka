package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

func TestCreateSessionRequestLeavesDefaultsToDaemon(t *testing.T) {
	t.Parallel()

	req, err := createSessionRequest(&sessionAddFlags{
		in: "file:///tmp/x.wav", profile: "broadcast", source: "auto", dubLangs: "all", delay: -1,
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

func TestPushConfirmationDoesNotRepeatTheTarget(t *testing.T) {
	t.Parallel()

	message := pushConfirmation("demo", "en")
	if message != "pushing demo/en\n" || strings.Contains(message, "rtmp://") {
		t.Fatalf("push confirmation = %q, want only the session and language", message)
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
