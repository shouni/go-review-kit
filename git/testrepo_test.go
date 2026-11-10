package git

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// testRepo is a repository that can be used as a clone source.
type testRepo struct {
	// path is the repository's path. It can be passed as-is as the clone source URL.
	path string
	// base is the name of the branch holding only the initial commit.
	base string
	// head is the name of the branch one commit ahead of base.
	head string
}

const (
	testBaseBranch = "main"
	testHeadBranch = "feature"
)

// newTestRepo creates a repository with two branches, base and head.
// Only head has changes, and base is its ancestor.
func newTestRepo(t *testing.T) testRepo {
	t.Helper()

	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to initialize the repository: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get the worktree: %v", err)
	}

	commit := func(content, message string) plumbing.Hash {
		t.Helper()

		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0o600); err != nil {
			t.Fatalf("failed to write the file: %v", err)
		}
		if _, err := worktree.Add("main.go"); err != nil {
			t.Fatalf("failed to stage: %v", err)
		}
		hash, err := worktree.Commit(message, &gogit.CommitOptions{
			Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()},
		})
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}
		return hash
	}

	initial := commit("package main\n\nfunc main() {}\n", "initial commit")

	// The default branch name varies by environment, so set it explicitly.
	if err := worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(testBaseBranch),
		Hash:   initial,
		Create: true,
	}); err != nil {
		t.Fatalf("failed to create the base branch: %v", err)
	}

	if err := worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(testHeadBranch),
		Hash:   initial,
		Create: true,
	}); err != nil {
		t.Fatalf("failed to create the head branch: %v", err)
	}
	commit("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n", "add hello")

	// Put the clone source's HEAD back on base.
	if err := worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(testBaseBranch),
	}); err != nil {
		t.Fatalf("failed to return to the base branch: %v", err)
	}

	return testRepo{path: dir, base: testBaseBranch, head: testHeadBranch}
}

// localPath returns the path of a clone target that does not exist yet.
func localPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "checkout")
}

// quietLogger is a logger that keeps test output clean.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// requireGitBinary skips the test in environments without the git command.
func requireGitBinary(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("skipping: git command not found")
	}
}
