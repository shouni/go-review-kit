// Package git provides two Git implementations of review.DiffSource.
//
//   - GoGit: a pure Go implementation on go-git. It needs no git binary and discards the working
//     directory on every run, which suits throwaway environments such as serverless.
//   - CLI: an implementation that calls the local git command. It reuses the checkout, so taking a
//     diff is fast, which suits local development and CI where a persistent working directory is
//     possible.
//
// The caller chooses which to use (this package does not choose).
package git

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
)

// DefaultContextLines is the default number of context lines the CLI implementation passes to git
// diff.
const DefaultContextLines = 10

// settings holds the settings shared by both adapters.
type settings struct {
	sshKeyPath   string
	base         string
	logger       *slog.Logger
	contextLines int
	dirNamer     func(repoURL string) string
}

func newSettings(opts ...Option) (settings, error) {
	s := settings{
		logger:       slog.Default(),
		contextLines: DefaultContextLines,
		dirNamer:     RepoDirName,
	}
	for _, opt := range opts {
		// Callers that build options conditionally tend to mix in nil.
		// It is not a mistake worth panicking over, so skip it.
		if opt == nil {
			continue
		}
		if err := opt(&s); err != nil {
			return settings{}, err
		}
	}
	return s, nil
}

// Option is an optional setting of an adapter or a Factory.
//
// Options apply only at creation, and an adapter's settings never change afterwards. If a setter
// could rewrite them later, the settings could change mid-run. Options return an error so that
// values needing validation can be handled.
type Option func(*settings) error

// WithSSHKey sets the path of the private key used for SSH authentication.
// When unset, no authentication is done for SSH repository URLs.
func WithSSHKey(path string) Option {
	return func(s *settings) error {
		s.sshKeyPath = path
		return nil
	}
}

// WithBase sets the base ref to return to on cleanup (used by the CLI implementation).
// When used through a Factory, Request.Base is set automatically.
func WithBase(ref string) Option {
	return func(s *settings) error {
		s.base = ref
		return nil
	}
}

// WithLogger replaces the logger the adapter uses.
func WithLogger(logger *slog.Logger) Option {
	return func(s *settings) error {
		if logger != nil {
			s.logger = logger
		}
		return nil
	}
}

// WithContextLines sets the number of context lines included in the diff (CLI implementation
// only). A negative value is an error.
func WithContextLines(n int) Option {
	return func(s *settings) error {
		if n < 0 {
			return fmt.Errorf("git: context lines must not be negative: %d", n)
		}
		s.contextLines = n
		return nil
	}
}

// WithDirNamer replaces the function that decides the working directory name from the repository
// URL (Factory only).
func WithDirNamer(fn func(repoURL string) string) Option {
	return func(s *settings) error {
		if fn == nil {
			return fmt.Errorf("git: dirNamer must not be nil")
		}
		s.dirNamer = fn
		return nil
	}
}

// RepoDirName is the default implementation that builds a working directory name from a
// repository URL.
//
// It drops the scheme, the credentials and a trailing .git, and folds awkward characters such as
// path separators into hyphens. The same URL always yields the same name, so the CLI
// implementation can reuse its checkout.
func RepoDirName(repoURL string) string {
	name := strings.TrimSpace(repoURL)
	if idx := strings.Index(name, "://"); idx >= 0 {
		name = name[idx+len("://"):]
	}
	// Credentials appear only before the host, so only an "@" before the first "/" is considered.
	// Searching the whole string with LastIndex or Index would mistake an "@" in the path (e.g.
	// owner/repo@v2) for credentials, yielding a name like "v2" with the host thrown away, which
	// collides with other repositories.
	if slash := strings.Index(name, "/"); slash != 0 {
		host := name
		if slash > 0 {
			host = name[:slash]
		}
		if at := strings.Index(host, "@"); at >= 0 {
			name = name[at+1:]
		}
	}
	name = strings.TrimSuffix(name, ".git")

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	cleaned := strings.Trim(b.String(), "-.")
	if cleaned == "" {
		cleaned = "repo"
	}

	// Folding alone gives different repositories the same name
	// (group/sub/proj and group-sub-proj both become group-sub-proj).
	// When different repositories fight over one directory, one repository's branch is published
	// as the other's review result. A short hash of the pre-folding form is appended to tell them
	// apart.
	//
	// Before hashing, the scp form's ":" is turned into "/". This keeps the property that the
	// same repository lands in the same working directory whether it is given as
	// git@host:owner/repo or https://host/owner/repo.
	sum := sha256.Sum256([]byte(strings.ReplaceAll(name, ":", "/")))
	return cleaned + "-" + hex.EncodeToString(sum[:])[:8]
}
