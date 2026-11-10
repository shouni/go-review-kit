package git

import (
	"fmt"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
)

// refCandidate is a resolution candidate for a ref string.
type refCandidate struct {
	// ref is the string actually tried for resolution.
	ref string
	// isBranch reports whether the candidate is a remote-tracking branch.
	// It decides whether cleanup should check it out as a branch.
	isBranch bool
}

// refCandidates returns the resolution candidates for a ref string, remote branches first.
//
// The point is to always try "remote branch → commit" in that order. In the reverse order, a
// digits-only branch named after a ticket number (e.g. 12345) would resolve as an abbreviated hash
// and the diff of an unintended commit would be taken.
func refCandidates(ref string) []refCandidate {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return nil
	}

	// When it already has origin/, interpret it only as a branch.
	if strings.HasPrefix(trimmed, remotePrefix) {
		return []refCandidate{{ref: trimmed, isBranch: true}}
	}

	return []refCandidate{
		{ref: remotePrefix + trimmed, isBranch: true},
		{ref: trimmed, isBranch: false},
	}
}

// remotePrefix is the prefix of remote-tracking branches.
const remotePrefix = remoteName + "/"

// remoteName is the name of the remote used.
const remoteName = "origin"

// localBranchName extracts the local branch name from a resolved remote-tracking branch name.
func localBranchName(ref string) string {
	return strings.TrimPrefix(ref, remotePrefix)
}

// quotePathForShell wraps a path to be embedded in a shell command string in single quotes.
// Single quotes inside the path are escaped too, which prevents command injection.
func quotePathForShell(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// expandTilde expands a leading ~/ to the home directory.
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(current.HomeDir, path[len("~/"):]), nil
}

// redactURL masks the credentials of URLs contained in a string.
//
// On failure git prints the whole URL to stderr, so a repository given as
// `https://user:token@host/...` ends up in logs and error messages, token and all. Errors also
// flow into notifications as the review.Result message, so the credentials are dropped here.
func redactURL(s string) string {
	return credentialsInURL.ReplaceAllString(s, "://***@")
}

// credentialsInURL matches the userinfo part of a URL (scheme://user:pass@).
// The scp form (git@host:path) carries no credentials, so it is not matched.
var credentialsInURL = regexp.MustCompile(`://[^/@\s]+@`)
