package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// authMetadataKey names the gRPC metadata entry carrying the per-install
// control token (a second factor is always on, even locally).
const authMetadataKey = "prukka-control-auth"

const (
	// tokenBytes is the entropy of a freshly minted control token.
	tokenBytes      = 32
	tokenWriteWait  = time.Second
	tokenWriteRetry = 5 * time.Millisecond
)

var errInvalidControlToken = errors.New("invalid control token")

// LoadOrCreateToken returns the per-install control token, minting and
// persisting one (mode 0600) on first daemon start.
func LoadOrCreateToken(path string) (string, error) {
	existing, err := readToken(path)
	if err != nil {
		if !errors.Is(err, errInvalidControlToken) {
			return "", err
		}

		return waitForToken(path)
	}

	if existing != "" {
		return existing, nil
	}

	return mintToken(path)
}

// ReadToken returns the existing control token. Only the daemon mints
// tokens: clients fail here until a daemon has initialized the state dir.
func ReadToken(path string) (string, error) {
	token, err := readToken(path)
	if err != nil {
		return "", err
	}

	if token == "" {
		return "", fmt.Errorf("no control token at %s — start the daemon once (`prukka up`) to initialize it", path)
	}

	return token, nil
}

// readToken loads the token file, mapping absence to an empty token.
func readToken(path string) (string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("read control token: %w", err)
	}

	token := strings.TrimSpace(string(data))
	if len(token) != tokenBytes*2 {
		return "", fmt.Errorf("%w: want %d hexadecimal characters", errInvalidControlToken, tokenBytes*2)
	}
	if _, err := hex.DecodeString(token); err != nil {
		return "", fmt.Errorf("%w: decode hexadecimal content: %w", errInvalidControlToken, err)
	}

	return token, nil
}

// mintToken generates and persists a fresh random token.
func mintToken(path string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}

	file, err := createTokenFile(path)
	if errors.Is(err, fs.ErrExist) {
		return waitForToken(path)
	}
	if err != nil {
		return "", err
	}
	if err := persistToken(file, path, token); err != nil {
		return "", err
	}

	return token, nil
}

func newToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint control token: %w", err)
	}

	return hex.EncodeToString(raw), nil
}

func createTokenFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create control token: %w", err)
	}

	return file, nil
}

func persistToken(file *os.File, path, token string) (err error) {
	closed := false
	published := false
	defer func() {
		if !closed {
			err = errors.Join(err, file.Close())
		}
		if !published {
			removeErr := os.Remove(filepath.Clean(path))
			if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove incomplete control token: %w", removeErr))
			}
		}
	}()

	if _, err = file.WriteString(token + "\n"); err != nil {
		return fmt.Errorf("write control token: %w", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Errorf("sync control token: %w", err)
	}
	if err = file.Close(); err != nil {
		closed = true

		return fmt.Errorf("close control token: %w", err)
	}
	closed = true
	published = true

	return nil
}

func waitForToken(path string) (string, error) {
	deadline := time.Now().Add(tokenWriteWait)
	var lastErr error

	for {
		token, err := readToken(path)
		if err == nil && token != "" {
			return token, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr == nil {
				lastErr = errInvalidControlToken
			}

			return "", fmt.Errorf("read control token after concurrent creation: %w", lastErr)
		}

		time.Sleep(tokenWriteRetry)
	}
}

// unaryAuth enforces the control token on unary RPCs.
func unaryAuth(token string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := checkToken(ctx, token); err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// streamAuth enforces the control token on streaming RPCs.
func streamAuth(token string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := checkToken(ss.Context(), token); err != nil {
			return err
		}

		return handler(srv, ss)
	}
}

// checkToken compares the caller's token in constant time.
func checkToken(ctx context.Context, want string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing control token")
	}

	got := md.Get(authMetadataKey)
	if len(got) == 1 && subtle.ConstantTimeCompare([]byte(got[0]), []byte(want)) == 1 {
		return nil
	}

	return status.Error(codes.Unauthenticated, "missing or invalid control token")
}

// tokenCreds attaches the control token to every outgoing call.
type tokenCreds string

// GetRequestMetadata implements credentials.PerRPCCredentials.
func (t tokenCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{authMetadataKey: string(t)}, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials; the
// OS already protects the local transport.
func (tokenCreds) RequireTransportSecurity() bool {
	return false
}
