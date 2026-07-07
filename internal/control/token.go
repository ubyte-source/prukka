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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// authMetadataKey names the gRPC metadata entry carrying the per-install
// control token (a second factor is always on, even locally).
const authMetadataKey = "prukka-control-auth"

// tokenBytes is the entropy of a freshly minted control token.
const tokenBytes = 32

// LoadOrCreateToken returns the per-install control token, minting and
// persisting one (mode 0600) on first daemon start.
func LoadOrCreateToken(path string) (string, error) {
	existing, err := readToken(path)
	if err != nil {
		return "", err
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

	return strings.TrimSpace(string(data)), nil
}

// mintToken generates and persists a fresh random token.
func mintToken(path string) (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint control token: %w", err)
	}

	token := hex.EncodeToString(raw)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}

	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write control token: %w", err)
	}

	return token, nil
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
