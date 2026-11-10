package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/shouni/go-review-kit/review"
)

// GoGit is a review.DiffSource implementation on go-git.
//
// It needs no git binary. Close deletes the whole working directory, which suits throwaway
// environments that clone afresh on every run.
//
// It is meant for a single run (Prepare → Diff → Close) and is not designed for concurrent use.
type GoGit struct {
	localPath string
	settings  settings

	repo *gogit.Repository
	auth transport.AuthMethod
}

// Check at compile time that the implementation satisfies the port.
var _ Source = (*GoGit)(nil)

// NewGoGit creates a GoGit whose working directory is localPath.
func NewGoGit(localPath string, opts ...Option) (*GoGit, error) {
	if localPath == "" {
		return nil, fmt.Errorf("git: working directory path is empty")
	}

	s, err := newSettings(opts...)
	if err != nil {
		return nil, err
	}
	return &GoGit{localPath: localPath, settings: s}, nil
}

// Prepare clones the repository (or opens it if it already exists) and fetches the latest from
// the remote.
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
func (g *GoGit) Prepare(ctx context.Context, repoURL string) error {
	if err := validateRepoURL(repoURL); err != nil {
		return err
	}

	auth, err := sshAuth(repoURL, g.settings.sshKeyPath)
	if err != nil {
		return fmt.Errorf("build credentials: %w", err)
	}
	g.auth = auth

	if err := g.cloneOrOpen(ctx, repoURL); err != nil {
		return err
	}
	return g.fetch(ctx)
}

func (g *GoGit) cloneOrOpen(ctx context.Context, repoURL string) error {
	_, err := os.Stat(g.localPath)
	switch {
	case os.IsNotExist(err):
		g.settings.logger.InfoContext(ctx, "cloning repository", "url", repoURL, "path", g.localPath)
		repo, cloneErr := gogit.PlainCloneContext(ctx, g.localPath, false, &gogit.CloneOptions{
			URL:          repoURL,
			SingleBranch: false,
			Auth:         g.auth,
		})
		if cloneErr != nil {
			return fmt.Errorf("clone: %w", cloneErr)
		}
		g.repo = repo
		return nil

	case err == nil:
		repo, openErr := gogit.PlainOpen(g.localPath)
		if openErr != nil {
			return fmt.Errorf("open existing repository: %w", openErr)
		}
		g.settings.logger.InfoContext(ctx, "opened existing repository", "path", g.localPath)
		g.repo = repo
		return nil

	default:
		return fmt.Errorf("check working directory (%s): %w", g.localPath, err)
	}
}

