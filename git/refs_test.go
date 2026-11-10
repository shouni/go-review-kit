package git

import (
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// Refs always resolve in the order "remote branch → commit". In the reverse order, a digits-only
// branch named after a ticket number would resolve as an abbreviated hash, and the diff of an
// unintended commit would be taken.
func TestRefCandidates(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []refCandidate
	}{
		{
			name:  "plain branch name",
			input: "develop",
			want: []refCandidate{
				{ref: "origin/develop", isBranch: true},
				{ref: "develop", isBranch: false},
			},
		},
		{
			name:  "hex-looking input still prefers the branch",
			input: "f9211119e3",
			want: []refCandidate{
				{ref: "origin/f9211119e3", isBranch: true},
				{ref: "f9211119e3", isBranch: false},
			},
		},
		{
			name:  "digits-only branch name",
			input: "12345",
			want: []refCandidate{
				{ref: "origin/12345", isBranch: true},
				{ref: "12345", isBranch: false},
			},
		},
		{
			name:  "origin/ prefix is a branch only",
			input: "origin/main",
			want:  []refCandidate{{ref: "origin/main", isBranch: true}},
		},
		{
			name:  "surrounding whitespace is ignored",
			input: "  main  ",
			want: []refCandidate{
				{ref: "origin/main", isBranch: true},
				{ref: "main", isBranch: false},
			},
		},
		{
			name:  "empty string has no candidates",
			input: "   ",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := refCandidates(tt.input)

			if len(got) != len(tt.want) {
				t.Fatalf("candidate count = %d, want %d (%+v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("candidate[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLocalBranchName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"origin/main", "main"},
		{"origin/release/v1", "release/v1"},
		{"main", "main"},
	}

	for _, tt := range tests {
		if got := localBranchName(tt.input); got != tt.want {
			t.Errorf("localBranchName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// GIT_SSH_COMMAND is interpreted through a shell, so unless single quotes in the path are escaped,
// a command can be injected.
func TestQuotePathForShell(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain path", "/home/user/.ssh/id_ed25519", `'/home/user/.ssh/id_ed25519'`},
		{"with a space", "/tmp/my key", `'/tmp/my key'`},
		{"with a single quote", "/tmp/it's", `'/tmp/it'\''s'`},
		{"command injection attempt", "/tmp/k'; rm -rf /; '", `'/tmp/k'\''; rm -rf /; '\'''`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quotePathForShell(tt.input); got != tt.want {
				t.Errorf("quotePathForShell(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExpandTilde(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Skipf("cannot get user information: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no tilde is unchanged", "/etc/ssh/key", "/etc/ssh/key"},
		{"relative path is unchanged", "keys/id_rsa", "keys/id_rsa"},
		{"tilde-user is not expanded", "~foo/key", "~foo/key"},
		{"expands tilde", "~/.ssh/id_ed25519", filepath.Join(current.HomeDir, ".ssh/id_ed25519")},
		{"empty string is unchanged", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandTilde(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("expandTilde(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// On failure git prints the whole URL to stderr, so any credentials in it would flow into logs and
// notifications through the error message. This checks that they are masked.
func TestRedactURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "https with token",
			input: "fatal: repository 'https://user:ghp_secret@github.com/o/r/' not found",
			want:  "fatal: repository 'https://***@github.com/o/r/' not found",
		},
		{
			name:  "user name only",
			input: "https://someone@example.com/o/r",
			want:  "https://***@example.com/o/r",
		},
		{
			name:  "no credentials is unchanged",
			input: "https://github.com/o/r",
			want:  "https://github.com/o/r",
		},
		{
			// The scp form carries no credentials, so it is left as is.
			name:  "scp form is unchanged",
			input: "git@github.com:o/r.git",
			want:  "git@github.com:o/r.git",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := redactURL(tt.input); got != tt.want {
				t.Errorf("redactURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// clean removes ignored files and nested git repositories too.
// If they survived, the reviewer would read the previous run's leftovers as the contents of head.
func TestCleanArgsRemoveIgnoredAndNested(t *testing.T) {
	t.Parallel()

	joined := strings.Join(cleanArgs, " ")
	for _, want := range []string{"clean", "-d", "-x"} {
		if !strings.Contains(joined, want) {
			t.Errorf("clean arguments lack %q: %v", want, cleanArgs)
		}
	}
	// Removing nested git repositories takes two -f flags.
	if n := strings.Count(joined, "-f"); n < 2 {
		t.Errorf("there are %d -f flags; removing nested git repositories takes 2: %v", n, cleanArgs)
	}
}
