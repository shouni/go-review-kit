package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shouni/go-review-kit/review"
)

// cleanArgs are the git clean arguments used to clear leftovers from the working tree.
//
// -x (remove ignored files too) and a second -f (remove nested git repositories too) are needed.
// Without them, names listed in .gitignore (node_modules/, .env and so on) and vendored
// repositories survive, and the reviewer reads a previous run's leftovers as the contents of head.
var cleanArgs = []string{"clean", "-f", "-f", "-d", "-x"}

// killGrace is how long to wait for git to wind down after the context is cancelled.
//
// Without it, an ssh started by git survives holding the parent's pipes, and cmd.Run does not
// return until the pipes close. If the other end drops silently, work that was supposedly
// cancelled lingers for minutes.
const killGrace = 5 * time.Second

// CLI is a review.DiffSource implementation that calls the local git command.
//
// Close does not delete the working directory; it only returns to the base ref and cleans up, so
// later runs only need to take the diff. It is meant for environments that can keep a persistent
// working directory.
type CLI struct {
	localPath string
	settings  settings
}

// Check at compile time that the implementation satisfies the port.
var _ Source = (*CLI)(nil)

// NewCLI creates a CLI whose working directory is localPath.
func NewCLI(localPath string, opts ...Option) (*CLI, error) {
	if localPath == "" {
		return nil, fmt.Errorf("git: working directory path is empty")
	}

	s, err := newSettings(opts...)
	if err != nil {
		return nil, err
	}
	return &CLI{localPath: localPath, settings: s}, nil
}

// Prepare clones the repository (or uses it as-is if it already exists) and fetches the latest
// from the remote.
//
// Only SSH repository URLs are accepted. Three forms are accepted: the scp form
// (git@host:owner/repo.git), the ssh:// scheme (ssh://[user@]host/owner/repo.git), and a local
// path for development and tests.
//
// http(s) is refused as a form error, returning an error that wraps review.ErrUnsupportedRepoURL
// (no clone is attempted). Authentication handles SSH keys only, so http(s) has no path for
// passing credentials; it only connects anonymously to public repositories and always fails on
// private ones. Making it a form error saves the user from investigating it as an authentication
// failure.
func (c *CLI) Prepare(ctx context.Context, repoURL string) error {
	if err := validateRepoURL(repoURL); err != nil {
		return err
	}

	if err := c.cloneIfNeeded(ctx, repoURL); err != nil {
		return err
	}

	if _, err := c.run(ctx, "fetch", remoteName, "--prune"); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	return nil
}

func (c *CLI) cloneIfNeeded(ctx context.Context, repoURL string) error {
	info, err := os.Stat(c.localPath)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("working directory path is not a directory: %s", c.localPath)
		}
		if _, gitErr := os.Stat(filepath.Join(c.localPath, ".git")); gitErr != nil {
			if os.IsNotExist(gitErr) {
				return fmt.Errorf("%s exists but is not a Git repository", c.localPath)
			}
			return fmt.Errorf("check .git in %s: %w", c.localPath, gitErr)
		}
		c.settings.logger.InfoContext(ctx, "reusing existing checkout", "path", c.localPath)
		return nil

	case os.IsNotExist(err):
		return c.clone(ctx, repoURL)

	default:
		return fmt.Errorf("check working directory (%s): %w", c.localPath, err)
	}
}

func (c *CLI) clone(ctx context.Context, repoURL string) error {
	parent := filepath.Dir(c.localPath)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return fmt.Errorf("create parent directory (%s): %w", parent, err)
	}

	c.settings.logger.InfoContext(ctx, "cloning repository", "url", redactURL(repoURL), "path", c.localPath)

	// clone alone runs in the parent directory, since the working directory does not exist yet.
	// "--" is inserted to keep the URL from being interpreted as an option
	// (given a value like "--upload-pack=...", git reads it as an option).
	if _, err := c.runIn(ctx, parent, "clone", "--", repoURL, filepath.Base(c.localPath)); err != nil {
		// Leave nothing half-built. Otherwise cloneIfNeeded later decides it is "reusing existing
		// checkout", and every fetch after that keeps failing with not a git repository
		// (reviews of that repository never pass again until someone deletes it by hand).
		if rmErr := os.RemoveAll(c.localPath); rmErr != nil {
			c.settings.logger.WarnContext(ctx, "could not remove the working directory after a failed clone",
				"path", c.localPath, "error", rmErr)
		}
		return fmt.Errorf("clone: %w", err)
	}
	return nil
}

// Diff returns the three-dot diff between base and head.
func (c *CLI) Diff(ctx context.Context, base, head string) (string, error) {
	baseRef, _, err := c.resolve(ctx, base)
	if err != nil {
		return "", err
	}
	headRef, _, err := c.resolve(ctx, head)
	if err != nil {
		return "", err
	}

	c.settings.logger.InfoContext(ctx, "taking diff", "base", baseRef, "head", headRef)

	return c.run(ctx,
		"diff",
		fmt.Sprintf("%s...%s", baseRef, headRef),
		fmt.Sprintf("--unified=%d", c.settings.contextLines),
	)
}

