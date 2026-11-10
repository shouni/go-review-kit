package git

import (
	"errors"
	"strings"
	"testing"

	"github.com/shouni/go-review-kit/review"
)

// The point is to exclude HTTP/HTTPS first. A URL with Basic auth also contains an @, so judging by
// the mere presence of @ would mistake it for the scp form.
func TestIsSSHURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"scp form", "git@github.com:shouni/example.git", true},
		{"ssh scheme", "ssh://git@github.com/shouni/example.git", true},
		{"ssh scheme without user", "ssh://github.com/shouni/example.git", true},
		{"https", "https://github.com/shouni/example.git", false},
		{"http", "http://github.com/shouni/example.git", false},
		{"https with Basic auth", "https://user@github.com/shouni/example.git", false},
		{"local path", "/tmp/repo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSSHURL(tt.input); got != tt.want {
				t.Errorf("isSSHURL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSSHUsername(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"scp form", "git@github.com:shouni/example.git", "git", false},
		{"scp form with another user", "deploy@example.com:repo.git", "deploy", false},
		{"ssh scheme", "ssh://git@github.com/shouni/example.git", "git", false},
		{"ssh scheme defaults user to git", "ssh://github.com/shouni/example.git", "git", false},
		{"colon before @", "host:path@name", "", true},
		{"no @", "github.com/shouni/example.git", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sshUsername(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("sshUsername(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSSHAuth(t *testing.T) {
	t.Run("HTTPS has no auth", func(t *testing.T) {
		auth, err := sshAuth("https://github.com/shouni/example.git", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth != nil {
			t.Fatalf("want no auth, got: %v", auth)
		}
	})

	t.Run("SSH without a key is an error", func(t *testing.T) {
		if _, err := sshAuth("git@github.com:shouni/example.git", ""); err == nil {
			t.Fatal("want an error, got nil")
		}
	})

	t.Run("key file does not exist", func(t *testing.T) {
		if _, err := sshAuth("git@github.com:shouni/example.git", "/nonexistent/id_ed25519"); err == nil {
			t.Fatal("want an error, got nil")
		}
	})

	t.Run("unsupported URL form", func(t *testing.T) {
		if _, err := sshAuth("host:path@name", "/tmp/key"); err == nil {
			t.Fatal("want an error, got nil")
		}
	})
}

// Authentication handles SSH keys only, so http(s) is refused explicitly.
//
// Without that, a private repository would be accessed anonymously and always fail, and since the
// error is git's "authentication failed", nobody could read it as a problem with the form.
func TestValidateRepoURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "scp form", input: "git@github.com:shouni/example.git"},
		{name: "ssh scheme", input: "ssh://git@github.com/shouni/example.git"},
		{name: "local path", input: "/tmp/repos/example"},
		{
			name:    "https is refused",
			input:   "https://github.com/shouni/example.git",
			wantErr: "http(s)",
		},
		{
			name:    "http is refused too",
			input:   "http://github.com/shouni/example.git",
			wantErr: "http(s)",
		},
		{
			// Refused because it cannot be told apart from a git option.
			name:    "leading hyphen is refused",
			input:   "--upload-pack=/bin/echo",
			wantErr: "option",
		},
		{name: "empty string is refused", input: "", wantErr: "empty"},
		{name: "whitespace only is refused too", input: "   ", wantErr: "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateRepoURL(tt.input)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("a URL that should pass was rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%q slipped through", tt.input)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// A form mistake can be identified by its sentinel.
//
// This lets callers separate input mistakes that retrying does not fix from failures that
// retrying may fix. The step name (StepError) says "where", but not "whether it can be fixed".
func TestValidateRepoURLIsUnsupportedSentinel(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "  ", "https://github.com/o/r.git", "--upload-pack=x"} {
		err := validateRepoURL(input)
		if err == nil {
			t.Fatalf("%q slipped through", input)
		}
		if !errors.Is(err, review.ErrUnsupportedRepoURL) {
			t.Errorf("validateRepoURL(%q) = %v, cannot be identified as ErrUnsupportedRepoURL", input, err)
		}
	}
}

// The refusal's error message does not carry credentials.
func TestValidateRepoURLRedactsCredentials(t *testing.T) {
	t.Parallel()

	err := validateRepoURL("https://user:ghp_secret@github.com/o/r.git")
	if err == nil {
		t.Fatal("https slipped through")
	}
	if strings.Contains(err.Error(), "ghp_secret") {
		t.Errorf("the error carries credentials: %v", err)
	}
}
