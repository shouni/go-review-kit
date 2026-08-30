package git

import (
	"log/slog"
	"strings"
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

	// 名前は「読める部分 + URL の短いハッシュ」です。ハッシュは別リポジトリの
	// 衝突を防ぐためのもので、値そのものはここでは検証しません。
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RepoDirName(tt.input)
			if !strings.HasPrefix(got, tt.want+"-") {
				t.Errorf("RepoDirName(%q) = %q, want prefix %q", tt.input, got, tt.want+"-")
			}
			if len(got) != len(tt.want)+1+8 {
				t.Errorf("RepoDirName(%q) = %q, ハッシュの長さが想定と違います", tt.input, got)
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

// パス側の "@" を認証情報と誤認しないこと。
//
// 誤認するとホスト名ごと捨てた名前になり、別のリポジトリと同じ作業ディレクトリを
// 指します。同じディレクトリを別リポジトリが奪い合うと、片方のブランチの内容が
// もう片方のレビュー結果として公開されます。
func TestRepoDirNameKeepsPathAtSign(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "パスに @ があってもホストを保つ",
			input: "https://github.com/shouni/example@v2",
			want:  "github.com-shouni-example-v2",
		},
		{
			name:  "認証情報は落とす",
			input: "https://user:token@github.com/shouni/example.git",
			want:  "github.com-shouni-example",
		},
		{
			name:  "scp 形式",
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

// 似て非なる URL が同じディレクトリ名にならないこと（サブグループ運用での衝突）。
func TestRepoDirNameDistinguishesSimilarPaths(t *testing.T) {
	t.Parallel()

	a := RepoDirName("https://gitlab.com/group/sub/proj")
	b := RepoDirName("https://gitlab.com/group/sub-proj")
	if a == b {
		t.Errorf("別リポジトリが同じ名前になります: %q", a)
	}
}
