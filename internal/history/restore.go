package history

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Materialize writes a version's saved copies to dest, laid out as
// <kind>/<name> (see CopyRel), for diffing. Parts without a copy are left
// out.
func Materialize(v Version, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, p := range v.Parts {
		if p.Missing {
			continue
		}
		files, err := v.Contents(p)
		if err != nil {
			continue
		}
		if err := writeContents(files, filepath.Join(dest, filepath.FromSlash(CopyRel(p))), isDirPart(p)); err != nil {
			return err
		}
	}
	return nil
}

// MaterializeParts copies parts as they are now (from their Paths) to
// dest, in the same layout as Materialize.
func MaterializeParts(parts []Part, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, p := range parts {
		if !p.Missing && p.Path != "" {
			if err := copyPart(p.Path, filepath.Join(dest, filepath.FromSlash(CopyRel(p)))); err != nil {
				return err
			}
		}
	}
	return nil
}

// RestoreStep is one file or skill folder a restore writes.
type RestoreStep struct {
	Part  Part
	To    string // where it goes now
	Dir   bool   // a skill folder: replaced as a whole
	Same  bool   // already identical: nothing to write
	files map[string][]byte
}

// PlanRestore works out where each of v's parts goes: where that part
// lives now (in current, the pipeline's fingerprint as it is), else where
// it lived when v was recorded (resolved against current.Roots).
// pipelineDest places a pipeline file that no longer exists. Parts that
// can't be restored are described in skipped.
func PlanRestore(v Version, current Fingerprint, pipelineDest func(name string) string) (steps []RestoreStep, skipped []string) {
	now := map[string]Part{}
	for _, p := range current.Parts {
		now[p.Kind+" "+p.Name] = p
	}
	for _, p := range v.Parts {
		if p.Missing {
			continue
		}
		files, err := v.Contents(p)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s %s: no saved copy (recorded before ship kept copies)", p.Kind, p.Name))
			continue
		}
		to := ""
		if c, ok := now[p.Kind+" "+p.Name]; ok && c.Path != "" {
			to = c.Path
		} else if p.Kind == KindPipeline && pipelineDest != nil {
			to = pipelineDest(p.Name)
		} else if p.Location != "" {
			to = Resolve(current.Roots, p.Location)
		}
		if to == "" {
			skipped = append(skipped, fmt.Sprintf("%s %s: don't know where it goes now", p.Kind, p.Name))
			continue
		}
		steps = append(steps, RestoreStep{Part: p, To: to, Dir: isDirPart(p), Same: currentHash(to) == p.Hash, files: files})
	}
	return steps, skipped
}

// Apply writes one restore step.
func (r RestoreStep) Apply() error {
	if r.Same {
		return nil
	}
	return writeContents(r.files, r.To, r.Dir)
}

// writeContents writes a part's files (see Version.Contents) to dest: a
// file, or for a dir, its files, removing any others (but not dotfiles,
// which aren't part of a version). Files keep their mode when they exist;
// new ones are executable when they start with #!.
func writeContents(files map[string][]byte, dest string, dir bool) error {
	if !dir {
		return writeKeepingMode(dest, files[""])
	}
	for rel, b := range files {
		if err := writeKeepingMode(filepath.Join(dest, filepath.FromSlash(rel)), b); err != nil {
			return err
		}
	}
	return filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dest {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dest, p)
		if _, ok := files[filepath.ToSlash(rel)]; !ok {
			return os.Remove(p)
		}
		return nil
	})
}

func writeKeepingMode(path string, b []byte) error {
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	} else if bytes.HasPrefix(b, []byte("#!")) {
		mode = 0o755
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, mode)
}
