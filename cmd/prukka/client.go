package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/control"

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

	return fn(ctx, v1.NewControlClient(conn))
}

// row writes one tab-separated table row, surfacing broken-pipe errors.
func row(w io.Writer, cols ...string) error {
	if _, err := fmt.Fprintln(w, strings.Join(cols, "\t")); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
