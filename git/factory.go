package git

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/shouni/go-review-kit/review"
)

// Source は、本パッケージのアダプターが満たす形です。
// review.DiffSource に、リポジトリを使える状態にする Prepare を加えたものです。
type Source interface {
	review.DiffSource
	// Prepare は、リポジトリをクローン（または既存を利用）してリモートの最新を取得します。
	Prepare(ctx context.Context, repoURL string) error
}

// Factory は、Request ごとに作業ディレクトリを決めて Source を開く review.DiffSourceFactory です。
//
// 作業ディレクトリは root/<リポジトリURL由来の名前> です。同じリポジトリからは常に同じ
// パスが得られるため、CLI 実装はチェックアウトを再利用できます。
//
// # 同じリポジトリの同時実行について
//
// **既定の命名は URL だけから決まるため、同じリポジトリのレビューを同時に走らせると
// 同じ作業ディレクトリを共有します。** 本パッケージは排他を行いません。共有されると:
//
//   - GoGit: 先に終わった側の Close が、まだ実行中の側の作業ディレクトリごと削除します。
//   - CLI: 一方が読んでいる最中にもう一方が別ブランチへ checkout するため、
//     **エラーにならずに別ブランチの内容をレビューします**（気付きにくい壊れ方です）。
//
// 同一プロセスで同じリポジトリを並行にレビューする場合は、WithDirNamer で実行ごとに
// 異なる名前を返してください（例: RepoDirName の結果へ実行 ID を足す）。
// チェックアウトの再利用は諦めることになりますが、使い捨ての環境では元々効きません。
type Factory struct {
	root      string
	settings  settings
	opts      []Option
	newSource func(localPath string, opts ...Option) (Source, error)
}

// 実装がポートを満たすことをコンパイル時に確認します。
var _ review.DiffSourceFactory = (*Factory)(nil)

// NewGoGitFactory は、GoGit を開く Factory を生成します。
func NewGoGitFactory(root string, opts ...Option) (*Factory, error) {
	return newFactory(root, opts, func(localPath string, o ...Option) (Source, error) {
		return NewGoGit(localPath, o...)
	})
}

// NewCLIFactory は、CLI を開く Factory を生成します。
func NewCLIFactory(root string, opts ...Option) (*Factory, error) {
	return newFactory(root, opts, func(localPath string, o ...Option) (Source, error) {
		return NewCLI(localPath, o...)
	})
}

func newFactory(root string, opts []Option, newSource func(string, ...Option) (Source, error)) (*Factory, error) {
	if root == "" {
		return nil, fmt.Errorf("git: 作業ディレクトリのルートが空です")
	}

	// 設定の検証をここで済ませ、Open のたびに同じエラーで失敗し続けるのを防ぎます。
	s, err := newSettings(opts...)
	if err != nil {
		return nil, err
	}

	return &Factory{root: root, settings: s, opts: opts, newSource: newSource}, nil
}

// Open は、Request に対応する作業ディレクトリで Source を準備して返します。
func (f *Factory) Open(ctx context.Context, req review.Request) (review.DiffSource, error) {
	localPath := filepath.Join(f.root, f.settings.dirNamer(req.RepoURL))

	// 後始末で戻る先は Request ごとに変わるため、生成時のオプションへ追加します。
	opts := append(slices.Clone(f.opts), WithBase(req.Base))

	source, err := f.newSource(localPath, opts...)
	if err != nil {
		return nil, err
	}

	if err := source.Prepare(ctx, req.RepoURL); err != nil {
		// 途中まで作られた作業ディレクトリを残すと、次回の準備が壊れた状態から始まります。
		// 後始末自体の失敗はここでは致命的ではないため、記録するだけにします。
		if closeErr := source.Close(context.WithoutCancel(ctx)); closeErr != nil {
			f.settings.logger.WarnContext(ctx, "準備失敗後の後始末に失敗しました", "error", closeErr)
		}
		return nil, err
	}

	return source, nil
}
