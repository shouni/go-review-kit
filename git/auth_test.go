package git

import (
	"errors"
	"strings"
	"testing"

	"github.com/shouni/go-review-kit/review"
)

// HTTP/HTTPS を先に除外するのが要点です。Basic 認証入りの URL にも @ が含まれるため、
// 単純な @ の有無で判定すると scp 形式と取り違えます。
func TestIsSSHURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"scp形式", "git@github.com:shouni/example.git", true},
		{"ssh スキーム", "ssh://git@github.com/shouni/example.git", true},
		{"ユーザー名なしの ssh スキーム", "ssh://github.com/shouni/example.git", true},
		{"https", "https://github.com/shouni/example.git", false},
		{"http", "http://github.com/shouni/example.git", false},
		{"Basic認証入りの https", "https://user@github.com/shouni/example.git", false},
		{"ローカルパス", "/tmp/repo", false},
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
		{"scp形式", "git@github.com:shouni/example.git", "git", false},
		{"scp形式の別ユーザー", "deploy@example.com:repo.git", "deploy", false},
		{"ssh スキーム", "ssh://git@github.com/shouni/example.git", "git", false},
		{"ssh スキームでユーザー省略時は git", "ssh://github.com/shouni/example.git", "git", false},
		{"コロンが @ より前にある", "host:path@name", "", true},
		{"@ が無い", "github.com/shouni/example.git", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sshUsername(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("エラーを期待しましたが %q が返りました", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
			if got != tt.want {
				t.Errorf("sshUsername(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSSHAuth(t *testing.T) {
	t.Run("HTTPSは認証なし", func(t *testing.T) {
		auth, err := sshAuth("https://github.com/shouni/example.git", "")
		if err != nil {
			t.Fatalf("予期しないエラー: %v", err)
		}
		if auth != nil {
			t.Fatalf("認証なしを期待しましたが: %v", auth)
		}
	})

	t.Run("SSHなのに鍵が未設定ならエラー", func(t *testing.T) {
		if _, err := sshAuth("git@github.com:shouni/example.git", ""); err == nil {
			t.Fatal("エラーを期待しましたが nil でした")
		}
	})

	t.Run("鍵ファイルが存在しない", func(t *testing.T) {
		if _, err := sshAuth("git@github.com:shouni/example.git", "/nonexistent/id_ed25519"); err == nil {
			t.Fatal("エラーを期待しましたが nil でした")
		}
	})

	t.Run("未対応のURL形式", func(t *testing.T) {
		if _, err := sshAuth("host:path@name", "/tmp/key"); err == nil {
			t.Fatal("エラーを期待しましたが nil でした")
		}
	})
}

// 認証は SSH 鍵だけを扱うため、http(s) は明示的に断ります。
//
// 断らないと、private リポジトリでは匿名アクセスになって必ず失敗し、
// しかもエラーは git 由来の「認証に失敗」なので、形式の問題だと読めません。
func TestValidateRepoURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "scp 形式", input: "git@github.com:shouni/example.git"},
		{name: "ssh スキーム", input: "ssh://git@github.com/shouni/example.git"},
		{name: "ローカルパス", input: "/tmp/repos/example"},
		{
			name:    "https は断る",
			input:   "https://github.com/shouni/example.git",
			wantErr: "http(s)",
		},
		{
			name:    "http も断る",
			input:   "http://github.com/shouni/example.git",
			wantErr: "http(s)",
		},
		{
			// git のオプションと区別できないため拒否します。
			name:    "ハイフン始まりは断る",
			input:   "--upload-pack=/bin/echo",
			wantErr: "オプション",
		},
		{name: "空文字は断る", input: "", wantErr: "空"},
		{name: "空白だけも断る", input: "   ", wantErr: "空"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateRepoURL(tt.input)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("通るはずの URL が拒否されました: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%q が素通りしました", tt.input)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("エラー %q に %q が含まれていません", err.Error(), tt.wantErr)
			}
		})
	}
}

// 形式の誤りは番兵で判別できること。
//
// 再試行しても直らない入力の誤りと、再試行で直りうる障害を呼び出し側が分けられるように
// するためです。工程名（StepError）は「どこで」を示しますが、「直せるのか」は示しません。
func TestValidateRepoURLIsUnsupportedSentinel(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "  ", "https://github.com/o/r.git", "--upload-pack=x"} {
		err := validateRepoURL(input)
		if err == nil {
			t.Fatalf("%q が素通りしました", input)
		}
		if !errors.Is(err, review.ErrUnsupportedRepoURL) {
			t.Errorf("validateRepoURL(%q) = %v, ErrUnsupportedRepoURL として判別できません", input, err)
		}
	}
}

// 断るエラーメッセージに認証情報を載せないこと。
func TestValidateRepoURLRedactsCredentials(t *testing.T) {
	t.Parallel()

	err := validateRepoURL("https://user:ghp_secret@github.com/o/r.git")
	if err == nil {
		t.Fatal("https が素通りしました")
	}
	if strings.Contains(err.Error(), "ghp_secret") {
		t.Errorf("エラーに認証情報が載っています: %v", err)
	}
}
