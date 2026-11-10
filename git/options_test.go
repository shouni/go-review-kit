package git

import (
	"log/slog"
	"strings"
	"testing"
)

func TestNewSettingsDefaults(t *testing.T) {
	s, err := newSettings()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s.contextLines != DefaultContextLines {
		t.Errorf("contextLines = %d, want %d", s.contextLines, DefaultContextLines)
	}
	if s.logger == nil {
		t.Error("logger is nil")
	}
	if s.dirNamer == nil {
		t.Error("dirNamer is nil")
	}
	if s.sshKeyPath != "" || s.base != "" {
		t.Errorf("defaults are not empty: %+v", s)
	}
}

func TestOptions(t *testing.T) {
	t.Run("sets values", func(t *testing.T) {
		s, err := newSettings(
			WithSSHKey("/tmp/id_ed25519"),
			WithBase("main"),
			WithContextLines(3),
			WithLogger(slog.Default()),
			WithDirNamer(func(string) string { return "fixed" }),
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if s.sshKeyPath != "/tmp/id_ed25519" {
			t.Errorf("sshKeyPath = %q", s.sshKeyPath)
		}
		if s.base != "main" {
			t.Errorf("base = %q", s.base)
		}
		if s.contextLines != 3 {
			t.Errorf("contextLines = %d", s.contextLines)
		}
		if s.dirNamer("anything") != "fixed" {
			t.Error("dirNamer was not replaced")
		}
	})

	t.Run("negative context lines is an error", func(t *testing.T) {
		if _, err := newSettings(WithContextLines(-1)); err == nil {
			t.Fatal("want an error, got nil")
		}
	})

	t.Run("nil dirNamer is an error", func(t *testing.T) {
		if _, err := newSettings(WithDirNamer(nil)); err == nil {
			t.Fatal("want an error, got nil")
		}
	})

	t.Run("nil logger is ignored", func(t *testing.T) {
		s, err := newSettings(WithLogger(nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s.logger == nil {
			t.Error("overwritten with nil")
		}
	})
}

// The same repository URL must always yield the same directory name,
// because the CLI implementation reuses its checkout under that name.
func TestRepoDirName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"scp form", "git@github.com:shouni/example.git", "github.com-shouni-example"},
		{"ssh scheme", "ssh://git@github.com/shouni/example.git", "github.com-shouni-example"},
		{"https", "https://github.com/shouni/example.git", "github.com-shouni-example"},
		{"without .git", "https://github.com/shouni/example", "github.com-shouni-example"},
		{"local path", "/tmp/repos/example", "tmp-repos-example"},
		{"surrounding whitespace", "  https://github.com/shouni/example.git  ", "github.com-shouni-example"},
		{"empty string", "", "repo"},
		{"symbols only", "///", "repo"},
	}

	// The name is "readable part + short hash of the URL". The hash prevents collisions between
	// different repositories, and its value itself is not checked here.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RepoDirName(tt.input)
			if !strings.HasPrefix(got, tt.want+"-") {
				t.Errorf("RepoDirName(%q) = %q, want prefix %q", tt.input, got, tt.want+"-")
			}
			if len(got) != len(tt.want)+1+8 {
				t.Errorf("RepoDirName(%q) = %q, the hash length is not as expected", tt.input, got)
			}
		})
	}
}

func TestRepoDirNameIsStable(t *testing.T) {
	// The same repository written in different ways lands in the same working directory.
	urls := []string{
		"git@github.com:shouni/example.git",
		"ssh://git@github.com/shouni/example.git",
		"https://github.com/shouni/example.git",
	}

	first := RepoDirName(urls[0])
	for _, url := range urls[1:] {
		if got := RepoDirName(url); got != first {
			t.Errorf("RepoDirName(%q) = %q, want %q", url, got, first)
		}
	}
}

// An "@" in the path is not mistaken for credentials.
//
// Mistaking it yields a name with the host thrown away, pointing at the same working directory as
// another repository. When different repositories fight over one directory, one repository's
// branch is published as the other's review result.
func TestRepoDirNameKeepsPathAtSign(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "keeps host with @ in path",
			input: "https://github.com/shouni/example@v2",
			want:  "github.com-shouni-example-v2",
		},
		{
			name:  "drops credentials",
			input: "https://user:token@github.com/shouni/example.git",
			want:  "github.com-shouni-example",
		},
		{
			name:  "scp form",
			input: "git@github.com:shouni/example.git",
			want:  "github.com-shouni-example",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RepoDirName(tt.input); !strings.HasPrefix(got, tt.want+"-") {
				t.Errorf("RepoDirName(%q) = %q, want prefix %q", tt.input, got, tt.want+"-")
			}
		})
	}
}

// Similar but different URLs do not get the same directory name (collisions with subgroups).
func TestRepoDirNameDistinguishesSimilarPaths(t *testing.T) {
	t.Parallel()

	a := RepoDirName("https://gitlab.com/group/sub/proj")
	b := RepoDirName("https://gitlab.com/group/sub-proj")
	if a == b {
		t.Errorf("different repositories get the same name: %q", a)
	}
}
