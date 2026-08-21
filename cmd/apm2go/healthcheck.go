package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

// healthTimeout bounds the probe. A container health check runs on a schedule,
// so hanging is worse than failing: a hung probe is indistinguishable from a
// dead process and delays the verdict either way.
const healthTimeout = 5 * time.Second

// newHealthcheckCmd registers the probe a container image's HEALTHCHECK runs.
//
// It exists because the port is configurable. A health check that curls a
// hardcoded address reports a perfectly healthy apm2go as unhealthy the moment
// anyone changes api.addr — which under an orchestrator means restarting a
// working process in a loop. Resolving the address through the same
// configuration the server itself used is the only way the two cannot drift.
func newHealthcheckCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "healthcheck",
		Short: "Probe the local API, for container health checks",
		Long: "Exit zero when this apm2go's API answers, non-zero otherwise. The address " +
			"comes from the same configuration the server reads, so the two cannot disagree.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := gf.load()
			if err != nil {
				return err
			}

			url := "http://" + probeAddress(cfg.API.Addr) + "/api/v1/health"
			client := &http.Client{Timeout: healthTimeout}

			resp, err := client.Get(url)
			if err != nil {
				return fmt.Errorf("apm2go is not answering on %s: %w", url, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("apm2go answered %s on %s", resp.Status, url)
			}
			return nil
		},
	}
}

// probeAddress turns a listen address into one that can be connected to.
//
// A wildcard bind says where the server accepts connections, not where to reach
// it; loopback is the address that is always correct from inside the same
// network namespace, which is where a container health check runs.
func probeAddress(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return listenAddr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
