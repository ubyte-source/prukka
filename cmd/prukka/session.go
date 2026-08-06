package main

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/session"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

func newSessionCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage sessions on the local daemon",
	}

	cmd.AddCommand(
		newSessionAddCmd(flags),
		newSessionListCmd(flags),
		newSessionRemoveCmd(flags),
		newSessionLangsCmd(flags),
		newSessionPushCmd(flags),
	)

	return cmd
}

// sessionAddFlags carries the `session add` inputs.
type sessionAddFlags struct {
	in       string
	langs    string
	profile  string
	subs     string
	source   string
	dubLangs string
	pair     string
	delay    time.Duration
	delaySet bool
}

func newSessionAddCmd(flags *rootFlags) *cobra.Command {
	af := &sessionAddFlags{}

	cmd := &cobra.Command{
		Use:   "add <slug>",
		Short: "Create a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionAdd(cmd, flags, af, args[0])
		},
	}

	cmd.Flags().StringVar(&af.in, "in", "", "source URL: rtmp://, srt://, file:// or device://")
	cmd.Flags().StringVar(&af.langs, "langs", "", "target languages, comma-separated (default: config defaults)")
	cmd.Flags().StringVar(&af.profile, "profile", "broadcast", "session profile: broadcast or call")
	cmd.Flags().StringVar(&af.subs, "subs", "", "subtitles: off, vtt or burn (default: config defaults)")
	cmd.Flags().StringVar(&af.source, "source", "auto", "source language hint (default: auto-detect)")
	cmd.Flags().StringVar(&af.dubLangs, "dub-langs", "all", "dubbed subset of --langs: all, none or comma-separated tags")
	cmd.Flags().StringVar(
		&af.pair, "pair", "", "paired session slug; linked removal requires reciprocal --pair flags",
	)
	// Zero, not a sentinel: pflag prints the registered default in the help.
	// Changed() carries not-set.
	cmd.Flags().DurationVar(&af.delay, "delay", 0, "session delay D (default: config defaults)")
	must(cmd.MarkFlagRequired("in"))

	return cmd
}

// runSessionAdd sends only explicit CLI values; the daemon applies its live
// configuration defaults.
func runSessionAdd(cmd *cobra.Command, flags *rootFlags, af *sessionAddFlags, slug string) error {
	af.delaySet = cmd.Flags().Changed("delay")
	req, err := createSessionRequest(af, slug)
	if err != nil {
		return err
	}

	return withControl(cmd, flags, func(ctx context.Context, client v1.ControlClient) error {
		resp, createErr := client.CreateSession(ctx, req)
		if createErr != nil {
			return createErr
		}

		s := resp.GetSession()
		cmd.Printf("session %q created (%s, langs: %s)\n",
			s.GetSlug(), s.GetProfile(), strings.Join(s.GetLangs(), ", "))

		return nil
	})
}

func createSessionRequest(af *sessionAddFlags, slug string) (*v1.CreateSessionRequest, error) {
	var tags []string
	if af.langs != "" {
		tags = strings.Split(af.langs, ",")
	}

	flagMap := make(map[string]string)
	if af.subs != "" {
		flagMap[session.FlagSubs] = af.subs
	}
	if dubErr := applyDubFlag(flagMap, af.dubLangs, tags); dubErr != nil {
		return nil, dubErr
	}
	if af.pair != "" {
		if af.pair == slug {
			return nil, fmt.Errorf("--pair must name a different session than %q", slug)
		}
		flagMap[session.FlagPair] = af.pair
	}

	// Validate the hint client-side for fast feedback; the lane re-parses.
	if hint, hintErr := lang.Parse(af.source); hintErr != nil {
		return nil, hintErr
	} else if hint != "" {
		flagMap[session.FlagSource] = string(hint)
	}

	wire := &v1.Session{
		Slug:      slug,
		Profile:   af.profile,
		SourceUrl: af.in,
		Langs:     tags,
		Flags:     flagMap,
	}
	if af.delaySet {
		wire.DelaySeconds = new(af.delay.Seconds())
	}

	return &v1.CreateSessionRequest{Session: wire}, nil
}

func applyDubFlag(flags map[string]string, raw string, targets []string) error {
	switch strings.TrimSpace(raw) {
	case "", "all":
		return nil
	case "none":
		flags[session.FlagDub] = session.Off

		return nil
	}

	available := make(map[core.Lang]bool, len(targets))
	for _, value := range targets {
		tag, err := lang.Parse(value)
		if err != nil {
			return err
		}
		available[tag] = true
	}

	selected := make([]string, 0)
	for value := range strings.SplitSeq(raw, ",") {
		tag, err := lang.Parse(value)
		if err != nil {
			return err
		}
		if len(available) > 0 && !available[tag] {
			return fmt.Errorf("dub language %q is not present in --langs", tag)
		}
		selected = append(selected, string(tag))
	}
	flags[session.FlagDubLangs] = strings.Join(selected, ",")

	return nil
}

func newSessionListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withControl(cmd, flags, func(ctx context.Context, client v1.ControlClient) error {
				resp, err := client.ListSessions(ctx, &v1.ListSessionsRequest{})
				if err != nil {
					return err
				}

				return printSessions(cmd, resp.GetSessions())
			})
		},
	}
}

