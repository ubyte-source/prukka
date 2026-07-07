package openrouter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

func TestTranslateContract(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatRequest(t, r)
		w.Header().Set("Content-Type", "application/json")

		reply := `{"choices":[{"message":{"role":"assistant","content":" hello world "}}],` +
			`"usage":{"prompt_tokens":80,"completion_tokens":20,"cost":0.001}}`
		if _, err := w.Write([]byte(reply)); err != nil {
			t.Errorf("write reply: %v", err)
		}
	}))
	defer srv.Close()

	m := &fakeMeter{}

	out, err := newClient(srv, m).ForSession("demo").Translate(t.Context(),
		core.Transcript{Text: "ciao mondo", Lang: "it"},
		"en",
		core.MTOpts{
			Glossary: map[string]string{"prukka": "Prukka", "CPU": "CPU"},
			Context:  []string{"previous line one"},
			MinRatio: 0.85,
			MaxRatio: 1.15,
		})
	if err != nil {
		t.Fatalf("Translate returned error: %v", err)
	}

	if out != "hello world" {
		t.Fatalf("Translate = %q, want trimmed translation", out)
	}

	if len(m.calls) != 1 {
		t.Fatalf("meter calls = %+v, want exactly one", m.calls)
	}

	got := m.calls[0]
	if got.session != "demo" || got.kind != "mt" || got.units != 100 {
		t.Fatalf("meter call = %+v, want demo/mt/100 tokens", got)
	}

	// The euro figure is a runtime float product; compare with tolerance.
	if diff := got.eur - 0.001*0.9; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("meter eur = %v, want ≈0.0009", got.eur)
	}
}
