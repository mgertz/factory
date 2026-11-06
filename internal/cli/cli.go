package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mgertz/factory/internal/config"
	"github.com/mgertz/factory/internal/logging"
	"github.com/mgertz/factory/internal/paths"
	"github.com/mgertz/factory/internal/runtime"
	"github.com/spf13/cobra"
)

func New() *cobra.Command {
	return newCommand(paths.Resolve)
}

func newCommand(resolve func() (paths.Paths, error)) *cobra.Command {
	root := &cobra.Command{
		Use: "factory", Short: "A local, self-hosted software factory",
		SilenceUsage: true, SilenceErrors: true,
		Args: cobra.NoArgs,
	}
	root.SetOut(os.Stdout)
	root.CompletionOptions.DisableDefaultCmd = true
	configuration := &cobra.Command{Use: "config", Short: "Manage Factory configuration", Args: cobra.NoArgs}
	configuration.AddCommand(&cobra.Command{
		Use: "init", Short: "Create a commented configuration without overwriting existing files", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := resolve()
			if err != nil {
				return err
			}
			if err := config.Init(p.ConfigFile); err != nil {
				return err
			}
			cmd.Println("Configuration created:", p.ConfigFile)
			return nil
		},
	})
	var foreground bool
	run := &cobra.Command{
		Use: "run", Short: "Start Factory in the background", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := resolve()
			if err != nil {
				return err
			}
			if foreground {
				return runtime.Serve(cmd.Context(), p, cmd.ErrOrStderr(), nil)
			}
			status, err := runtime.Start(cmd.Context(), p)
			if err != nil {
				return err
			}
			cmd.Printf("Factory started successfully (PID: %d)\n", status.PID)
			return nil
		},
	}
	run.Flags().BoolVar(&foreground, "foreground", false, "Run in the foreground and also log to stderr")
	status := &cobra.Command{
		Use: "status", Short: "Show Factory state, PID and uptime", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := resolve()
			if err != nil {
				return err
			}
			status, err := runtime.Inspect(cmd.Context(), p)
			if errors.Is(err, runtime.ErrNotRunning) {
				cmd.Println("Factory: Stopped")
				return nil
			}
			if err != nil {
				return err
			}
			uptime := max(time.Duration(0), time.Since(status.StartedAt)).Truncate(time.Second)
			cmd.Printf("Factory: Running\nPID: %d\nUptime: %s\nState: %s\n", status.PID, uptime, status.State)
			return nil
		},
	}
	stop := &cobra.Command{
		Use: "stop", Short: "Request and await controlled shutdown", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := resolve()
			if err != nil {
				return err
			}
			err = runtime.Stop(cmd.Context(), p)
			if errors.Is(err, runtime.ErrNotRunning) {
				cmd.Println("Factory is not running.")
				return nil
			}
			if err != nil {
				return err
			}
			cmd.Println("Factory stopped.")
			return nil
		},
	}
	var lines int
	var follow bool
	log := &cobra.Command{
		Use: "log", Short: "Show recent logs, optionally following new lines", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if lines < 0 {
				return fmt.Errorf("line count must not be negative")
			}
			p, err := resolve()
			if err != nil {
				return err
			}
			return logging.Show(cmd.Context(), p.LogFile(), cmd.OutOrStdout(), lines, follow)
		},
	}
	log.Flags().IntVarP(&lines, "lines", "n", 50, "Number of recent lines (0 to show only new lines with -f)")
	log.Flags().BoolVarP(&follow, "follow", "f", false, "Follow new log output until interrupted")
	daemon := &cobra.Command{
		Use: "__daemon", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runtime.Child(cmd.Context(), resolve)
		},
	}
	root.AddCommand(configuration, run, status, stop, log, daemon)
	return root
}
