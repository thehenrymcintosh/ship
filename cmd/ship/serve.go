package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/daemon"
)

func (a *app) serveCmd() *cobra.Command {
	var port int
	var detach, restart, force bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon (API, web UI and run engine) in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if restart {
				return a.restartDaemon(force, port)
			}
			if detach {
				if c, err := daemon.Connect(a.home); err == nil {
					fmt.Printf("already running at %s (pid %d)\n", c.Info.URL(), c.Info.PID)
					return nil
				}
				var extra []string
				if port != 0 {
					extra = []string{"--port", fmt.Sprint(port)}
				}
				if err := daemon.SpawnDetached(a.home, extra); err != nil {
					return err
				}
				c, err := waitForDaemon(a.home, 5*time.Second)
				if err != nil {
					return err
				}
				fmt.Printf("started at %s (pid %d)\n", c.Info.URL(), c.Info.PID)
				return nil
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
			slog.SetDefault(log)
			err := daemon.Serve(context.Background(), daemon.Options{Home: a.home, Port: port, Log: log})
			if errors.Is(err, daemon.ErrAlreadyRunning) {
				return fail(exitConflict, "%v (see `%s open`)", err, brand.Name)
			}
			return err
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "port (default: config ui.port, 7420, or the next free one)")
	cmd.Flags().BoolVar(&detach, "detach", false, "start in the background (logs to ~/"+brand.Dir+"/daemon.log)")
	cmd.Flags().BoolVar(&restart, "restart", false, "restart once no agent/run step is executing")
	cmd.Flags().BoolVar(&force, "force", false, "with --restart: interrupt running steps")
	return cmd
}

func waitForDaemon(home string, d time.Duration) (*daemon.Client, error) {
	deadline := time.Now().Add(d)
	for {
		c, err := daemon.Connect(home)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (a *app) restartDaemon(force bool, port int) error {
	c, err := daemon.Connect(a.home)
	if err == nil {
		var res struct {
			Executing int `json:"executing"`
		}
		if err := c.Do("POST", "/api/shutdown", map[string]bool{"force": force, "restart": true}, &res); err != nil {
			return err
		}
		if res.Executing > 0 && !force {
			fmt.Printf("waiting for %d running step(s) to finish (use --force to interrupt them)…\n", res.Executing)
		}
		for daemon.Running(a.home) {
			time.Sleep(200 * time.Millisecond)
		}
	}
	var extra []string
	if port != 0 {
		extra = []string{"--port", fmt.Sprint(port)}
	}
	if err := daemon.SpawnDetached(a.home, extra); err != nil {
		return err
	}
	c, err = waitForDaemon(a.home, 5*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("restarted at %s (pid %d, %s)\n", c.Info.URL(), c.Info.PID, c.Info.Version)
	return nil
}

func (a *app) openCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open [run]",
		Short: "Open the web UI (at a run, if given)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/"
			if len(args) == 1 {
				id, err := a.resolveRun(args[0])
				if err != nil {
					return err
				}
				path = "/runs/" + id
			}
			c, err := daemon.Ensure(a.home)
			if err != nil {
				return err
			}
			url := c.UIURL(path)
			openBrowser(url)
			fmt.Println(c.Info.URL() + path)
			return nil
		},
	}
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("%s %s\n", brand.Name, brand.Version)
			if c, err := daemon.Connect(a.home); err == nil {
				fmt.Printf("daemon %s at %s (pid %d)\n", c.Info.Version, c.Info.URL(), c.Info.PID)
			}
		},
	}
}
