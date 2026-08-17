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

// readMainGo は、作業ディレクトリの main.go を読みます。
func readMainGo(t *testing.T, dir string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("main.go の読み込みに失敗: %v", err)
	}
	return string(content)
}

func TestCLICheckoutHead(t *testing.T) {
	requireGitBinary(t)
	repo := newTestRepo(t)
	ctx := context.Background()

	cli, err := NewCLI(localPath(t), WithBase(repo.base), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("CLI の生成に失敗: %v", err)
	}
	if err := cli.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("Prepare に失敗: %v", err)
	}

	dir, err := cli.CheckoutHead(ctx, repo.head)
	if err != nil {
		t.Fatalf("CheckoutHead に失敗: %v", err)
	}
	if !strings.Contains(readMainGo(t, dir), "hello") {
		t.Error("作業ツリーが head の内容になっていません")
	}

	// Close で基準参照へ戻ることも確認します（head のまま残ると次回の Diff 前提が崩れます）。
	if err := cli.Close(ctx); err != nil {
		t.Fatalf("Close に失敗: %v", err)
	}
	if strings.Contains(readMainGo(t, dir), "hello") {
		t.Error("Close 後も作業ツリーが head のままです")
	}
}

func TestCLICheckoutHeadRemovesUntracked(t *testing.T) {
	requireGitBinary(t)
	repo := newTestRepo(t)
	ctx := context.Background()

	cli, err := NewCLI(localPath(t), WithBase(repo.base), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("CLI の生成に失敗: %v", err)
	}
	if err := cli.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("Prepare に失敗: %v", err)
	}

	// 前回の実行が Close へ到達せず残した未追跡ファイルを装います。
	leftover := filepath.Join(cli.localPath, "leftover.txt")
	if err := os.WriteFile(leftover, []byte("stale"), 0o600); err != nil {
		t.Fatalf("未追跡ファイルの作成に失敗: %v", err)
	}

	if _, err := cli.CheckoutHead(ctx, repo.head); err != nil {
		t.Fatalf("CheckoutHead に失敗: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Error("未追跡ファイルが残っています")
	}
}

func TestCLICheckoutHeadUnknownRef(t *testing.T) {
	requireGitBinary(t)
	repo := newTestRepo(t)
	ctx := context.Background()

	cli, err := NewCLI(localPath(t), WithBase(repo.base), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("CLI の生成に失敗: %v", err)
	}
	if err := cli.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("Prepare に失敗: %v", err)
	}

	if _, err := cli.CheckoutHead(ctx, "no-such-ref"); !errors.Is(err, review.ErrRefNotFound) {
		t.Errorf("ErrRefNotFound が返っていません: %v", err)
	}
}

func TestGoGitCheckoutHead(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	g, err := NewGoGit(localPath(t), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("GoGit の生成に失敗: %v", err)
	}
	if err := g.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("Prepare に失敗: %v", err)
	}
	t.Cleanup(func() {
		if err := g.Close(context.Background()); err != nil {
			t.Errorf("Close に失敗: %v", err)
		}
	})

	dir, err := g.CheckoutHead(ctx, repo.head)
	if err != nil {
		t.Fatalf("CheckoutHead に失敗: %v", err)
	}
	if !strings.Contains(readMainGo(t, dir), "hello") {
		t.Error("作業ツリーが head の内容になっていません")
	}
}

func TestGoGitCheckoutHeadBeforePrepare(t *testing.T) {
	g, err := NewGoGit(localPath(t), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("GoGit の生成に失敗: %v", err)
	}

	if _, err := g.CheckoutHead(context.Background(), "main"); err == nil {
		t.Fatal("Prepare 前の CheckoutHead がエラーになりません")
	}
}

func TestGoGitCheckoutHeadUnknownRef(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	g, err := NewGoGit(localPath(t), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("GoGit の生成に失敗: %v", err)
	}
	if err := g.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("Prepare に失敗: %v", err)
	}
	t.Cleanup(func() {
		if err := g.Close(context.Background()); err != nil {
			t.Errorf("Close に失敗: %v", err)
		}
	})

	if _, err := g.CheckoutHead(ctx, "no-such-ref"); !errors.Is(err, review.ErrRefNotFound) {
		t.Errorf("ErrRefNotFound が返っていません: %v", err)
	}
}
