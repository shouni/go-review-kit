package git

import (
	"testing"
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
