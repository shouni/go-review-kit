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

func newGoGitForTest(t *testing.T, path string) *GoGit {
	t.Helper()

	source, err := NewGoGit(path, WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("GoGit の生成に失敗: %v", err)
	}
	return source
}

func TestNewGoGitRejectsEmptyPath(t *testing.T) {
	if _, err := NewGoGit(""); err == nil {
		t.Fatal("エラーを期待しましたが nil でした")
	}
}

func TestGoGitPrepareAndDiff(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newGoGitForTest(t, localPath(t))
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

	// 同じ参照どうしなら差分は空になり、パイプラインはスキップと判断します。
	empty, err := source.Diff(ctx, repo.base, repo.base)
	if err != nil {
		t.Fatalf("差分の取得に失敗: %v", err)
	}
	if strings.TrimSpace(empty) != "" {
		t.Fatalf("差分が空ではありません:\n%s", empty)
	}
}

func TestGoGitPrepareReusesExistingCheckout(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	path := localPath(t)

	first := newGoGitForTest(t, path)
	if err := first.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("1回目の準備に失敗: %v", err)
	}

	// 既にクローン済みのパスに対しては、オープンとフェッチだけで済みます。
	second := newGoGitForTest(t, path)
	if err := second.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("2回目の準備に失敗: %v", err)
	}
	if _, err := second.Diff(ctx, repo.base, repo.head); err != nil {
		t.Fatalf("差分の取得に失敗: %v", err)
	}
}

func TestGoGitDiffResolvesRefs(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newGoGitForTest(t, localPath(t))
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("準備に失敗: %v", err)
	}

	// origin/ を明示しても、しなくても同じ参照へ解決されます。
	explicit, err := source.Diff(ctx, "origin/"+repo.base, "origin/"+repo.head)
	if err != nil {
		t.Fatalf("差分の取得に失敗: %v", err)
	}
	implicit, err := source.Diff(ctx, repo.base, repo.head)
	if err != nil {
		t.Fatalf("差分の取得に失敗: %v", err)
	}
	if explicit != implicit {
		t.Error("origin/ の有無で差分が変わっています")
	}

	// コミットハッシュを直接指定することもできます。
	hash, err := source.resolve(repo.head)
	if err != nil {
		t.Fatalf("参照の解決に失敗: %v", err)
	}
	if _, err := source.Diff(ctx, repo.base, hash.String()); err != nil {
		t.Fatalf("ハッシュ指定の差分取得に失敗: %v", err)
	}
}

func TestGoGitDiffUnknownRef(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source := newGoGitForTest(t, localPath(t))
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
		{"参照が空", repo.base, ""},
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

func TestGoGitDiffBeforePrepare(t *testing.T) {
	source := newGoGitForTest(t, localPath(t))

	if _, err := source.Diff(context.Background(), "main", "develop"); err == nil {
		t.Fatal("エラーを期待しましたが nil でした")
	}
}

// GoGit は使い捨て前提のため、Close で作業ディレクトリごと削除します。
func TestGoGitCloseRemovesWorkdir(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	path := localPath(t)

	source := newGoGitForTest(t, path)
	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("準備に失敗: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("クローン先が存在しません: %v", err)
	}

	if err := source.Close(ctx); err != nil {
		t.Fatalf("後始末に失敗: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("作業ディレクトリが残っています: %v", err)
	}

	// 存在しないパスに対する Close は成功扱いです（後始末は冪等であるべきため）。
	if err := source.Close(ctx); err != nil {
		t.Fatalf("2回目の後始末に失敗: %v", err)
	}
}

func TestGoGitPrepareFailsForUnknownRepo(t *testing.T) {
	source := newGoGitForTest(t, localPath(t))

	if err := source.Prepare(context.Background(), "/nonexistent/repo.git"); err == nil {
		t.Fatal("エラーを期待しましたが nil でした")
	}
}

// シンボリック参照が空のハッシュとして通らないこと。
//
// Reference を resolved=false で引くと、origin/HEAD のようなシンボリック参照では
// Hash() が ZeroHash を返し、エラー無しで空のハッシュが解決結果になります。
func TestGoGitResolveSkipsSymbolicRef(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source, err := NewGoGit(filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("GoGit の生成に失敗: %v", err)
	}
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("準備に失敗: %v", err)
	}

	hash, err := source.resolve("main")
	if err != nil {
		t.Fatalf("main を解決できませんでした: %v", err)
	}
	if hash.IsZero() {
		t.Error("空のハッシュが解決結果として返っています")
	}
}

// 存在しない参照は ErrRefNotFound として判別できること。
func TestGoGitResolveReportsNotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	source, err := NewGoGit(filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("GoGit の生成に失敗: %v", err)
	}
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	if err := source.Prepare(ctx, repo.path); err != nil {
		t.Fatalf("準備に失敗: %v", err)
	}

	if _, err := source.resolve("no-such-ref"); !errors.Is(err, review.ErrRefNotFound) {
		t.Errorf("err = %v, ErrRefNotFound として判別できません", err)
	}
}
