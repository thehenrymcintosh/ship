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
	// The same files the fingerprint sees (through symlinks), but a
	// symlink itself is never removed: it's the user's, not the version's.
	for _, rel := range treeFiles(dest) {
		if _, ok := files[rel]; ok {
			continue
		}
		p := filepath.Join(dest, filepath.FromSlash(rel))
		if st, err := os.Lstat(p); err == nil && st.Mode()&fs.ModeSymlink == 0 {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// Touches lists where applying r writes or removes files: r.To, plus the
// real location of every one of them that a symlink puts somewhere other
// than under r.To (a linked skill, or a linked dir or file inside one), so
// the caller can check those for unsaved work too.
func (r RestoreStep) Touches() []string {
	out := []string{r.To}
	if r.Same {
		return out
	}
	base := r.To
	if parent, err := filepath.EvalSymlinks(filepath.Dir(r.To)); err == nil {
		base = filepath.Join(parent, filepath.Base(r.To))
	}
	rels := []string{""}
	if r.Dir {
		rels = treeFiles(r.To)
		for rel := range r.files {
			rels = append(rels, rel)
		}
	}
	seen := map[string]bool{}
	for _, rel := range rels {
		real := realPath(filepath.Join(r.To, filepath.FromSlash(rel)))
		if in, err := filepath.Rel(base, real); err == nil && in != ".." && !strings.HasPrefix(in, ".."+string(filepath.Separator)) {
			continue
		}
		if !seen[real] {
			seen[real] = true
			out = append(out, real)
		}
	}
	return out
}

// realPath resolves the symlinks in p, which needn't exist yet: its
// nearest existing ancestor is resolved and the rest kept.
func realPath(p string) string {
	rest := ""
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
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