func (g *GoGit) fetch(ctx context.Context) error {
	g.settings.logger.InfoContext(ctx, "fetching latest from remote", "remote", remoteName)

	err := g.repo.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName: remoteName,
		Auth:       g.auth,
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/*:refs/remotes/" + remoteName + "/*")},
		Tags:       gogit.AllTags,
		Progress:   io.Discard,
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch: %w", err)
	}
	return nil
}

// Diff returns the diff from the merge base of base and head up to head.
func (g *GoGit) Diff(ctx context.Context, base, head string) (string, error) {
	if g.repo == nil {
		return "", fmt.Errorf("git: Prepare has not completed")
	}

	baseCommit, err := g.commit(base)
	if err != nil {
		return "", err
	}
	headCommit, err := g.commit(head)
	if err != nil {
		return "", err
	}

	// For a three-dot comparison, the merge base is the starting point of the diff. This keeps
	// commits that advanced on the base side out of the diff, so only changes made on head are
	// reviewed.
	mergeBases, err := baseCommit.MergeBase(headCommit)
	if err != nil {
		return "", fmt.Errorf("find merge base: %w", err)
	}
	if len(mergeBases) == 0 {
		return "", fmt.Errorf("no common ancestor found for %s and %s", base, head)
	}

	baseTree, err := mergeBases[0].Tree()
	if err != nil {
		return "", fmt.Errorf("get base tree: %w", err)
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		return "", fmt.Errorf("get head tree: %w", err)
	}

	changes, err := object.DiffTreeWithOptions(ctx, baseTree, headTree, object.DefaultDiffTreeOptions)
	if err != nil {
		return "", fmt.Errorf("compute diff: %w", err)
	}

	patch, err := changes.PatchContext(ctx)
	if err != nil {
		return "", fmt.Errorf("generate patch: %w", err)
	}
	return patch.String(), nil
}

// CheckoutHead force-checks head out into the working tree and returns its path.
// It implements review.DiffSource.
//
// go-git's checkout cannot take a context, so ctx has no effect on cancellation and is used only
// for logging (the signature is the port's common one).
func (g *GoGit) CheckoutHead(ctx context.Context, head string) (string, error) {
	if g.repo == nil {
		return "", fmt.Errorf("git: Prepare has not completed")
	}

	hash, err := g.resolve(head)
	if err != nil {
		return "", fmt.Errorf("resolve head ref: %w", err)
	}

	worktree, err := g.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("get worktree: %w", err)
	}

	// Force discards local changes in the working tree. Right after cloning it still holds the
	// default branch's contents, and what the reviewer reads must always be the state of head.
	if err := worktree.Checkout(&gogit.CheckoutOptions{Hash: hash, Force: true}); err != nil {
		return "", fmt.Errorf("check out head (%s): %w", head, err)
	}

	g.settings.logger.InfoContext(ctx, "checked out head", "path", g.localPath, "head", head)
	return g.localPath, nil
}

// Close deletes the working directory.
func (g *GoGit) Close(ctx context.Context) error {
	g.settings.logger.InfoContext(ctx, "removing working directory", "path", g.localPath)
	g.repo = nil

	if err := os.RemoveAll(g.localPath); err != nil {
		return fmt.Errorf("remove working directory (%s): %w", g.localPath, err)
	}
	return nil
}

// commit resolves a ref string to a commit object.
func (g *GoGit) commit(ref string) (*object.Commit, error) {
	hash, err := g.resolve(ref)
	if err != nil {
		return nil, err
	}

	commit, err := g.repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("get commit (%s): %w", ref, err)
	}
	return commit, nil
}

// resolve resolves a ref string to a commit hash.
// When it is not found, it returns an error wrapping review.ErrRefNotFound.
//
// ★ It distinguishes "no candidate existed" from "reading failed". Folding everything into
// ErrRefNotFound would surface repository corruption and disk failures as "ref not found: main",
// and the sentinel itself would lie. When it hits errors other than absence, it returns the first.
func (g *GoGit) resolve(ref string) (plumbing.Hash, error) {
	if ref == "" {
		return plumbing.ZeroHash, fmt.Errorf("%w: ref name is empty", review.ErrRefNotFound)
	}

	// readErr is the first error hit other than absence.
	var readErr error
	keep := func(err error) {
		// Absence is a reason to "try the next candidate", not a failure.
		if errors.Is(err, plumbing.ErrReferenceNotFound) || errors.Is(err, plumbing.ErrObjectNotFound) {
			return
		}
		if readErr == nil {
			readErr = err
		}
	}

	for _, candidate := range refCandidates(ref) {
		if candidate.isBranch {
			name := plumbing.NewRemoteReferenceName(remoteName, localBranchName(candidate.ref))
			// Use resolved=true. With false, a symbolic ref (origin/HEAD and the like) makes
			// Hash() return ZeroHash, and an empty hash passes as the resolution result with no
			// error.
			reference, err := g.repo.Reference(name, true)
			if err != nil {
				keep(err)
				continue
			}
			if reference.Hash().IsZero() {
				continue
			}
			return reference.Hash(), nil
		}

		// Appending ^{commit} peels annotated tags down to their commit as well.
		hash, err := g.repo.ResolveRevision(plumbing.Revision(candidate.ref + "^{commit}"))
		if err != nil {
			keep(err)
			hash, err = g.repo.ResolveRevision(plumbing.Revision(candidate.ref))
			if err != nil {
				keep(err)
				continue
			}
		}
		if _, err := g.repo.CommitObject(*hash); err != nil {
			keep(err)
			continue
		}
		return *hash, nil
	}

	if readErr != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve ref (%s): %w", ref, readErr)
	}
	return plumbing.ZeroHash, fmt.Errorf("%w: %s", review.ErrRefNotFound, ref)
}
