package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSTTProtocol(t *testing.T) {
	t.Parallel()

	var out, stderr bytes.Buffer
	code := realMain(
		[]string{
			subcommandSTT, "--protocol-version", "2", "--model", "fixture",
			"--rate", "16000", "--threads", "3", "--language", "it",
			"--silence-hang", "160ms", "--max-window", "2s", "--min-speech", "120ms",
			"--partial-stride", "250ms", "--fast-decode",
		},
		strings.NewReader("pcmx"), &out, &stderr,
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, &stderr)
	}
	decoder := json.NewDecoder(&out)
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err := decoder.Decode(&ready); err != nil || !ready.Ready {
		t.Fatalf("STT readiness = %+v, %v", ready, err)
	}
	var response struct {
		Text       string `json:"text"`
		Language   string `json:"language"`
		Final      bool   `json:"final"`
		EndSamples int64  `json:"end_samples"`
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Text == "" || response.Language != "it" || !response.Final || response.EndSamples != 2 {
		t.Fatalf("STT response = %+v", response)
	}
}

func TestSTTProtocolRejectsInvalidThreads(t *testing.T) {
	t.Parallel()

	for _, threads := range []string{"0", "-1", "65"} {
		var out, stderr bytes.Buffer
		code := realMain(
			[]string{
				subcommandSTT, "--protocol-version", "2", "--model", "fixture",
				"--rate", "16000", "--threads", threads,
			},
			strings.NewReader("pcm"), &out, &stderr,
		)
		if code == 0 || !strings.Contains(stderr.String(), "--threads between 1 and 64") {
			t.Fatalf("threads %s: exit = %d, stderr = %q", threads, code, &stderr)
		}
	}
}

func TestSTTProtocolRequiresExactVersion(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--model", "fixture"},
		{"--protocol-version", "1", "--model", "fixture"},
		{"--protocol-version", "3", "--model", "fixture"},
	} {
		var out, stderr bytes.Buffer
		code := realMain(append([]string{subcommandSTT}, args...), strings.NewReader(""), &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "requires --protocol-version 2") {
			t.Fatalf("args %v: exit = %d, stderr = %q", args, code, &stderr)
		}
	}
}

func TestMTProtocol(t *testing.T) {
	t.Parallel()

	var out, stderr bytes.Buffer
	code := realMain(
		[]string{"mt", "--from", "it", "--to", "en"},
		strings.NewReader("{\"text\":\"ciao\"}\n"), &out, &stderr,
	)
	if code != 0 || !strings.Contains(out.String(), "[en] ciao") {
		t.Fatalf("exit = %d, out = %s, stderr = %s", code, &out, &stderr)
	}
}

func TestTTSProtocol(t *testing.T) {
	t.Parallel()

	var out, stderr bytes.Buffer
	code := realMain(
		[]string{"tts", "--model", "voice", "--rate", "16000"},
		strings.NewReader("{\"text\":\"hello\"}\n"), &out, &stderr,
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, &stderr)
	}

	scanner := bufio.NewScanner(&out)
	if !scanner.Scan() {
		t.Fatal("missing audio response")
	}
	var audio struct {
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &audio); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(audio.Audio)
	if err != nil || len(raw) != 8000 {
		t.Fatalf("audio bytes = %d, err = %v", len(raw), err)
	}
	if !scanner.Scan() || !strings.Contains(scanner.Text(), `"done":true`) {
		t.Fatalf("missing done response: %q", scanner.Text())
	}
}

func TestProtocolRejectsInvalidInvocation(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, {"unknown"}, {subcommandSTT}, {"mt"}, {"tts"}} {
		var out, stderr bytes.Buffer
		if code := realMain(args, strings.NewReader(""), &out, &stderr); code == 0 {
			t.Fatalf("realMain(%v) succeeded", args)
		}
	}
}
