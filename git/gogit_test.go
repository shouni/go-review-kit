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

func newGoGitForTest(t *testing.T, path string) *GoGit {
	t.Helper()

	source, err := NewGoGit(path, WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("failed to create the GoGit: %v", err)
	}
	return source
}

func TestNewGoGitRejectsEmptyPath(t *testing.T) {
	if _, err := NewGoGit(""); err == nil {
		t.Fatal("want an error, got nil")
	}
}

func TestGoGitPrepareAndDiff(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newGoGitForTest(t, localPath(t))
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

	// Between the same ref the diff is empty, and the pipeline decides to skip.
	empty, err := source.Diff(ctx, repo.base, repo.base)
	if err != nil {
		t.Fatalf("failed to take the diff: %v", err)
	}
	if strings.TrimSpace(empty) != "" {
		t.Fatalf("the diff is not empty:\n%s", empty)
	}
}

func TestGoGitPrepareReusesExistingCheckout(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	path := localPath(t)

	first := newGoGitForTest(t, path)
	if err := first.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("first prepare failed: %v", err)
	}

	// For a path that is already cloned, opening and fetching are enough.
	second := newGoGitForTest(t, path)
	if err := second.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("second prepare failed: %v", err)
	}
	if _, err := second.Diff(ctx, repo.base, repo.head); err != nil {
		t.Fatalf("failed to take the diff: %v", err)
	}
}

func TestGoGitDiffResolvesRefs(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newGoGitForTest(t, localPath(t))
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	// With or without an explicit origin/, it resolves to the same ref.
	explicit, err := source.Diff(ctx, "origin/"+repo.base, "origin/"+repo.head)
	if err != nil {
		t.Fatalf("failed to take the diff: %v", err)
	}
	implicit, err := source.Diff(ctx, repo.base, repo.head)
	if err != nil {
		t.Fatalf("failed to take the diff: %v", err)
	}
	if explicit != implicit {
		t.Error("the diff changes depending on origin/")
	}

	// A commit hash can be given directly too.
	hash, err := source.resolve(repo.head)
	if err != nil {
		t.Fatalf("failed to resolve the ref: %v", err)
	}
	if _, err := source.Diff(ctx, repo.base, hash.String()); err != nil {
		t.Fatalf("failed to take the diff by hash: %v", err)
	}
}

func TestGoGitDiffUnknownRef(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newGoGitForTest(t, localPath(t))
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
		{"empty ref", repo.base, ""},
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

func TestGoGitDiffBeforePrepare(t *testing.T) {
	source := newGoGitForTest(t, localPath(t))

	if _, err := source.Diff(context.Background(), "main", "develop"); err == nil {
		t.Fatal("want an error, got nil")
	}
}

// GoGit is meant to be thrown away, so Close deletes the whole working directory.
func TestGoGitCloseRemovesWorkdir(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	path := localPath(t)

	source := newGoGitForTest(t, path)
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the clone target does not exist: %v", err)
	}

	if err := source.Close(ctx); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the working directory is still there: %v", err)
	}

	// Close on a path that does not exist counts as success (cleanup should be idempotent).
	if err := source.Close(ctx); err != nil {
		t.Fatalf("second cleanup failed: %v", err)
	}
}

func TestGoGitPrepareFailsForUnknownRepo(t *testing.T) {
	source := newGoGitForTest(t, localPath(t))

	if err := source.Prepare(context.Background(), "/nonexistent/repo.git"); err == nil {
		t.Fatal("want an error, got nil")
	}
}

// A symbolic ref does not pass as an empty hash.
//
// Looking up a Reference with resolved=false makes Hash() return ZeroHash for a symbolic ref such
// as origin/HEAD, and an empty hash becomes the resolution result with no error.
func TestGoGitResolveSkipsSymbolicRef(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source, err := NewGoGit(filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("failed to create the GoGit: %v", err)
	}
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	hash, err := source.resolve("main")
	if err != nil {
		t.Fatalf("could not resolve main: %v", err)
	}
	if hash.IsZero() {
		t.Error("an empty hash was returned as the resolution result")
	}
}

// A ref that does not exist can be identified as ErrRefNotFound.
func TestGoGitResolveReportsNotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source, err := NewGoGit(filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("failed to create the GoGit: %v", err)
	}
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}

	if _, err := source.resolve("no-such-ref"); !errors.Is(err, review.ErrRefNotFound) {
		t.Errorf("err = %v, cannot be identified as ErrRefNotFound", err)
	}
}
