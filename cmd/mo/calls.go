package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
	"github.com/rvanmech/unky-mo/internal/review"
	"github.com/spf13/cobra"
)

// errFindings makes `mo calls --fail-on` exit 1 without cobra printing its
// usage.
type errFindings int

func (e errFindings) Error() string { return fmt.Sprintf("%d finding(s) matched --fail-on", int(e)) }

func callsCmd() *cobra.Command {
	var base, format string
	var failOn []string
	c := &cobra.Command{
		Use:   "calls [dir]",
		Short: "Show the call graph of the change in the checkout containing dir (default: .)",
		Long: "Lists the functions the change adds, removes or changes, the calls it adds and drops,\n" +
			"and findings: removed functions still called, changed signatures whose callers weren't\n" +
			"updated, and changed functions no test reaches. The web Overview's Functions view shows\n" +
			"the same.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			if base != gitfiles.ModeBranch && base != gitfiles.ModeHead {
				return fmt.Errorf("--base must be %s or %s", gitfiles.ModeBranch, gitfiles.ModeHead)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			o, err := gitfiles.GetOverview(ctx, moexec.DefaultCommander, dir, base)
			if err != nil {
				return err
			}
			cg, err := review.Calls(ctx, moexec.DefaultCommander, o)
			if err != nil {
				return err
			}
			switch format {
			case "text":
				review.WriteCallsText(os.Stdout, cg)
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(cg); err != nil {
					return err
				}
			case "dot":
				review.WriteCallsDOT(os.Stdout, cg)
			default:
				return fmt.Errorf("--format must be text, json or dot")
			}
			fail := map[string]bool{}
			for _, k := range failOn {
				fail[k] = true
			}
			n := 0
			for _, f := range cg.Findings {
				if fail[f.Kind] {
					n++
				}
			}
			if n > 0 {
				return errFindings(n)
			}
			return nil
		},
	}
	c.Flags().StringVar(&base, "base", gitfiles.ModeBranch, "compare with the branch's merge base (branch) or HEAD (head)")
	c.Flags().StringVar(&format, "format", "text", "output: text, json or dot (Graphviz)")
	c.Flags().StringSliceVar(&failOn, "fail-on", nil, "exit 1 when there are findings of these kinds (removed-called, signature-callers, untested)")
	return c
}
