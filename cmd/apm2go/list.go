package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/yigitf/apm2go/internal/container"
	"github.com/yigitf/apm2go/internal/discovery"
)

func newListCmd(gf *globalFlags) *cobra.Command {
	var showAll bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the JVM processes running on this host",
		Long: "Scan /proc for Java processes and print what apm2go knows about each one, " +
			"including whether it can be instrumented. This performs discovery only; " +
			"nothing is attached.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, logger, err := gf.load()
			if err != nil {
				return err
			}

			scanner := discovery.NewScanner(cfg.Discovery.ProcRoot).
				WithContainers(container.NewResolver(cfg.Discovery.DockerSocket, logger))
			jvms, err := scanner.Scan()
			if err != nil {
				return err
			}

			filter := discovery.NewFilter(cfg.Discovery.Include, cfg.Discovery.Exclude)
			if !showAll {
				kept := jvms[:0]
				for _, j := range jvms {
					if filter.Accept(j) {
						kept = append(kept, j)
					}
				}
				jvms = kept
			}

			if len(jvms) == 0 {
				cmd.Println("No JVM processes found.")
				if cfg.Discovery.ProcRoot != "/proc" {
					cmd.Printf("(scanned %s)\n", cfg.Discovery.ProcRoot)
				}
				return nil
			}

			sort.Slice(jvms, func(i, j int) bool { return jvms[i].PID < jvms[j].PID })
			printJVMTable(jvms)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "include JVMs excluded by the discovery filters")
	return cmd
}

// printJVMTable renders the discovery result as an aligned table.
func printJVMTable(jvms []*discovery.JVM) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PID\tSERVICE\tJAVA\tUSER\tWHERE\tSTATUS\tCOMMAND")

	for _, j := range jvms {
		status := "attachable"
		if ok, reason := j.Attachable(); !ok {
			status = reason
		} else if j.AlreadyInstrumented {
			status = "attachable (another agent present)"
		}

		java := j.JavaVersion
		if java == "" {
			java = "unknown"
		}
		user := j.User
		if user == "" {
			user = strconv.Itoa(j.UID)
		}

		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			j.PID, j.ServiceName, java, user, whereItRuns(j), status, summarizeCommand(j))
	}
	_ = w.Flush()
}

// whereItRuns names the process's home in one column: a pod, a container, a
// systemd unit, or the host itself.
func whereItRuns(j *discovery.JVM) string {
	switch {
	case j.Container != nil && j.Container.PodName != "":
		return "pod " + j.Container.PodName
	case j.Container != nil && j.Container.Name != "":
		return "container " + j.Container.Name
	case j.InContainer:
		return "container"
	case j.SystemdUnit != "":
		return "unit " + j.SystemdUnit
	default:
		return "host"
	}
}

// summarizeCommand renders the most identifying part of a command line, since
// a full JVM invocation is far too long for a table.
func summarizeCommand(j *discovery.JVM) string {
	switch {
	case j.JarPath != "":
		return "-jar " + j.JarPath
	case j.MainClass != "":
		return j.MainClass
	default:
		return truncate(strings.Join(j.Cmdline, " "), 60)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
