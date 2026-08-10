package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shouni/go-review-kit/review"
)

func newCLIForTest(t *testing.T, path string, opts ...Option) *CLI {
	t.Helper()
	requireGitBinary(t)

	source, err := NewCLI(path, append([]Option{WithLogger(quietLogger())}, opts...)...)
	if err != nil {
		t.Fatalf("CLI の生成に失敗: %v", err)
	}
	return source
}

func TestNewCLIRejectsEmptyPath(t *testing.T) {
	if _, err := NewCLI(""); err == nil {
		t.Fatal("エラーを期待しましたが nil でした")
	}
}

func TestCLIPrepareAndDiff(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newCLIForTest(t, localPath(t), WithBase(repo.base))
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("準備に失敗: %v", err)
	}

	diff, err := source.Diff(ctx, repo.base, repo.head)
	if err != nil {
		t.Fatalf("差分の取得に失敗: %v", err)
	}
	if !strings.Contains(diff, "hello") {
		t.Fatalf("差分に head 側の変更が含まれていません:\n%s", diff)
	}

	empty, err := source.Diff(ctx, repo.base, repo.base)
	if err != nil {
		t.Fatalf("差分の取得に失敗: %v", err)
	}
	if strings.TrimSpace(empty) != "" {
		t.Fatalf("差分が空ではありません:\n%s", empty)
	}
}

// 差分の本文に git の警告が混ざらないことを確認します。標準出力と標準エラーを
// まとめて受け取ると、警告がそのまま AI へのプロンプトに入ってしまいます。
func TestCLIDiffContainsOnlyPatch(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newCLIForTest(t, localPath(t), WithBase(repo.base))
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("準備に失敗: %v", err)
	}

	diff, err := source.Diff(ctx, repo.base, repo.head)
	if err != nil {
		t.Fatalf("差分の取得に失敗: %v", err)
	}
	if !strings.HasPrefix(diff, "diff --git") {
		t.Fatalf("差分がパッチで始まっていません:\n%s", diff)
	}
}

func TestCLIDiffUnknownRef(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newCLIForTest(t, localPath(t), WithBase(repo.base))
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("準備に失敗: %v", err)
	}

	tests := []struct {
		name string
		base string
		head string
	}{
		{"head が存在しない", repo.base, "no-such-branch"},
		{"base が存在しない", "no-such-branch", repo.head},
		{"参照が空", repo.base, "  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := source.Diff(ctx, tt.base, tt.head)
			if !errors.Is(err, review.ErrRefNotFound) {
				t.Fatalf("ErrRefNotFound を期待しましたが: %v", err)
			}
		})
	}
}

// CLI はチェックアウトを再利用するため、Close で作業ディレクトリを消さず、
// 基準参照へ戻して未追跡ファイルを片付けます。
func TestCLICloseRestoresCleanCheckout(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	path := localPath(t)

	source := newCLIForTest(t, path, WithBase(repo.base))
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("準備に失敗: %v", err)
	}

	// 追跡ファイルの改変と、未追跡ファイルの追加を仕込みます。
	tracked := filepath.Join(path, "main.go")
	if err := os.WriteFile(tracked, []byte("壊した内容\n"), 0o600); err != nil {
		t.Fatalf("ファイルの書き込みに失敗: %v", err)
	}
	untracked := filepath.Join(path, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("ゴミ\n"), 0o600); err != nil {
		t.Fatalf("ファイルの書き込みに失敗: %v", err)
	}

	if err := source.Close(ctx); err != nil {
		t.Fatalf("後始末に失敗: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("作業ディレクトリが消えています: %v", err)
	}
	if _, err := os.Stat(untracked); !os.IsNotExist(err) {
		t.Fatalf("未追跡ファイルが残っています: %v", err)
	}
	content, err := os.ReadFile(tracked) //nolint:gosec // テスト内の一時パス
	if err != nil {
		t.Fatalf("ファイルの読み込みに失敗: %v", err)
	}
	if strings.Contains(string(content), "壊した内容") {
		t.Fatal("改変した内容が復元されていません")
	}

	// 後始末のあとも、そのまま次の差分取得を続けられます。
	if _, err := source.Diff(ctx, repo.base, repo.head); err != nil {
		t.Fatalf("後始末後の差分取得に失敗: %v", err)
	}
}

// 基準参照が未設定の場合はチェックアウトを省略し、掃除だけ行います。
func TestCLICloseWithoutBase(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	path := localPath(t)

	source := newCLIForTest(t, path)
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("準備に失敗: %v", err)
	}

	untracked := filepath.Join(path, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("ゴミ\n"), 0o600); err != nil {
		t.Fatalf("ファイルの書き込みに失敗: %v", err)
	}

	if err := source.Close(ctx); err != nil {
		t.Fatalf("後始末に失敗: %v", err)
	}
	if _, err := os.Stat(untracked); !os.IsNotExist(err) {
		t.Fatalf("未追跡ファイルが残っています: %v", err)
	}
}

func TestCLIPrepareRejectsNonRepository(t *testing.T) {
	requireGitBinary(t)

	t.Run("Gitリポジトリではないディレクトリ", func(t *testing.T) {
		dir := t.TempDir()
		source := newCLIForTest(t, dir)

		err := source.Prepare(context.Background(), newTestRepo(t).path)
		if err == nil {
			t.Fatal("エラーを期待しましたが nil でした")
		}
		if !strings.Contains(err.Error(), "Git リポジトリではありません") {
			t.Fatalf("理由が伝わるエラーではありません: %v", err)
		}
	})

	t.Run("ディレクトリではないパス", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatalf("ファイルの作成に失敗: %v", err)
		}

		source := newCLIForTest(t, file)
		if err := source.Prepare(context.Background(), newTestRepo(t).path); err == nil {
			t.Fatal("エラーを期待しましたが nil でした")
		}
	})
}

func TestCLIEnvIncludesSSHCommand(t *testing.T) {
	t.Run("鍵未設定なら追加しない", func(t *testing.T) {
		source, err := NewCLI("/tmp/repo")
		if err != nil {
			t.Fatalf("生成に失敗: %v", err)
		}
		for _, kv := range source.env() {
			if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") {
				t.Fatalf("GIT_SSH_COMMAND が設定されています: %s", kv)
			}
		}
	})

	t.Run("鍵のパスをクォートして渡す", func(t *testing.T) {
		source, err := NewCLI("/tmp/repo", WithSSHKey("/tmp/it's key"))
		if err != nil {
			t.Fatalf("生成に失敗: %v", err)
		}

		var got string
		for _, kv := range source.env() {
			if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") {
				got = kv
			}
		}

		want := `GIT_SSH_COMMAND=ssh -i '/tmp/it'\''s key' -F /dev/null`
		if got != want {
			t.Fatalf("GIT_SSH_COMMAND = %q, want %q", got, want)
		}
	})
}
