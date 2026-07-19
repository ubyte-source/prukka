package control

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
