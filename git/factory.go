package git

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/shouni/go-review-kit/review"
)

// Source is the shape the adapters in this package satisfy:
// review.DiffSource plus Prepare, which makes the repository ready for use.
type Source interface {
	review.DiffSource
	// Prepare clones the repository (or reuses an existing one) and fetches the latest from the remote.
	Prepare(ctx context.Context, repoURL string) error
}

// Factory is a review.DiffSourceFactory that picks a working directory per Request and opens a
// Source there.
//
// The working directory is root/<a name derived from the repository URL>. The same repository
// always yields the same path, so the CLI implementation can reuse its checkout.
//
// # Concurrent runs on the same repository
//
// The default naming depends on the URL alone, so reviews of the same repository running at the
// same time share one working directory. This package takes no lock. When it is shared:
//
//   - GoGit: the Close of whichever finishes first deletes the working directory of the one still
//     running.
//   - CLI: one checks out another branch while the other is reading, so it reviews the other
//     branch's contents without any error (a breakage that is hard to notice).
//
// To review the same repository concurrently within one process, return a different name per run
// with WithDirNamer (for example, append a run ID to the result of RepoDirName).
// You give up reusing the checkout, but in a throwaway environment it never worked anyway.
type Factory struct {
	root      string
	settings  settings
	opts      []Option
	newSource func(localPath string, opts ...Option) (Source, error)
}

// Check at compile time that the implementation satisfies the port.
var _ review.DiffSourceFactory = (*Factory)(nil)

// NewGoGitFactory creates a Factory that opens GoGit sources.
func NewGoGitFactory(root string, opts ...Option) (*Factory, error) {
	return newFactory(root, opts, func(localPath string, o ...Option) (Source, error) {
		return NewGoGit(localPath, o...)
	})
}

// NewCLIFactory creates a Factory that opens CLI sources.
func NewCLIFactory(root string, opts ...Option) (*Factory, error) {
	return newFactory(root, opts, func(localPath string, o ...Option) (Source, error) {
		return NewCLI(localPath, o...)
	})
}

func newFactory(root string, opts []Option, newSource func(string, ...Option) (Source, error)) (*Factory, error) {
	if root == "" {
		return nil, fmt.Errorf("git: working directory root is empty")
	}

	// Validate the settings here, so Open does not keep failing with the same error every time.
	s, err := newSettings(opts...)
	if err != nil {
		return nil, err
	}

	return &Factory{root: root, settings: s, opts: opts, newSource: newSource}, nil
}

// Open prepares a Source in the working directory for the Request and returns it.
func (f *Factory) Open(ctx context.Context, req review.Request) (review.DiffSource, error) {
	localPath := filepath.Join(f.root, f.settings.dirNamer(req.RepoURL))

	// The ref to return to on cleanup differs per Request, so it is appended to the options given at creation.
	opts := append(slices.Clone(f.opts), WithBase(req.Base))

	source, err := f.newSource(localPath, opts...)
	if err != nil {
		return nil, err
	}

	if err := source.Prepare(ctx, req.RepoURL); err != nil {
		// Leaving a half-built working directory behind would start the next preparation from a broken state.
		// A failure of the cleanup itself is not fatal here, so it is only logged.
		if closeErr := source.Close(context.WithoutCancel(ctx)); closeErr != nil {
			f.settings.logger.WarnContext(ctx, "cleanup after a failed prepare failed", "error", closeErr)
		}
		return nil, err
	}

	return source, nil
}
