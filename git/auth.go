package git

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/shouni/go-review-kit/review"
)

// sshAuth builds the go-git auth method for a repository URL.
//
// For anything not in SSH form it returns no auth (nil). Only local paths get here (validateRepoURL
// refuses http(s) first).
func sshAuth(repoURL, sshKeyPath string) (transport.AuthMethod, error) {
	if !isSSHURL(repoURL) {
		return nil, nil
	}

	username, err := sshUsername(repoURL)
	if err != nil {
		return nil, err
	}

	keyPath, err := expandTilde(sshKeyPath)
	if err != nil {
		return nil, fmt.Errorf("expand SSH key path: %w", err)
	}
	if keyPath == "" {
		return nil, fmt.Errorf("repository URL is SSH but no SSH key is configured: %s", repoURL)
	}

	key, err := os.ReadFile(keyPath) //nolint:gosec // the key path is set by the caller
	if err != nil {
		return nil, fmt.Errorf("read SSH key file (%s): %w", keyPath, err)
	}

	// Assumes a key without a passphrase.
	// HostKeyCallback is not set; it is left to go-git's default known_hosts verification
	// (SSH_KNOWN_HOSTS, or else ~/.ssh/known_hosts and /etc/ssh/ssh_known_hosts).
	auth, err := ssh.NewPublicKeys(username, key, "")
	if err != nil {
		return nil, fmt.Errorf("load SSH auth key: %w", err)
	}
	return auth, nil
}

// isSSHURL reports whether a repository URL is in SSH form.
//
// http(s) is excluded first because a URL with credentials (https://user@host/...) contains an @
// and would be mistaken for the scp form. validateRepoURL refuses it first, so this is normally not
// reached, but the function still judges correctly on its own.
func isSSHURL(repoURL string) bool {
	if strings.HasPrefix(repoURL, "http://") || strings.HasPrefix(repoURL, "https://") {
		return false
	}
	return strings.HasPrefix(repoURL, "ssh://") || strings.Contains(repoURL, "@")
}

// sshUsername extracts the SSH user name from a repository URL.
func sshUsername(repoURL string) (string, error) {
	if strings.HasPrefix(repoURL, "ssh://") {
		u, err := url.Parse(repoURL)
		if err != nil {
			return "", fmt.Errorf("parse repository URL: %w", err)
		}
		if u.User != nil && u.User.Username() != "" {
			return u.User.Username(), nil
		}
		return "git", nil
	}

	// The scp form (user@host:path). Only accepted when an @ comes before the colon.
	at := strings.Index(repoURL, "@")
	colon := strings.Index(repoURL, ":")
	if at > 0 && colon > at {
		return repoURL[:at], nil
	}

	return "", fmt.Errorf("unsupported SSH repository URL form: %s", repoURL)
}

// validateRepoURL checks that the repository URL is one this package can handle.
//
// Authentication handles SSH keys only (WithSSHKey → GIT_SSH_COMMAND / go-git's PublicKeys).
// http(s) has no path for passing credentials and only connects anonymously to public
// repositories, so it would "look supported, yet always fail on private ones". It is refused
// explicitly here so the user can fix the form.
//
// Accepted are the SSH forms (git@host:owner/repo.git, ssh://[user@]host/path) and local paths.
// Local paths are for development and tests.
func validateRepoURL(repoURL string) error {
	trimmed := strings.TrimSpace(repoURL)
	if trimmed == "" {
		return fmt.Errorf("%w: repository URL is empty", review.ErrUnsupportedRepoURL)
	}

	// A value starting with "-" can be read by git as an option, so it is refused.
	// No legitimate repository URL takes this form.
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("%w: a value starting with %q cannot be told apart from an option", review.ErrUnsupportedRepoURL, "-")
	}

	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return fmt.Errorf(
			"%w: http(s) is not supported (authentication handles SSH keys only); "+
				"use the git@host:owner/repo.git or ssh://host/owner/repo.git form: %s",
			review.ErrUnsupportedRepoURL, redactURL(trimmed))
	}

	return nil
}
