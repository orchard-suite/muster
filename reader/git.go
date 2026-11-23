package reader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitTimeout bounds one git call, so a stuck repository never holds a read.
const gitTimeout = 60 * time.Second

// gitEnv is the environment every git call runs with. A read takes no
// optional lock, so `git status` never refreshes the index under a commit
// running at the same moment; git never asks for a password; and its output
// is in one language to parse.
func gitEnv() []string {
	return append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}

// git runs one git command in dir and answers its standard output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	cmd.Stdin = nil
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", args[0], ctx.Err())
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			reason, _, _ := strings.Cut(strings.TrimSpace(errOut.String()), "\n")
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimPrefix(reason, "fatal: "))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return out.String(), nil
}

// lines splits output into its non-empty lines.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}
