package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/paths"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// rpcTimeout bounds every CLI control-plane call.
const rpcTimeout = 5 * time.Second

// withControl dials the local daemon, runs fn with a bounded context and
// closes the connection.
func withControl(
	cmd *cobra.Command, flags *rootFlags, fn func(ctx context.Context, client v1.ControlClient) error,
) error {
	cfg, _, err := flags.load()
	if err != nil {
		return err
	}

	conn, err := control.Dial(cfg)
	if err != nil {
		return err
	}

	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			cmd.PrintErrln("warning: closing control connection:", closeErr)
		}
	}()

	ctx, cancel := context.WithTimeout(cmd.Context(), rpcTimeout)
	defer cancel()

	// The state only says whether the daemon answered once the call is over,
	// so it must be read after fn, never passed alongside it.
	callErr := fn(ctx, v1.NewControlClient(conn))

	return cliError(conn.GetState(), callErr)
}

// controlError shows a control-plane failure as the one line an operator can
// act on, keeping the gRPC status reachable through Unwrap.
type controlError struct {
	cause   error
	message string
}

// Error implements error with the operator-facing wording.
func (e *controlError) Error() string { return e.message }

// Unwrap keeps the originating gRPC status inspectable.
func (e *controlError) Unwrap() error { return e.cause }

// cliError strips gRPC's "rpc error: code = ... desc =" plumbing and names the
// next step when no daemon answers. codes.Unavailable alone does not identify
// that case — the daemon mints it too — so the connection state settles it: a
// call the daemon answered leaves the connection Ready.
func cliError(state connectivity.State, err error) error {
	if err == nil {
		return nil
	}

	st, ok := status.FromError(err)
	if !ok {
		return err
	}

	if st.Code() == codes.Unavailable && state != connectivity.Ready {
		return &controlError{
			message: "no daemon answering on " + paths.IPCPath() +
				" — start it with `prukka up` or `prukka service install --now`",
			cause: err,
		}
	}

	return &controlError{message: st.Message(), cause: err}
}

// row writes one tab-separated table row, surfacing broken-pipe errors.
func row(w io.Writer, cols ...string) error {
	if _, err := fmt.Fprintln(w, strings.Join(cols, "\t")); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