// CheckoutHead force-checks head out into the working tree and returns its path.
// It implements review.DiffSource.
//
// Untracked files are removed after the checkout too. The working directory is reused across
// runs, so this keeps the reviewer from reading, as the contents of head, leftovers from a
// previous run that crashed before reaching Close.
func (c *CLI) CheckoutHead(ctx context.Context, head string) (string, error) {
	headRef, isBranch, err := c.resolve(ctx, head)
	if err != nil {
		return "", fmt.Errorf("resolve head ref: %w", err)
	}

	if err := c.forceCheckout(ctx, headRef, isBranch); err != nil {
		return "", fmt.Errorf("check out head: %w", err)
	}

	if _, err := c.run(ctx, cleanArgs...); err != nil {
		return "", fmt.Errorf("clean after head checkout: %w", err)
	}

	c.settings.logger.InfoContext(ctx, "checked out head", "path", c.localPath, "head", headRef)
	return c.localPath, nil
}

// Close forces the working tree back to the base ref and then removes untracked files.
// The working directory itself is kept, to be reused by the next run.
//
// ★ Clean first, fetch afterwards. With network I/O at the head of the cleanup, if the connection
// had dropped or the deadline was used up at the end of the review, the working tree would carry
// over to the next run still at head, with its untracked files still there.
// The fetch is best-effort, to keep the next ref resolution fresh, so the cleanup itself succeeds
// even when it fails.
func (c *CLI) Close(ctx context.Context) error {
	// checkout does not remove untracked files, so drop them first.
	if _, err := c.run(ctx, cleanArgs...); err != nil {
		return fmt.Errorf("clean on cleanup: %w", err)
	}

	if err := c.checkoutBase(ctx); err != nil {
		return err
	}

	if _, err := c.run(ctx, "fetch", remoteName); err != nil {
		c.settings.logger.WarnContext(ctx, "fetch on cleanup failed (cleaning has completed)",
			"path", c.localPath, "error", err)
	}

	c.settings.logger.InfoContext(ctx, "cleanup completed", "path", c.localPath, "base", c.settings.base)
	return nil
}

func (c *CLI) checkoutBase(ctx context.Context) error {
	if c.settings.base == "" {
		// Through a Factory, Request.Base is always set, so this is reached only when created directly.
		// There is no ref to return to, so only clean.
		c.settings.logger.WarnContext(ctx, "base ref is not set, skipping checkout")
		return nil
	}

	baseRef, isBranch, err := c.resolve(ctx, c.settings.base)
	if err != nil {
		return fmt.Errorf("resolve base ref on cleanup: %w", err)
	}

	if err := c.forceCheckout(ctx, baseRef, isBranch); err != nil {
		return fmt.Errorf("check out on cleanup: %w", err)
	}
	return nil
}

// forceCheckout forcibly switches the working tree to a resolved ref.
//
// -f discards local changes, so that the next run always starts from a clean state even if a
// previous run crashed partway.
func (c *CLI) forceCheckout(ctx context.Context, ref string, isBranch bool) error {
	args := []string{"checkout", "-f", ref}
	if isBranch {
		args = []string{"checkout", "-f", "-B", localBranchName(ref), ref}
	}
	_, err := c.run(ctx, args...)
	return err
}

// resolve resolves a ref string as a remote branch or a commit.
func (c *CLI) resolve(ctx context.Context, ref string) (resolved string, isBranch bool, err error) {
	if strings.TrimSpace(ref) == "" {
		return "", false, fmt.Errorf("%w: ref name is empty", review.ErrRefNotFound)
	}

	for _, candidate := range refCandidates(ref) {
		if _, err := c.run(ctx, "rev-parse", "--verify", "--quiet", candidate.ref+"^{commit}"); err == nil {
			return candidate.ref, candidate.isBranch, nil
		}
	}
	return "", false, fmt.Errorf("%w: %s", review.ErrRefNotFound, ref)
}

// run runs a git command in the working directory and returns its standard output.
func (c *CLI) run(ctx context.Context, args ...string) (string, error) {
	return c.runIn(ctx, c.localPath, args...)
}

// runIn runs a git command in the given directory and returns its standard output.
//
// The point is to capture standard output and standard error separately. Capturing them together
// would mix git's warnings (such as the detached HEAD notice) into the diff body and pass them
// straight to the AI.
func (c *CLI) runIn(ctx context.Context, dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // args are built by this package, and refs are already validated by resolve
	cmd.Dir = dir
	cmd.Env = c.env()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Even if a child process lingers after cancellation, cut it off here (see the comment on killGrace).
	cmd.WaitDelay = killGrace

	c.settings.logger.DebugContext(ctx, "running git command", "dir", dir, "args", args)

	if err := cmd.Run(); err != nil {
		// On failure git prints the whole URL to stderr, so any credentials in it would flow
		// straight into the caller's logs and notifications as part of the error.
		message := redactURL(strings.TrimSpace(stderr.String()))

		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			c.settings.logger.DebugContext(ctx, "git command failed",
				"args", args, "exit", exitErr.ExitCode(), "stderr", message)
			return "", fmt.Errorf("git %s exited with code %d: %s: %w",
				strings.Join(args, " "), exitErr.ExitCode(), message, err)
		}
		return "", fmt.Errorf("run git %s: %w", strings.Join(args, " "), err)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// env returns the environment with a GIT_SSH_COMMAND naming the SSH key added.
func (c *CLI) env() []string {
	env := os.Environ()
	if c.settings.sshKeyPath == "" {
		return env
	}

	// GIT_SSH_COMMAND is interpreted through a shell, so the path is quoted before embedding.
	// -F /dev/null ignores ssh_config so the result does not depend on per-environment settings.
	// Host key verification is left to ssh's default known_hosts.
	sshCommand := fmt.Sprintf("ssh -i %s -F /dev/null", quotePathForShell(c.settings.sshKeyPath))
	return append(env, "GIT_SSH_COMMAND="+sshCommand)
}
