package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shouni/go-review-kit/review"
)

func testRequestFor(repo testRepo) review.Request {
	return review.Request{
		RepoURL:    repo.path,
		Base:       repo.base,
		Head:       repo.head,
		Mode:       "detail",
		Model:      "gemini-2.5-pro",
		StorageURI: "gs://bucket/review.html",
	}
}

func TestFactoryRejectsEmptyRoot(t *testing.T) {
	if _, err := NewGoGitFactory(""); err == nil {
		t.Fatal("GoGit: エラーを期待しましたが nil でした")
	}
	if _, err := NewCLIFactory(""); err == nil {
		t.Fatal("CLI: エラーを期待しましたが nil でした")
	}
}

// 不正なオプションは Open のたびに失敗し続けるため、生成時に弾きます。
func TestFactoryValidatesOptionsEagerly(t *testing.T) {
	if _, err := NewGoGitFactory(t.TempDir(), WithContextLines(-1)); err == nil {
		t.Fatal("エラーを期待しましたが nil でした")
	}
}

func TestFactoryOpen(t *testing.T) {
	tests := []struct {
		name string
		newF func(root string, opts ...Option) (*Factory, error)
		skip func(*testing.T)
	}{
		{"GoGit", NewGoGitFactory, func(*testing.T) {}},
		{"CLI", NewCLIFactory, requireGitBinary},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.skip(t)

			repo := newTestRepo(t)
			root := t.TempDir()
			ctx := context.Background()

			factory, err := tt.newF(root, WithLogger(quietLogger()))
			if err != nil {
				t.Fatalf("Factory の生成に失敗: %v", err)
			}

			source, err := factory.Open(ctx, testRequestFor(repo))
			if err != nil {
				t.Fatalf("Open に失敗: %v", err)
			}
			t.Cleanup(func() { _ = source.Close(context.Background()) })

			// 作業ディレクトリは root 配下のリポジトリURL由来の名前になります。
			want := filepath.Join(root, RepoDirName(repo.path))
			if _, err := os.Stat(want); err != nil {
				t.Fatalf("期待した作業ディレクトリがありません (%s): %v", want, err)
			}

			diff, err := source.Diff(ctx, repo.base, repo.head)
			if err != nil {
				t.Fatalf("差分の取得に失敗: %v", err)
			}
			if !strings.Contains(diff, "hello") {
				t.Fatalf("差分の内容が一致しません:\n%s", diff)
			}
		})
	}
}

// 準備に失敗したら、途中まで作った作業ディレクトリを残しません。
// 残ると、次回の準備が壊れた状態から始まります。
func TestFactoryOpenCleansUpOnPrepareFailure(t *testing.T) {
	root := t.TempDir()

	factory, err := NewGoGitFactory(root, WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("Factory の生成に失敗: %v", err)
	}

	req := review.Request{
		RepoURL:    "/nonexistent/repo.git",
		Base:       "main",
		Head:       "develop",
		Model:      "gemini-2.5-pro",
		StorageURI: "gs://bucket/review.html",
	}

	if _, err := factory.Open(context.Background(), req); err == nil {
		t.Fatal("エラーを期待しましたが nil でした")
	}

	path := filepath.Join(root, RepoDirName(req.RepoURL))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("失敗した作業ディレクトリが残っています: %v", err)
	}
}

// Factory は Request ごとに基準参照を差し込みます。直接生成した場合と違い、
// 呼び出し側が WithBase を意識する必要はありません。
func TestFactoryInjectsBase(t *testing.T) {
	requireGitBinary(t)

	repo := newTestRepo(t)
	ctx := context.Background()

	factory, err := NewCLIFactory(t.TempDir(), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("Factory の生成に失敗: %v", err)
	}

	source, err := factory.Open(ctx, testRequestFor(repo))
	if err != nil {
		t.Fatalf("Open に失敗: %v", err)
	}

	cli, ok := source.(*CLI)
	if !ok {
		t.Fatalf("CLI を期待しましたが %T でした", source)
	}
	if cli.settings.base != repo.base {
		t.Fatalf("base = %q, want %q", cli.settings.base, repo.base)
	}

	if err := source.Close(ctx); err != nil {
		t.Fatalf("後始末に失敗: %v", err)
	}
}

func TestFactoryUsesCustomDirNamer(t *testing.T) {
	repo := newTestRepo(t)
	root := t.TempDir()
	ctx := context.Background()

	factory, err := NewGoGitFactory(root,
		WithLogger(quietLogger()),
		WithDirNamer(func(string) string { return "fixed-name" }),
	)
	if err != nil {
		t.Fatalf("Factory の生成に失敗: %v", err)
	}

	source, err := factory.Open(ctx, testRequestFor(repo))
	if err != nil {
		t.Fatalf("Open に失敗: %v", err)
	}
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	if _, err := os.Stat(filepath.Join(root, "fixed-name")); err != nil {
		t.Fatalf("差し替えた名前の作業ディレクトリがありません: %v", err)
	}
}
