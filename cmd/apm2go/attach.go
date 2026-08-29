package main

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/yigitf/apm2go/internal/assets"
	"github.com/yigitf/apm2go/internal/attachhelper"
	"github.com/yigitf/apm2go/internal/discovery"
	"github.com/yigitf/apm2go/internal/ingesttoken"
	"github.com/yigitf/apm2go/internal/injector"
)

func newAttachCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <pid>",
		Short: "Instrument a running JVM without restarting it",
		Long: "Inject the tracing agent into an already-running JVM. The process keeps running " +
			"throughout; no restart is needed. Requires root, or the same user as the target.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := strconv.Atoi(args[0])
			if err != nil || pid <= 0 {
				return fmt.Errorf("invalid pid %q", args[0])
			}

			cfg, log, err := gf.load()
			if err != nil {
				return err
			}

			scanner := discovery.NewScanner(cfg.Discovery.ProcRoot)
			jvm, err := scanner.Inspect(pid)
			if err != nil {
				return fmt.Errorf("inspect pid %d: %w", pid, err)
			}
			if jvm == nil {
				return fmt.Errorf("pid %d is not a Java process", pid)
			}

			store, err := assets.New()
			if err != nil {
				return err
			}
			helperStore, err := attachhelper.New()
			if err != nil {
				return err
			}
			if err := helperStore.Verify(); err != nil {
				return fmt.Errorf("attach helper: %w", err)
			}
			helperPath, err := helperStore.Materialize(filepath.Join(cfg.DataDir, "attach-helper"))
			if err != nil {
				return fmt.Errorf("stage attach helper: %w", err)
			}
			// A one-shot attach mints a token the running service does not know
			// about. That is intended: the manual path is for diagnosis, and
			// the daemon re-attaches idempotently with its own token.
			inj, err := injector.New(cfg.Attach, cfg.Discovery.ProcRoot, helperPath, store, ingesttoken.NewRegistry(), log)
			if err != nil {
				return err
			}

			ctx, stop := runContext(cmd.Context())
			defer stop()

			cmd.Printf("Attaching to %s (pid %d, Java %s, user %s)...\n",
				jvm.ServiceName, jvm.PID, jvm.JavaVersion, jvm.User)

			res, err := inj.Inject(ctx, jvm)
			if err != nil {
				return err
			}

			if res.AlreadyInstrumented {
				cmd.Printf("Already instrumented by apm2go; nothing to do.\n")
				return nil
			}

			cmd.Printf("Attached in %s. Traces will be exported to %s\n",
				res.Duration.Round(1e6), res.Endpoint)
			for _, w := range res.Warnings {
				if w != "" {
					cmd.Printf("\nNote: %s\n", w)
				}
			}
			return nil
		},
	}
}
