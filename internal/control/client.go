package control

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/paths"
)

// Dial connects a CLI client to the local daemon with the per-install
// token; the caller owns closing the connection.
func Dial(cfg *config.Config) (*grpc.ClientConn, error) {
	token, err := ReadToken(paths.TokenPath())
	if err != nil {
		return nil, err
	}

	var ipcTLS *tls.Config

	if cfg.Control.IPCTLS {
		ipcTLS, err = ClientIPCTLS(paths.StateDir())
		if err != nil {
			return nil, err
		}
	}

	return dialGRPC(paths.IPCPath(), token, ipcTLS)
}

// dialGRPC opens a lazy client connection to the IPC endpoint with the
// token attached to every call.
func dialGRPC(ipcPath, token string, ipcTLS *tls.Config) (*grpc.ClientConn, error) {
	transport := insecure.NewCredentials()
	if ipcTLS != nil {
		transport = credentials.NewTLS(ipcTLS)
	}

	conn, err := grpc.NewClient("passthrough:///prukkad",
		grpc.WithTransportCredentials(transport),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return dialIPC(ctx, ipcPath)
		}),
		grpc.WithPerRPCCredentials(tokenCreds(token)),
	)
	if err != nil {
		return nil, fmt.Errorf("dial control endpoint: %w", err)
	}

	return conn, nil
}
