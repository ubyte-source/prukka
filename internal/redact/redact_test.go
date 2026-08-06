package redact_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/redact"
)

// Credential shapes assembled from parts, so no fixture is a
// hardcoded-credential literal.
const (
	secretUser      = "mirror-user"
	secretPassword  = "s3cr3tpass"
	secretSignature = "s1gnature"
	secretKey       = "stream-key"
)

func credentialed(host, path, query string) string {
	return "rtmp://" + secretUser + ":" + secretPassword + "@" + host + path + "?" + query + "#fragment"
}

func TestURLKeepsOnlySchemeAndHost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "userinfo, stream key, token and fragment",
			raw:  credentialed("live.example", "/in/"+secretKey, "token="+secretPassword),
			want: "rtmp://live.example",
		},
		{
			name: "presigned query keeps the port",
			raw:  "https://mirror.example:8443/model.bin?X-Amz-Signature=" + secretSignature,
			want: "https://mirror.example:8443",
		},
		{
			name: "srt passphrase",
			raw:  "srt://relay.example:9000?passphrase=" + secretPassword,
			want: "srt://relay.example:9000",
		},
		{name: "file path has no host", raw: "file:///Users/alice/private.wav", want: "file://[redacted]"},
		{name: "file share host is hidden", raw: "file://nas.local/share/take.wav", want: "file://[redacted]"},
		{name: "device identifier is hidden", raw: "device://audio/private-device-id", want: "device://[redacted]"},
		{name: "hostless scheme keeps only the scheme", raw: "unix:///var/run/prukka.sock", want: "unix://[redacted]"},
		{name: "schemeless text is never echoed", raw: "not a url with token=" + secretPassword, want: redact.Redacted},
		{name: "unparsable url is never echoed", raw: "http://" + secretUser + "@exam ple.com/x", want: redact.Redacted},
		{name: "scheme is lowercased", raw: "RTMP://Live.Example:1935/in/" + secretKey, want: "rtmp://Live.Example:1935"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := redact.URL(tc.raw)
			if got != tc.want {
				t.Fatalf("URL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			assertNoSecrets(t, got)
		})
	}
}

func TestTextScrubsEveryURLToken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "rtmp userinfo and stream key in stderr",
			in:   credentialedStderr(),
			want: "rtmp://live.example:1935 Connection refused",
		},
		{
			name: "srt query passphrase",
			in:   "srt://relay.example:9000?passphrase=" + secretPassword + "&mode=caller failed",
			want: "srt://relay.example:9000 failed",
		},
		{
			name: "file path has no host",
			in:   "file:///Users/x/private/take.wav: No such file or directory",
			want: "file://[redacted] No such file or directory",
		},
		{
			name: "quoted token ends at the quote",
			in:   "Opening 'rtmp://" + secretUser + ":" + secretPassword + "@live.example/in/" + secretKey + "' for writing",
			want: "Opening 'rtmp://live.example' for writing",
		},
		{
			name: "unparsable token is never echoed",
			in:   "read https://host.example/%zz" + secretKey + ": bad escape",
			want: "read " + redact.Redacted + " bad escape",
		},
		{
			name: "plain text untouched",
			in:   "Invalid data found when processing input",
			want: "Invalid data found when processing input",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := redact.Text(tc.in)
			if got != tc.want {
				t.Fatalf("Text(%q) = %q, want %q", tc.in, got, tc.want)
			}
			assertNoSecrets(t, got)
		})
	}
}

// credentialedStderr mirrors the ffmpeg breadcrumb that proved the leak.
func credentialedStderr() string {
	return "rtmp://" + secretUser + ":" + secretPassword + "@live.example:1935/app/" + secretKey + ": Connection refused"
}

func TestSplitKeepsOnlySchemeHostAndPathPresence(t *testing.T) {
	t.Parallel()

	parts, ok := redact.Split("rtmps://" + secretUser + ":" + secretPassword + "@example.test:443/live/material?q=v")
	if !ok || parts != (redact.Parts{Scheme: "rtmps", Host: "example.test:443", HasPath: true}) {
		t.Fatalf("Split kept the wrong skeleton: %+v ok=%v", parts, ok)
	}

	parts, ok = redact.Split("device://audio/private-device-id")
	if !ok || parts != (redact.Parts{Scheme: "device", Host: "audio", HasPath: true}) {
		t.Fatalf("device skeleton = %+v ok=%v", parts, ok)
	}

	parts, ok = redact.Split("https://host.example/")
	if !ok || parts.HasPath {
		t.Fatalf("root path counted as a path: %+v ok=%v", parts, ok)
	}

	if parts, ok = redact.Split("not a url"); ok {
		t.Fatalf("schemeless text parsed as a URL: %+v", parts)
	}
	if parts, ok = redact.Split("http://exam ple.com/x"); ok {
		t.Fatalf("unparsable URL parsed: %+v", parts)
	}
}

