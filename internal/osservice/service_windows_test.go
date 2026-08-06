//go:build windows

package osservice

import (
	"strings"
	"testing"
)

func TestRenderedTaskDefinition(t *testing.T) {
	t.Setenv("PRUKKA_STATE", `C:\prukka\state`)

	path, content, err := rendered(&Options{
		ExecPath:   `C:\Program Files\Prukka\prukka.exe`,
		ConfigPath: `C:\prukka\config.yaml`,
	})
	if err != nil {
		t.Fatalf("rendered returned error: %v", err)
	}

	if !strings.Contains(path, taskName) {
		t.Fatalf("path %q does not name the scheduled task", path)
	}

	for _, want := range []string{
		`<Command>C:\Program Files\Prukka\prukka.exe</Command>`,
		`<Arguments>daemon --config C:\prukka\config.yaml --log-file C:\prukka\state\daemon.log</Arguments>`,
		"<LogonTrigger>",
		"<LogonType>InteractiveToken</LogonType>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<RestartOnFailure>",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("task definition lacks %q:\n%s", want, content)
		}
	}
}

func TestArgumentsLineQuotesSpacedArguments(t *testing.T) {
	t.Parallel()

	got := argumentsLine([]string{"daemon", "--config", `C:\My Configs\prukka.yaml`})

	if want := `daemon --config "C:\My Configs\prukka.yaml"`; got != want {
		t.Fatalf("argumentsLine = %q, want %q", got, want)
	}
}

func TestXMLEscapeCoversTheSpecials(t *testing.T) {
	t.Parallel()

	if got, want := xmlEscape(`a&b<c>d"e'f`), "a&amp;b&lt;c&gt;d&quot;e&apos;f"; got != want {
		t.Fatalf("xmlEscape = %q, want %q", got, want)
	}
}
