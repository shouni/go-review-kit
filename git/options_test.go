package git

import (
	"log/slog"
	"testing"
)

func TestNewSettingsDefaults(t *testing.T) {
	s, err := newSettings()
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}

	if s.contextLines != DefaultContextLines {
		t.Errorf("contextLines = %d, want %d", s.contextLines, DefaultContextLines)
	}
	if s.logger == nil {
		t.Error("logger が nil です")
	}
	if s.dirNamer == nil {
		t.Error("dirNamer が nil です")
	}
	if s.sshKeyPath != "" || s.base != "" {
		t.Errorf("既定値が空ではありません: %+v", s)
	}
}

func TestOptions(t *testing.T) {
	t.Run("値を設定する", func(t *testing.T) {
		s, err := newSettings(
			WithSSHKey("/tmp/id_ed25519"),
			WithBase("main"),
			WithContextLines(3),
			WithLogger(slog.Default()),
			WithDirNamer(func(string) string { return "fixed" }),
		)
		if err != nil {
			t.Fatalf("予期しないエラー: %v", err)
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
		if s.dirNamer("なんでも") != "fixed" {
			t.Error("dirNamer が差し替わっていません")
		}
	})

	t.Run("負の前後行数はエラー", func(t *testing.T) {
		if _, err := newSettings(WithContextLines(-1)); err == nil {
			t.Fatal("エラーを期待しましたが nil でした")
		}
	})

	t.Run("nil の dirNamer はエラー", func(t *testing.T) {
		if _, err := newSettings(WithDirNamer(nil)); err == nil {
			t.Fatal("エラーを期待しましたが nil でした")
		}
	})

	t.Run("nil のロガーは無視される", func(t *testing.T) {
		s, err := newSettings(WithLogger(nil))
		if err != nil {
			t.Fatalf("予期しないエラー: %v", err)
		}
		if s.logger == nil {
			t.Error("nil で上書きされています")
		}
	})
}

// 同じリポジトリURLからは常に同じディレクトリ名が得られる必要があります。
// CLI 実装はこの名前でチェックアウトを再利用するためです。
func TestRepoDirName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"scp形式", "git@github.com:shouni/example.git", "github.com-shouni-example"},
		{"ssh スキーム", "ssh://git@github.com/shouni/example.git", "github.com-shouni-example"},
		{"https", "https://github.com/shouni/example.git", "github.com-shouni-example"},
		{".git なし", "https://github.com/shouni/example", "github.com-shouni-example"},
		{"ローカルパス", "/tmp/repos/example", "tmp-repos-example"},
		{"前後の空白", "  https://github.com/shouni/example.git  ", "github.com-shouni-example"},
		{"空文字", "", "repo"},
		{"記号だけ", "///", "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RepoDirName(tt.input); got != tt.want {
				t.Errorf("RepoDirName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRepoDirNameIsStable(t *testing.T) {
	// 同じリポジトリを違う書き方で指定しても、同じ作業ディレクトリへ落ち着きます。
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
