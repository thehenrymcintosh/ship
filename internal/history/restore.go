package history

import (
	"fmt"
	"os"
	"path/filepath"
)

// Materialize copies a version's saved files to dest, laid out as
// <kind>/<name> (see CopyRel), for diffing.
func Materialize(v Version, dest string) error {
	src := filepath.Join(v.Dir, filesDir)
	if _, err := os.Stat(src); err != nil {
		return os.MkdirAll(dest, 0o755)
	}
	return copyPart(src, dest)
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
	Part Part
	From string // the version's copy
	To   string // where it goes now
	Dir  bool   // a skill folder: replaced as a whole
	Same bool   // already identical: nothing to write
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
		if p.Copy == "" {
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
		from := filepath.Join(v.Dir, filepath.FromSlash(p.Copy))
		st, err := os.Stat(from)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s %s: %v", p.Kind, p.Name, err))
			continue
		}
		steps = append(steps, RestoreStep{Part: p, From: from, To: to, Dir: st.IsDir(), Same: currentHash(to) == p.Hash})
	}
	return steps, skipped
}

// Apply writes one restore step.
func (r RestoreStep) Apply() error {
	if r.Same {
		return nil
	}
	if r.Dir {
		if err := os.RemoveAll(r.To); err != nil {
			return err
		}
	}
	return copyPart(r.From, r.To)
}
