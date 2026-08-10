package git

import (
	"os/user"
	"path/filepath"
	"testing"
)

// 参照はつねに「リモートブランチ → コミット」の順で解決します。逆順にすると、
// チケット番号をそのままブランチ名にした数字だけのブランチが短縮ハッシュとして
// 解決され、意図しないコミットの差分を取ってしまいます。
func TestRefCandidates(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []refCandidate
	}{
		{
			name:  "通常のブランチ名",
			input: "develop",
			want: []refCandidate{
				{ref: "origin/develop", isBranch: true},
				{ref: "develop", isBranch: false},
			},
		},
		{
			name:  "16進数に見える入力もブランチを優先する",
			input: "f9211119e3",
			want: []refCandidate{
				{ref: "origin/f9211119e3", isBranch: true},
				{ref: "f9211119e3", isBranch: false},
			},
		},
		{
			name:  "数字だけのブランチ名",
			input: "12345",
			want: []refCandidate{
				{ref: "origin/12345", isBranch: true},
				{ref: "12345", isBranch: false},
			},
		},
		{
			name:  "origin/ 付きはブランチとしてのみ解釈する",
			input: "origin/main",
			want:  []refCandidate{{ref: "origin/main", isBranch: true}},
		},
		{
			name:  "前後の空白は無視する",
			input: "  main  ",
			want: []refCandidate{
				{ref: "origin/main", isBranch: true},
				{ref: "main", isBranch: false},
			},
		},
		{
			name:  "空文字は候補なし",
			input: "   ",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := refCandidates(tt.input)

			if len(got) != len(tt.want) {
				t.Fatalf("候補数 = %d, want %d (%+v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("候補[%d] = %+v, want %+v", i, got[i], tt.want[i])
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

// GIT_SSH_COMMAND はシェル経由で解釈されるため、パスに含まれるシングルクォートを
// エスケープしないとコマンドを差し込まれます。
func TestQuotePathForShell(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"通常のパス", "/home/user/.ssh/id_ed25519", `'/home/user/.ssh/id_ed25519'`},
		{"空白入り", "/tmp/my key", `'/tmp/my key'`},
		{"シングルクォート入り", "/tmp/it's", `'/tmp/it'\''s'`},
		{"コマンド差し込みの試み", "/tmp/k'; rm -rf /; '", `'/tmp/k'\''; rm -rf /; '\'''`},
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
		t.Skipf("ユーザー情報を取得できません: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"チルダなしはそのまま", "/etc/ssh/key", "/etc/ssh/key"},
		{"相対パスはそのまま", "keys/id_rsa", "keys/id_rsa"},
		{"チルダのみは展開しない", "~foo/key", "~foo/key"},
		{"チルダを展開する", "~/.ssh/id_ed25519", filepath.Join(current.HomeDir, ".ssh/id_ed25519")},
		{"空文字はそのまま", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandTilde(tt.input)
			if err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
			if got != tt.want {
				t.Errorf("expandTilde(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
