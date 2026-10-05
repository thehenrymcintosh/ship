package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/daemon"
	"github.com/thehenrymcintosh/ship/internal/update"
)

func (a *app) updateCmd() *cobra.Command {
	var check, force bool
	var version string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update " + brand.Name + " to the latest release (and restart the daemon)",
		Long: `Download the latest release from github.com/` + brand.Repo + ` for this OS and
architecture, verify its checksum, replace this binary, and restart a running
daemon so it uses the new version.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			u := update.New()
			tag := version
			if tag == "" {
				var err error
				if tag, err = u.Latest(ctx); err != nil {
					if errors.Is(err, update.ErrNoRelease) {
						return fail(exitUser, "github.com/%s has no published releases yet", brand.Repo)
					}
					return err
				}
			} else if !strings.HasPrefix(tag, "v") {
				tag = "v" + tag
			}
			current := brand.Version
			newer := update.Newer(tag, current)
			if check {
				if newer {
					fmt.Printf("%s is available (you have %s). Run `%s update`.\n", tag, current, brand.Name)
				} else {
					fmt.Printf("%s %s is up to date.\n", brand.Name, current)
				}
				return nil
			}
			if !newer && !force && version == "" {
				fmt.Printf("%s %s is up to date.\n", brand.Name, current)
				return nil
			}

			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			fmt.Printf("Downloading %s…\n", u.AssetName(tag))
			bin, err := u.Download(ctx, tag)
			if err != nil {
				return err
			}
			if err := update.Replace(exe, bin); err != nil {
				return fail(exitUser, "%v\nReinstall with: curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sh", err, brand.Repo)
			}
			fmt.Printf("Updated %s %s → %s (%s)\n", brand.Name, current, strings.TrimPrefix(tag, "v"), exe)

			// The new binary carries the new skills; refresh unedited copies.
			sk := exec.Command(exe, "--home", a.home, "refresh-skills")
			sk.Stdout, sk.Stderr = os.Stdout, os.Stderr
			if err := sk.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "Couldn't update the skills (%v); run `%s init` in each repo.\n", err, brand.Name)
			}

			// A running daemon keeps the old code until it restarts.
			if daemon.Running(a.home) {
				fmt.Println("Restarting the daemon (it waits for running agent/script steps to finish)…")
				r := exec.Command(exe, "--home", a.home, "serve", "--restart")
				r.Stdout, r.Stderr = os.Stdout, os.Stderr
				if err := r.Run(); err != nil {
					return fmt.Errorf("restarting the daemon: %w (run `%s serve --restart`)", err, brand.Name)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether an update is available")
	cmd.Flags().StringVar(&version, "version", "", "install this release (e.g. v0.2.0) instead of the latest")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already up to date")
	return cmd
}
