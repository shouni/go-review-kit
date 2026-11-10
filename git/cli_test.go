package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shouni/go-review-kit/review"
)

func newCLIForTest(t *testing.T, path string, opts ...Option) *CLI {
	t.Helper()
	requireGitBinary(t)

	source, err := NewCLI(path, append([]Option{WithLogger(quietLogger())}, opts...)...)
	if err != nil {
		t.Fatalf("failed to create the CLI: %v", err)
	}
	return source
}

func TestNewCLIRejectsEmptyPath(t *testing.T) {
	if _, err := NewCLI(""); err == nil {
		t.Fatal("want an error, got nil")
	}
}

func TestCLIPrepareAndDiff(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newCLIForTest(t, localPath(t), WithBase(repo.base))
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	diff, err := source.Diff(ctx, repo.base, repo.head)
	if err != nil {
		t.Fatalf("failed to take the diff: %v", err)
	}
	if !strings.Contains(diff, "hello") {
		t.Fatalf("the diff does not contain the head-side change:\n%s", diff)
	}

	empty, err := source.Diff(ctx, repo.base, repo.base)
	if err != nil {
		t.Fatalf("failed to take the diff: %v", err)
	}
	if strings.TrimSpace(empty) != "" {
		t.Fatalf("the diff is not empty:\n%s", empty)
	}
}

// Checks that git's warnings do not get mixed into the diff body. Capturing standard output and
// standard error together would put the warnings straight into the prompt for the AI.
func TestCLIDiffContainsOnlyPatch(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newCLIForTest(t, localPath(t), WithBase(repo.base))
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	diff, err := source.Diff(ctx, repo.base, repo.head)
	if err != nil {
		t.Fatalf("failed to take the diff: %v", err)
	}
	if !strings.HasPrefix(diff, "diff --git") {
		t.Fatalf("the diff does not start with a patch:\n%s", diff)
	}
}

func TestCLIDiffUnknownRef(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newCLIForTest(t, localPath(t), WithBase(repo.base))
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	tests := []struct {
		name string
		base string
		head string
	}{
		{"head does not exist", repo.base, "no-such-branch"},
		{"base does not exist", "no-such-branch", repo.head},
		{"empty ref", repo.base, "  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := source.Diff(ctx, tt.base, tt.head)
			if !errors.Is(err, review.ErrRefNotFound) {
				t.Fatalf("want ErrRefNotFound, got: %v", err)
			}
		})
	}
}

// The CLI reuses its checkout, so Close does not delete the working directory; it returns to the
// base ref and clears untracked files.
func TestCLICloseRestoresCleanCheckout(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	path := localPath(t)

	source := newCLIForTest(t, path, WithBase(repo.base))
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	// Plant a modification to a tracked file and an added untracked file.
	tracked := filepath.Join(path, "main.go")
	if err := os.WriteFile(tracked, []byte("broken content\n"), 0o600); err != nil {
		t.Fatalf("failed to write the file: %v", err)
	}
	untracked := filepath.Join(path, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("junk\n"), 0o600); err != nil {
		t.Fatalf("failed to write the file: %v", err)
	}

	if err := source.Close(ctx); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the working directory is gone: %v", err)
	}
	if _, err := os.Stat(untracked); !os.IsNotExist(err) {
		t.Fatalf("untracked file is still there: %v", err)
	}
	content, err := os.ReadFile(tracked) //nolint:gosec // a temporary path inside the test
	if err != nil {
		t.Fatalf("failed to read the file: %v", err)
	}
	if strings.Contains(string(content), "broken content") {
		t.Fatal("the modified contents were not restored")
	}

	// After the cleanup, the next diff can be taken right away.
	if _, err := source.Diff(ctx, repo.base, repo.head); err != nil {
		t.Fatalf("failed to take the diff after cleanup: %v", err)
	}
}

// When the base ref is not set, the checkout is skipped and only cleaning is done.
func TestCLICloseWithoutBase(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	path := localPath(t)

	source := newCLIForTest(t, path)
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	untracked := filepath.Join(path, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("junk\n"), 0o600); err != nil {
		t.Fatalf("failed to write the file: %v", err)
	}

	if err := source.Close(ctx); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(untracked); !os.IsNotExist(err) {
		t.Fatalf("untracked file is still there: %v", err)
	}
}

func TestCLIPrepareRejectsNonRepository(t *testing.T) {
	requireGitBinary(t)

	t.Run("directory that is not a Git repository", func(t *testing.T) {
		dir := t.TempDir()
		source := newCLIForTest(t, dir)

		err := source.Prepare(context.Background(), newTestRepo(t).path)
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		if !strings.Contains(err.Error(), "is not a Git repository") {
			t.Fatalf("the error does not convey the reason: %v", err)
		}
	})

	t.Run("path that is not a directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatalf("failed to create the file: %v", err)
		}

		source := newCLIForTest(t, file)
		if err := source.Prepare(context.Background(), newTestRepo(t).path); err == nil {
			t.Fatal("want an error, got nil")
		}
	})
}

func TestCLIEnvIncludesSSHCommand(t *testing.T) {
	t.Run("not added without a key", func(t *testing.T) {
		source, err := NewCLI("/tmp/repo")
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}
		for _, kv := range source.env() {
			if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") {
				t.Fatalf("GIT_SSH_COMMAND is set: %s", kv)
			}
		}
	})

	t.Run("key path is quoted", func(t *testing.T) {
		source, err := NewCLI("/tmp/repo", WithSSHKey("/tmp/it's key"))
		if err != nil {
			t.Fatalf("failed to create: %v", err)
		}

		var got string
		for _, kv := range source.env() {
			if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") {
				got = kv
			}
		}

		want := `GIT_SSH_COMMAND=ssh -i '/tmp/it'\''s key' -F /dev/null`
		if got != want {
			t.Fatalf("GIT_SSH_COMMAND = %q, want %q", got, want)
		}
	})
}
