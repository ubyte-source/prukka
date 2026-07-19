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
// closes the connection; every client subcommand goes through here.
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

	// The state has to be read after the call, never as an argument next to
	// it: it only says whether the daemon answered once the call is over.
	callErr := fn(ctx, v1.NewControlClient(conn))

	return cliError(conn.GetState(), callErr)
}

// controlError shows a control-plane failure as the one line an operator can
// act on, while keeping the gRPC status reachable through Unwrap:
// status.FromError looks through wrappers, so callers that branch on the
// daemon's tagged details still work on a translated error (pushRetryable).
type controlError struct {
	cause   error
	message string
}

// Error implements error with the operator-facing wording.
func (e *controlError) Error() string { return e.message }

// Unwrap keeps the originating gRPC status inspectable.
func (e *controlError) Unwrap() error { return e.cause }

// cliError strips gRPC's "rpc error: code = ... desc =" plumbing and names the
// next step for the failure users hit most: no daemon behind the socket.
//
// codes.Unavailable alone does not identify that case — the daemon mints it
// too, for the tagged not-ready push and for "engine catalog unavailable" —
// and the transport wording that would identify it is gRPC-internal. The
// connection state settles it instead: a call the daemon answered leaves the
// connection Ready, a call that never reached the endpoint does not. The
// wording claims only that, since a refused, mistyped or over-long socket path
// fails the same way with a daemon very much alive; the path is there to be
// inspected.
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