// ExampleURL shows that only the scheme, host and port survive.
func ExampleURL() {
	fmt.Println(redact.URL("rtmps://user:pass@live.example:1935/in/stream-key?token=abc"))
	fmt.Println(redact.URL("file://nas.local/share/take.wav"))
	fmt.Println(redact.URL("10.0.0.7/in/stream-key"))

	// Output:
	// rtmps://live.example:1935
	// file://[redacted]
	// [redacted-url]
}

// ExampleText scrubs every URL-shaped token while the prose stays readable.
func ExampleText() {
	fmt.Println(redact.Text("Opening 'rtmp://user:pass@live.example/in/stream-key' for writing"))
	// Output:
	// Opening 'rtmp://live.example' for writing
}

// ExampleSplit shows Parts recording that a path existed, without the path.
func ExampleSplit() {
	parts, ok := redact.Split("https://cdn.example:8443/models/it-en.bin?X-Amz-Signature=abc")
	fmt.Println(ok, parts.Scheme, parts.Host, parts.HasPath)
	// Output:
	// true https cdn.example:8443 true
}

// assertNoSecrets fails when anything beyond scheme and host survived.
func assertNoSecrets(t *testing.T, got string) {
	t.Helper()

	for _, secret := range []string{secretUser, secretPassword, secretSignature, secretKey, "private", "alice"} {
		if strings.Contains(got, secret) {
			t.Fatalf("reduction leaked %q: %q", secret, got)
		}
	}
}

func FuzzURL(f *testing.F) {
	f.Add(credentialed("live.example", "/in/"+secretKey, "token="+secretPassword))
	f.Add("https://mirror.example:8443/model.bin?X-Amz-Signature=" + secretSignature)
	f.Add("srt://relay.example:9000?passphrase=" + secretPassword)
	f.Add("file:///Users/alice/private.wav")
	f.Add("device://audio/private-device-id")
	f.Add("not a url with token=" + secretPassword)
	f.Add("http://" + secretUser + "@exam ple.com/x")
	f.Add("RTMP://Live.Example:1935/in/" + secretKey)
	f.Add("mailto:someone@example.test")
	f.Add("unix:///var/run/prukka.sock")
	f.Add("")

	f.Fuzz(func(t *testing.T, raw string) {
		got := redact.URL(raw)
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" {
			if got != redact.Redacted {
				t.Fatalf("URL(%q) = %q, want %q for an unparseable input", raw, got, redact.Redacted)
			}

			return
		}

		full := parsed.Scheme + "://" + parsed.Host
		anonymous := parsed.Scheme + "://[redacted]"
		if got != full && got != anonymous {
			t.Fatalf("URL(%q) = %q, want %q or %q", raw, got, full, anonymous)
		}
		assertReductionDropsParts(t, parsed, got, []string{redact.Redacted, full, anonymous})
	})
}

// assertReductionDropsParts counts a stripped part as leaked only when no
// permitted output shape carries it: a username spelling a substring of the
// host is the host showing, not the username leaking.
func assertReductionDropsParts(t *testing.T, parsed *url.URL, got string, permitted []string) {
	t.Helper()

	for _, part := range strippedParts(parsed) {
		if part == "" || carriedByPermitted(permitted, part) {
			continue
		}
		if strings.Contains(got, part) {
			t.Fatalf("reduction %q leaked %q", got, part)
		}
	}
}

func strippedParts(parsed *url.URL) []string {
	query := parsed.Query()
	parts := make([]string, 0, 5+2*len(query))
	parts = append(parts, parsed.EscapedPath(), parsed.Fragment, parsed.RawQuery)
	if user := parsed.User; user != nil {
		password, _ := user.Password()
		parts = append(parts, user.Username(), password)
	}
	for key, values := range query {
		parts = append(parts, key)
		parts = append(parts, values...)
	}

	return parts
}

func carriedByPermitted(permitted []string, part string) bool {
	for _, shape := range permitted {
		if strings.Contains(shape, part) {
			return true
		}
	}

	return false
}