func printSessions(cmd *cobra.Command, sessions []*v1.Session) error {
	if len(sessions) == 0 {
		cmd.Println("no sessions — create one with `prukka session add`")

		return nil
	}

	// ERROR trails every fixed-width column: its detail is unbounded and
	// mid-table it would push the rest of the row past the fold.
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if err := row(w, "SLUG", "STATUS", "PROFILE", "LANGS", "SOURCE", "DELAY", "ERROR"); err != nil {
		return err
	}

	for _, s := range sessions {
		source := s.GetSourceLabel()
		if source == "" && s.GetSourceUrl() != "" {
			// Compatibility with daemons predating source_label.
			source = control.PublicSourceLabel(s.GetSourceUrl())
		}
		if source == "" {
			source = "-"
		}
		cols := []string{
			s.GetSlug(),
			s.GetStatus(),
			s.GetProfile(),
			strings.Join(s.GetLangs(), ","),
			source,
			fmt.Sprintf("%.0fs", s.GetDelaySeconds()),
			statusError(s.GetError()),
		}
		if err := row(w, cols...); err != nil {
			return err
		}
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

func statusError(detail string) string {
	if detail == "" {
		return "-"
	}

	return detail
}

// pushReadyBudget bounds how long a push waits for the lane to warm its dubbed
// track; session startup pays model loads measured in seconds.
const pushReadyBudget = 45 * time.Second

// pushRetryable reports the daemon's tagged not-ready answer. A bare
// codes.Unavailable is a transport failure and retrying it would just spin.
func pushRetryable(err error) bool {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		return false
	}
	for _, detail := range st.Details() {
		if info, isInfo := detail.(*errdetails.ErrorInfo); isInfo &&
			info.GetReason() == control.PushNotReadyReason {
			return true
		}
	}

	return false
}

func newSessionPushCmd(flags *rootFlags) *cobra.Command {
	var langTag, subs string

	cmd := &cobra.Command{
		Use:   "push <slug> <target-url>",
		Short: "Push one language's dubbed output to RTMP, SRT or a device",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &v1.PushRequest{Slug: args[0], Lang: langTag, TargetUrl: args[1], Subs: subs}
			deadline := time.Now().Add(pushReadyBudget)
			waited := false
			for {
				err := withControl(cmd, flags, func(ctx context.Context, client v1.ControlClient) error {
					_, pushErr := client.Push(ctx, req)

					return pushErr
				})
				if err == nil {
					cmd.Printf("pushing %s/%s\n", args[0], langTag)

					return nil
				}
				if !pushRetryable(err) || time.Now().After(deadline) {
					return err
				}
				if !waited {
					waited = true
					cmd.PrintErrf("waiting for the dubbed track of %s/%s…\n", args[0], langTag)
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(time.Second):
				}
			}
		},
	}

	cmd.Flags().StringVar(&langTag, "lang", "", "target language to push")
	cmd.Flags().StringVar(&subs, "subs", "off",
		"subtitles on the pushed video: off, vtt (sidecar via HLS) or burn (drawn onto the video)")
	must(cmd.MarkFlagRequired("lang"))

	return cmd
}

func newSessionRemoveCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <slug>",
		Short: "Remove a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withControl(cmd, flags, func(ctx context.Context, client v1.ControlClient) error {
				if _, err := client.DeleteSession(ctx, &v1.DeleteSessionRequest{Slug: args[0]}); err != nil {
					return err
				}

				cmd.Printf("session %q removed\n", args[0])

				return nil
			})
		},
	}
}

// newSessionLangsCmd turns interspersed parsing off so "-de" after the slug
// reads as a removal rather than a flag.
func newSessionLangsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "langs <slug> [+lang] [-lang] ...",
		Short: "Hot-add (+tag) and hot-remove (-tag) target languages",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionLangs(cmd, flags, args)
		},
	}
	cmd.Flags().SetInterspersed(false)

	return cmd
}

func runSessionLangs(cmd *cobra.Command, flags *rootFlags, args []string) error {
	add, remove, err := splitLangArgs(args[1:])
	if err != nil {
		return err
	}

	req := &v1.UpdateSessionRequest{Slug: args[0], AddLangs: add, RemoveLangs: remove}

	return withControl(cmd, flags, func(ctx context.Context, client v1.ControlClient) error {
		resp, updateErr := client.UpdateSession(ctx, req)
		if updateErr != nil {
			return updateErr
		}

		cmd.Printf("session %q languages: %s\n",
			resp.GetSession().GetSlug(), strings.Join(resp.GetSession().GetLangs(), ", "))

		return nil
	})
}

// splitLangArgs validates +/− prefixed language changes client-side so
// typos fail fast with the registry's suggestions.
func splitLangArgs(changes []string) (add, remove []string, err error) {
	for _, change := range changes {
		tag, found := strings.CutPrefix(change, "+")
		if found {
			if _, parseErr := lang.Parse(tag); parseErr != nil {
				return nil, nil, parseErr
			}

			add = append(add, tag)

			continue
		}

		tag, found = strings.CutPrefix(change, "-")
		if !found {
			return nil, nil, fmt.Errorf("language change %q must start with + or -", change)
		}

		if _, parseErr := lang.Parse(tag); parseErr != nil {
			return nil, nil, parseErr
		}

		remove = append(remove, tag)
	}

	return add, remove, nil
}
