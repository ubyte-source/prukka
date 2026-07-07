package main

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/core/lang"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// newSessionCmd groups session management.
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
	in      string
	langs   string
	profile string
	subs    string
	source  string
	budget  float64
	delay   time.Duration
}

// newSessionAddCmd creates a session:
//
//	prukka session add demo --in rtmp://0.0.0.0:1935/in/demo --langs it,en
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
	cmd.Flags().StringVar(&af.profile, "profile", "broadcast", "session profile: broadcast, call or agent")
	cmd.Flags().StringVar(&af.subs, "subs", "", "subtitles: off, vtt or burn (default: config defaults)")
	cmd.Flags().StringVar(&af.source, "source", "auto", "source language hint (default: auto-detect)")
	cmd.Flags().Float64Var(&af.budget, "budget", 0, "budget in EUR per hour (default: config defaults)")
	cmd.Flags().DurationVar(&af.delay, "delay", -1, "session delay D (default: config defaults)")
	must(cmd.MarkFlagRequired("in"))

	return cmd
}

// runSessionAdd resolves defaults from config and creates the session;
// tags are validated server-side.
func runSessionAdd(cmd *cobra.Command, flags *rootFlags, af *sessionAddFlags, slug string) error {
	cfg, _, err := flags.load()
	if err != nil {
		return err
	}

	tags := strings.Split(af.langs, ",")

	if af.langs == "" {
		tags = make([]string, len(cfg.Defaults.Langs))
		for i, l := range cfg.Defaults.Langs {
			tags[i] = string(l)
		}
	}

	if af.subs == "" {
		af.subs = cfg.Defaults.Subs
	}

	if af.budget == 0 {
		af.budget = cfg.Budgets.PerSessionEURPerHour
	}

	if af.delay < 0 {
		af.delay = cfg.Defaults.Delay.Std()
	}

	flagMap := map[string]string{"subs": af.subs, "bed": cfg.Defaults.Bed}

	// Validate the hint client-side for fast feedback; the lane re-parses.
	if hint, hintErr := lang.Parse(af.source); hintErr != nil {
		return hintErr
	} else if hint != "" {
		flagMap["source"] = string(hint)
	}

	req := &v1.CreateSessionRequest{Session: &v1.Session{
		Slug:             slug,
		Profile:          af.profile,
		SourceUrl:        af.in,
		Langs:            tags,
		Flags:            flagMap,
		BudgetEurPerHour: af.budget,
		DelaySeconds:     af.delay.Seconds(),
	}}

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

// newSessionListCmd prints all sessions as a table.
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

// printSessions renders the session table.
func printSessions(cmd *cobra.Command, sessions []*v1.Session) error {
	if len(sessions) == 0 {
		cmd.Println("no sessions — create one with `prukka session add`")

		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if err := row(w, "SLUG", "PROFILE", "LANGS", "SOURCE", "BUDGET €/H", "DELAY"); err != nil {
		return err
	}

	for _, s := range sessions {
		cols := []string{
			s.GetSlug(),
			s.GetProfile(),
			strings.Join(s.GetLangs(), ","),
			s.GetSourceUrl(),
			fmt.Sprintf("%.2f", s.GetBudgetEurPerHour()),
			fmt.Sprintf("%.0fs", s.GetDelaySeconds()),
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

// newSessionPushCmd pushes one language's output to an external target:
//
//	prukka session push demo --lang en --subs burn rtmp://a.rtmp.youtube.com/live2/KEY
func newSessionPushCmd(flags *rootFlags) *cobra.Command {
	var langTag, subs string

	cmd := &cobra.Command{
		Use:   "push <slug> <target-url>",
		Short: "Push one language's dubbed output to an RTMP target",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withControl(cmd, flags, func(ctx context.Context, client v1.ControlClient) error {
				req := &v1.PushRequest{Slug: args[0], Lang: langTag, TargetUrl: args[1], Subs: subs}
				if _, err := client.Push(ctx, req); err != nil {
					return err
				}

				cmd.Printf("pushing %s/%s to %s\n", args[0], langTag, args[1])

				return nil
			})
		},
	}

	cmd.Flags().StringVar(&langTag, "lang", "", "target language to push")
	cmd.Flags().StringVar(&subs, "subs", "off",
		"subtitles on the pushed video: off, vtt (sidecar via HLS) or burn (drawn onto the video)")
	must(cmd.MarkFlagRequired("lang"))

	return cmd
}

// newSessionRemoveCmd deletes a session.
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

// newSessionLangsCmd hot-adds and hot-removes languages (flag parsing off
// so "-de" reads as a removal):
//
//	prukka session langs demo +fr -de
func newSessionLangsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:                "langs <slug> [+lang] [-lang] ...",
		Short:              "Hot-add (+tag) and hot-remove (-tag) target languages",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionLangs(cmd, flags, args)
		},
	}

	return cmd
}

// runSessionLangs parses +/− language args and applies them.
func runSessionLangs(cmd *cobra.Command, flags *rootFlags, args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		return cmd.Help()
	}

	if len(args) < 2 {
		return fmt.Errorf("usage: %s", cmd.Use)
	}

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
