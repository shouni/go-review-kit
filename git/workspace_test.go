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

// readMainGo reads main.go in the working directory.
func readMainGo(t *testing.T, dir string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("failed to read main.go: %v", err)
	}
	return string(content)
}

func TestCLICheckoutHead(t *testing.T) {
	requireGitBinary(t)
	repo := newTestRepo(t)
	ctx := context.Background()

	cli, err := NewCLI(localPath(t), WithBase(repo.base), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("failed to create the CLI: %v", err)
	}
	if err := cli.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	dir, err := cli.CheckoutHead(ctx, repo.head)
	if err != nil {
		t.Fatalf("CheckoutHead failed: %v", err)
	}
	if !strings.Contains(readMainGo(t, dir), "hello") {
		t.Error("the working tree does not have the contents of head")
	}

	// Also check that Close returns to the base ref (left at head, the next Diff's assumptions break).
	if err := cli.Close(ctx); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if strings.Contains(readMainGo(t, dir), "hello") {
		t.Error("the working tree is still at head after Close")
	}
}

func TestCLICheckoutHeadRemovesUntracked(t *testing.T) {
	requireGitBinary(t)
	repo := newTestRepo(t)
	ctx := context.Background()

	cli, err := NewCLI(localPath(t), WithBase(repo.base), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("failed to create the CLI: %v", err)
	}
	if err := cli.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	// Pose as an untracked file left by a previous run that never reached Close.
	leftover := filepath.Join(cli.localPath, "leftover.txt")
	if err := os.WriteFile(leftover, []byte("stale"), 0o600); err != nil {
		t.Fatalf("failed to create the untracked file: %v", err)
	}

	if _, err := cli.CheckoutHead(ctx, repo.head); err != nil {
		t.Fatalf("CheckoutHead failed: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Error("untracked file is still there")
	}
}

func TestCLICheckoutHeadUnknownRef(t *testing.T) {
	requireGitBinary(t)
	repo := newTestRepo(t)
	ctx := context.Background()

	cli, err := NewCLI(localPath(t), WithBase(repo.base), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("failed to create the CLI: %v", err)
	}
	if err := cli.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	if _, err := cli.CheckoutHead(ctx, "no-such-ref"); !errors.Is(err, review.ErrRefNotFound) {
		t.Errorf("ErrRefNotFound was not returned: %v", err)
	}
}

func TestGoGitCheckoutHead(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	g, err := NewGoGit(localPath(t), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("failed to create the GoGit: %v", err)
	}
	if err := g.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	t.Cleanup(func() {
		if err := g.Close(context.Background()); err != nil {
			t.Errorf("Close failed: %v", err)
		}
	})

	dir, err := g.CheckoutHead(ctx, repo.head)
	if err != nil {
		t.Fatalf("CheckoutHead failed: %v", err)
	}
	if !strings.Contains(readMainGo(t, dir), "hello") {
		t.Error("the working tree does not have the contents of head")
	}
}

func TestGoGitCheckoutHeadBeforePrepare(t *testing.T) {
	g, err := NewGoGit(localPath(t), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("failed to create the GoGit: %v", err)
	}

	if _, err := g.CheckoutHead(context.Background(), "main"); err == nil {
		t.Fatal("CheckoutHead before Prepare is not an error")
	}
}

func TestGoGitCheckoutHeadUnknownRef(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	g, err := NewGoGit(localPath(t), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("failed to create the GoGit: %v", err)
	}
	if err := g.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	t.Cleanup(func() {
		if err := g.Close(context.Background()); err != nil {
			t.Errorf("Close failed: %v", err)
		}
	})

	if _, err := g.CheckoutHead(ctx, "no-such-ref"); !errors.Is(err, review.ErrRefNotFound) {
		t.Errorf("ErrRefNotFound was not returned: %v", err)
	}
}
