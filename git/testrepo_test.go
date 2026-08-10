package git

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// testRepo は、クローン元として使えるリポジトリです。
type testRepo struct {
	// path はリポジトリのパスです。クローン元の URL としてそのまま渡せます。
	path string
	// base は初期コミットだけを持つブランチ名です。
	base string
	// head は base から 1 コミット進んだブランチ名です。
	head string
}

const (
	testBaseBranch = "main"
	testHeadBranch = "feature"
)

// newTestRepo は、base と head の 2 ブランチを持つリポジトリを作ります。
// head 側にだけ差分があり、base はその祖先です。
func newTestRepo(t *testing.T) testRepo {
	t.Helper()

	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("リポジトリの初期化に失敗: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("ワークツリーの取得に失敗: %v", err)
	}

	commit := func(content, message string) plumbing.Hash {
		t.Helper()

		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0o600); err != nil {
			t.Fatalf("ファイルの書き込みに失敗: %v", err)
		}
		if _, err := worktree.Add("main.go"); err != nil {
			t.Fatalf("ステージに失敗: %v", err)
		}
		hash, err := worktree.Commit(message, &gogit.CommitOptions{
			Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()},
		})
		if err != nil {
			t.Fatalf("コミットに失敗: %v", err)
		}
		return hash
	}

	initial := commit("package main\n\nfunc main() {}\n", "初期コミット")

	// 既定ブランチ名は環境によって変わるため、明示的に付け替えます。
	if err := worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(testBaseBranch),
		Hash:   initial,
		Create: true,
	}); err != nil {
		t.Fatalf("baseブランチの作成に失敗: %v", err)
	}

	if err := worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(testHeadBranch),
		Hash:   initial,
		Create: true,
	}); err != nil {
		t.Fatalf("headブランチの作成に失敗: %v", err)
	}
	commit("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n", "hello を追加")

	// クローン元の HEAD は base に戻しておきます。
	if err := worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(testBaseBranch),
	}); err != nil {
		t.Fatalf("baseブランチへの復帰に失敗: %v", err)
	}

	return testRepo{path: dir, base: testBaseBranch, head: testHeadBranch}
}

// localPath は、まだ存在しないクローン先のパスを返します。
func localPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "checkout")
}

// quietLogger は、テスト出力を汚さないロガーです。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// requireGitBinary は、git コマンドが無い環境ではテストをスキップします。
func requireGitBinary(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git コマンドが見つからないためスキップします")
	}
}
