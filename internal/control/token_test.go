package control

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestLoadOrCreateTokenMintsAndPersists(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state", "control.token")

	first, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("LoadOrCreateToken returned error: %v", err)
	}

	if len(first) != tokenBytes*2 { // hex-encoded
		t.Fatalf("token length = %d, want %d hex chars", len(first), tokenBytes*2)
	}

	// The file is owner-only; Windows expresses that in ACLs, not the mode.
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat token: %v", statErr)
	}

	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %v, want 0600", info.Mode().Perm())
	}

	second, reloadErr := LoadOrCreateToken(path)
	if reloadErr != nil {
		t.Fatalf("second LoadOrCreateToken returned error: %v", reloadErr)
	}

	if second != first {
		t.Fatal("token changed on reload, want it persisted")
	}
}

func TestReadTokenRequiresAMintedToken(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "control.token")

	if _, err := ReadToken(path); err == nil {
		t.Fatal("ReadToken succeeded with no token file, want error")
	}

	if _, err := LoadOrCreateToken(path); err != nil {
		t.Fatalf("mint returned error: %v", err)
	}

	if _, err := ReadToken(path); err != nil {
		t.Fatalf("ReadToken after mint returned error: %v", err)
	}
}

func TestLoadOrCreateTokenIsAtomicAcrossConcurrentCallers(t *testing.T) {
	t.Parallel()

	const callers = 32
	path := filepath.Join(t.TempDir(), "state", "control.token")
	start := make(chan struct{})
	tokens := make(chan string, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup

	group.Add(callers)
	for range callers {
		go func() {
			defer group.Done()
			<-start

			token, err := LoadOrCreateToken(path)
			if err != nil {
				errs <- err

				return
			}
			tokens <- token
		}()
	}

	close(start)
	group.Wait()
	close(errs)
	close(tokens)

	for err := range errs {
		t.Errorf("LoadOrCreateToken: %v", err)
	}

	want := ""
	for token := range tokens {
		if want == "" {
			want = token
		}
		if token != want {
			t.Errorf("token = %q, want shared token %q", token, want)
		}
	}
	if want == "" {
		t.Fatal("no caller returned a token")
	}
}

func TestReadTokenRejectsMalformedContent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "control.token")
	if err := os.WriteFile(path, []byte("not-a-token\n"), 0o600); err != nil {
		t.Fatalf("write malformed token: %v", err)
	}
	if _, err := ReadToken(path); err == nil {
		t.Fatal("ReadToken accepted malformed content")
	}
}

// TestLoadOrCreateTokenReclaimsAnInterruptedMint: mintToken publishes the file
// name (O_CREAT|O_EXCL) before it writes the secret, so a SIGKILL, an OOM kill
// or a power loss in that window leaves a zero-byte control.token behind. The
// daemon then reads it on every start, and a service manager with
// restart-on-failure crash-loops forever. An empty file never carried a token
// any client could hold, so reclaiming it rotates no credential — it is the one
// corruption the daemon may fix by itself.
func TestLoadOrCreateTokenReclaimsAnInterruptedMint(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "control.token")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("stage the interrupted mint: %v", err)
	}

	token, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("LoadOrCreateToken over an interrupted mint: %v", err)
	}
	if len(token) != tokenBytes*2 {
		t.Fatalf("token length = %d, want %d hex chars", len(token), tokenBytes*2)
	}

	persisted, readErr := ReadToken(path)
	if readErr != nil || persisted != token {
		t.Fatalf("persisted token = (%q, %v), want the reclaimed %q", persisted, readErr, token)
	}
}

// TestLoadOrCreateTokenNamesACorruptTokenAndItsRemedy: content the daemon did
// not write may be a token clients already adopted, damaged after publication,
// so it is never re-minted behind their backs. The operator gets the one
// diagnosis they can act on: which file, what is wrong with it, what to do.
func TestLoadOrCreateTokenNamesACorruptTokenAndItsRemedy(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "control.token")
	if err := os.WriteFile(path, []byte("not-a-token\n"), 0o600); err != nil {
		t.Fatalf("write malformed token: %v", err)
	}

	_, err := LoadOrCreateToken(path)
	if err == nil {
		t.Fatal("LoadOrCreateToken accepted a corrupt token file")
	}
	for _, want := range []string{path, "corrupt", "remove"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("diagnosis %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "concurrent") {
		t.Errorf("diagnosis blames concurrency for a corrupt file: %q", err)
	}

	kept, readErr := os.ReadFile(filepath.Clean(path))
	if readErr != nil || string(kept) != "not-a-token\n" {
		t.Fatalf("token file = (%q, %v), want the operator's file untouched", kept, readErr)
	}
}

func TestCheckToken(t *testing.T) {
	t.Parallel()

	const want = "the-secret"

	valid := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(authMetadataKey, want))
	if err := checkToken(valid, want); err != nil {
		t.Fatalf("checkToken with the right token returned %v", err)
	}

	wrong := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(authMetadataKey, "nope"))
	if status.Code(checkToken(wrong, want)) != codes.Unauthenticated {
		t.Fatal("wrong token was not Unauthenticated")
	}

	if status.Code(checkToken(context.Background(), want)) != codes.Unauthenticated {
		t.Fatal("missing metadata was not Unauthenticated")
	}
}

func TestTokenCredsAttachMetadata(t *testing.T) {
	t.Parallel()

	md, err := tokenCreds("abc").GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata returned error: %v", err)
	}

	if md[authMetadataKey] != "abc" {
		t.Fatalf("metadata = %v, want the token under %q", md, authMetadataKey)
	}

	if tokenCreds("abc").RequireTransportSecurity() {
		t.Fatal("local creds must not require transport security")
	}
}
