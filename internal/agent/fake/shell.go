package fake

import (
	"bytes"
	"context"
	"fmt"

	"github.com/thehenrymcintosh/ship/internal/proc"
)

func runShell(ctx context.Context, dir string, env []string, script string) error {
	var out bytes.Buffer
	res, err := proc.Run(ctx, proc.Spec{Path: "bash", Args: []string{"-euo", "pipefail", "-c", script}, Dir: dir, Env: env, Stdout: &out, Stderr: &out})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, out.String())
	}
	return nil
}
