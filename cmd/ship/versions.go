package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/history"
	gitws "github.com/thehenrymcintosh/ship/internal/workspace/git"
)

func (a *app) diffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <pipeline> <version> [version]",
		Short: "Show what changed between two versions of a pipeline (or a version and the files now)",
		Long: `Compare two versions' saved copies of the pipeline and the skills, rules and
scripts it uses. With one version, compare it with the files as they are now.
Versions are v1, v2… (see ` + "`" + brand.Name + ` pipeline versions` + "`" + `) or a hash prefix.`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, hs, now, err := a.pipelineRead(cmd, args[0])
			if err != nil {
				return err
			}
			tmp, err := os.MkdirTemp("", "ship-diff-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp)
			side := func(ref string) (string, error) {
				if ref == "" {
					return "now", history.MaterializeParts(now.Parts, filepath.Join(tmp, "now"))
				}
				v, err := hs.Find(ref)
				if err != nil {
					return "", fail(exitNotFound, "%v", err)
				}
				label := fmt.Sprintf("v%d", v.Version)
				if !v.Frozen {
					fmt.Println(a.color("33", fmt.Sprintf("%s was recorded before ship kept copies of every file, so some of it can't be compared.", label)))
				}
				return label, history.Materialize(*v, filepath.Join(tmp, label))
			}
			left, err := side(args[1])
			if err != nil {
				return err
			}
			right := ""
			if len(args) == 3 {
				right = args[2]
			}
			if right, err = side(right); err != nil {
				return err
			}
			if left == right {
				return fail(exitUser, "that's the same version twice")
			}
			gargs := []string{"diff", "--no-index", "--src-prefix=", "--dst-prefix="}
			if !a.noColor && isTTY(os.Stdout) {
				gargs = append(gargs, "--color=always")
			}
			c := exec.Command("git", append(gargs, left, right)...)
			c.Dir = tmp
			out, _ := c.Output()
			if len(out) == 0 {
				fmt.Printf("%s and %s are the same.\n", left, right)
				return nil
			}
			os.Stdout.Write(out)
			return nil
		},
	}
}

func (a *app) restoreCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "restore <pipeline> <version>",
		Short: "Put a pipeline, and the skills, rules and scripts it uses, back as they were in a version",
		Long: `Write a version's saved copies of the pipeline and everything it uses back to
where they live now (or, for files since removed, where they lived then).
Files that have uncommitted changes are left alone unless you pass --force,
as are files outside the repo (such as your own ~/.claude skills, or what a
symlink in a skill points at), which git can't bring back. Symlinks are
followed, never removed. Parts added since the version are left as they are. The
files as they were before the restore are recorded as a version first, so
you can restore them too.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			e, repo, hs, _, err := a.pipelineCtx(cmd, name)
			if err != nil {
				return err
			}
			v, err := hs.Find(args[1])
			if err != nil {
				return fail(exitNotFound, "%v", err)
			}
			l := e.Loader(repo)
			closure, err := l.Closure(name)
			if err != nil {
				return err
			}
			fp := e.Fingerprint(repo, closure)
			steps, skipped := history.PlanRestore(*v, fp, func(p string) string {
				return filepath.Join(l.Dir(name), p+".yml")
			})
			var blocked []string
			for _, st := range steps {
				if st.Same || force {
					continue
				}
				// Through symlinks too: what they point at gets written.
				for _, p := range st.Touches() {
					if why := a.unsafeToOverwrite(repo, p); why != "" {
						blocked = append(blocked, fmt.Sprintf("%s (%s)", a.show(repo, p), why))
					}
				}
			}
			if len(blocked) > 0 {
				return fail(exitConflict, "not restoring %s v%d; these would lose changes:\n  %s\nCommit or stash them, or pass --force.", name, v.Version, strings.Join(blocked, "\n  "))
			}
			changed := 0
			for _, st := range steps {
				if st.Same {
					continue
				}
				if err := st.Apply(); err != nil {
					return err
				}
				changed++
				fmt.Printf("  restored %s %s → %s\n", st.Part.Kind, st.Part.Name, a.show(repo, st.To))
			}
			for _, s := range skipped {
				fmt.Println(a.color("33", "  skipped  "+s))
			}
			if changed == 0 {
				fmt.Printf("%s already matches v%d.\n", name, v.Version)
				return nil
			}
			closure, err = e.Loader(repo).Closure(name)
			if err == nil {
				now := e.Fingerprint(repo, closure)
				if now.Hash == v.Hash {
					fmt.Printf("\n%s is back to v%d. Review and commit the changes; runs started from then on are v%d again.\n", name, v.Version, v.Version)
				} else {
					fmt.Printf("\n%s is as in v%d except: %s.\n", name, v.Version, strings.Join(history.Changed(v.Parts, now.Parts), "; "))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite files with uncommitted changes, and files outside the repo")
	return cmd
}

// unsafeToOverwrite says why replacing path could lose work, or "".
func (a *app) unsafeToOverwrite(repo, path string) string {
	if _, err := os.Stat(path); err != nil {
		return "" // nothing there to lose
	}
	if repo == "" {
		return "outside a repo"
	}
	rel, err := filepath.Rel(repo, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		// path may be a real location (symlinks resolved): try the repo's.
		if real, rerr := filepath.EvalSymlinks(repo); rerr == nil {
			rel, err = filepath.Rel(real, path)
		}
	}
	if err != nil || strings.HasPrefix(rel, "..") {
		return "outside the repo"
	}
	out, err := gitws.Git(context.Background(), repo, "status", "--porcelain", "--", rel)
	if err != nil {
		return "can't tell if it has changes"
	}
	if strings.TrimSpace(out) != "" {
		return "uncommitted changes"
	}
	return ""
}
