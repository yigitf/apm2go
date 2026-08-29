package main

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/yigitf/apm2go/internal/config"
	"github.com/yigitf/apm2go/internal/logging"
	"github.com/yigitf/apm2go/internal/version"
)

// defaultConfigPath is where the RPM/DEB packages install the config file.
const defaultConfigPath = "/etc/apm2go/config.yaml"

// globalFlags are shared by every subcommand that needs configuration.
type globalFlags struct {
	configPath string
	logLevel   string
	dataDir    string
}

func newRootCmd() *cobra.Command {
	var gf globalFlags

	cmd := &cobra.Command{
		Use:           "apm2go",
		Short:         "Standalone APM for applications already running on a host",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.String(),
	}

	pf := cmd.PersistentFlags()
	pf.StringVarP(&gf.configPath, "config", "c", defaultConfigPath, "path to config file")
	pf.StringVar(&gf.logLevel, "log-level", "", "override log level (debug, info, warn, error)")
	pf.StringVar(&gf.dataDir, "data-dir", "", "override data directory")

	cmd.AddCommand(
		newRunCmd(&gf),
		newListCmd(&gf),
		newAttachCmd(&gf),
		newVersionCmd(),
		newHealthcheckCmd(&gf),
	)
	return cmd
}

// load resolves configuration for a subcommand and builds its logger.
func (gf *globalFlags) load() (config.Config, *slog.Logger, error) {
	cfg, err := config.Load(gf.configPath)
	if err != nil {
		return cfg, nil, err
	}
	if gf.logLevel != "" {
		cfg.Log.Level = gf.logLevel
	}
	if gf.dataDir != "" {
		cfg.DataDir = gf.dataDir
	}
	return cfg, logging.New(cfg.Log, os.Stderr), nil
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Println(version.String())
			return nil
		},
	}
}
