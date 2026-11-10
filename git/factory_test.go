package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shouni/go-review-kit/review"
)

func testRequestFor(repo testRepo) review.Request {
	return review.Request{
		RepoURL:    repo.path,
		Base:       repo.base,
		Head:       repo.head,
		Mode:       "detail",
		Model:      "gemini-2.5-pro",
		StorageURI: "gs://bucket/review.html",
	}
}

func TestFactoryRejectsEmptyRoot(t *testing.T) {
	if _, err := NewGoGitFactory(""); err == nil {
		t.Fatal("GoGit: want an error, got nil")
	}
	if _, err := NewCLIFactory(""); err == nil {
		t.Fatal("CLI: want an error, got nil")
	}
}

// An invalid option would keep failing on every Open, so it is rejected at creation.
func TestFactoryValidatesOptionsEagerly(t *testing.T) {
	if _, err := NewGoGitFactory(t.TempDir(), WithContextLines(-1)); err == nil {
		t.Fatal("want an error, got nil")
	}
}

func TestFactoryOpen(t *testing.T) {
	tests := []struct {
		name string
		newF func(root string, opts ...Option) (*Factory, error)
		skip func(*testing.T)
	}{
		{"GoGit", NewGoGitFactory, func(*testing.T) {}},
		{"CLI", NewCLIFactory, requireGitBinary},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.skip(t)

			repo := newTestRepo(t)
			root := t.TempDir()
			ctx := context.Background()

			factory, err := tt.newF(root, WithLogger(quietLogger()))
			if err != nil {
				t.Fatalf("failed to create the Factory: %v", err)
			}

			source, err := factory.Open(ctx, testRequestFor(repo))
			if err != nil {
				t.Fatalf("Open failed: %v", err)
			}
			t.Cleanup(func() { _ = source.Close(context.Background()) })

			// The working directory gets a name under root derived from the repository URL.
			want := filepath.Join(root, RepoDirName(repo.path))
			if _, err := os.Stat(want); err != nil {
				t.Fatalf("the expected working directory is missing (%s): %v", want, err)
			}

			diff, err := source.Diff(ctx, repo.base, repo.head)
			if err != nil {
				t.Fatalf("failed to take the diff: %v", err)
			}
			if !strings.Contains(diff, "hello") {
				t.Fatalf("diff contents do not match:\n%s", diff)
			}
		})
	}
}

// When preparation fails, the half-built working directory is not left behind.
// If it were, the next preparation would start from a broken state.
func TestFactoryOpenCleansUpOnPrepareFailure(t *testing.T) {
	root := t.TempDir()

	factory, err := NewGoGitFactory(root, WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("failed to create the Factory: %v", err)
	}

	req := review.Request{
		RepoURL:    "/nonexistent/repo.git",
		Base:       "main",
		Head:       "develop",
		Model:      "gemini-2.5-pro",
		StorageURI: "gs://bucket/review.html",
	}

	if _, err := factory.Open(context.Background(), req); err == nil {
		t.Fatal("want an error, got nil")
	}

	path := filepath.Join(root, RepoDirName(req.RepoURL))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the failed working directory is still there: %v", err)
	}
}

// A Factory injects the base ref per Request. Unlike direct creation, the caller does not need to
// think about WithBase.
func TestFactoryInjectsBase(t *testing.T) {
	requireGitBinary(t)

	repo := newTestRepo(t)
	ctx := context.Background()

	factory, err := NewCLIFactory(t.TempDir(), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("failed to create the Factory: %v", err)
	}

	source, err := factory.Open(ctx, testRequestFor(repo))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	cli, ok := source.(*CLI)
	if !ok {
		t.Fatalf("want a CLI, got %T", source)
	}
	if cli.settings.base != repo.base {
		t.Fatalf("base = %q, want %q", cli.settings.base, repo.base)
	}

	if err := source.Close(ctx); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
}

func TestFactoryUsesCustomDirNamer(t *testing.T) {
	repo := newTestRepo(t)
	root := t.TempDir()
	ctx := context.Background()

	factory, err := NewGoGitFactory(root,
		WithLogger(quietLogger()),
		WithDirNamer(func(string) string { return "fixed-name" }),
	)
	if err != nil {
		t.Fatalf("failed to create the Factory: %v", err)
	}

	source, err := factory.Open(ctx, testRequestFor(repo))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	if _, err := os.Stat(filepath.Join(root, "fixed-name")); err != nil {
		t.Fatalf("no working directory with the replaced name: %v", err)
	}
}
