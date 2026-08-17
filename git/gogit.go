package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/shouni/go-review-kit/review"
)

// GoGit は、go-git による review.DiffSource 実装です。
//
// git バイナリを必要としません。Close で作業ディレクトリごと削除するため、実行のたびに
// クローンし直す使い捨ての環境に向きます。
//
// 単一の実行（Prepare → Diff → Close）で使う前提で、並行利用は想定していません。
type GoGit struct {
	localPath string
	settings  settings

	repo *gogit.Repository
	auth transport.AuthMethod
}

// 実装がポートを満たすことをコンパイル時に確認します。
var (
	_ Source                   = (*GoGit)(nil)
	_ review.WorkspaceProvider = (*GoGit)(nil)
)

// NewGoGit は、localPath を作業ディレクトリとする GoGit を生成します。
func NewGoGit(localPath string, opts ...Option) (*GoGit, error) {
	if localPath == "" {
		return nil, fmt.Errorf("git: 作業ディレクトリのパスが空です")
	}

	s, err := newSettings(opts...)
	if err != nil {
		return nil, err
	}
	return &GoGit{localPath: localPath, settings: s}, nil
}

// Prepare は、リポジトリをクローン（既にあればオープン）し、リモートの最新を取得します。
func (g *GoGit) Prepare(ctx context.Context, repoURL string) error {
	auth, err := sshAuth(repoURL, g.settings.sshKeyPath)
	if err != nil {
		return fmt.Errorf("認証情報の構築に失敗しました: %w", err)
	}
	g.auth = auth

	if err := g.cloneOrOpen(ctx, repoURL); err != nil {
		return err
	}
	return g.fetch(ctx)
}

func (g *GoGit) cloneOrOpen(ctx context.Context, repoURL string) error {
	_, err := os.Stat(g.localPath)
	switch {
	case os.IsNotExist(err):
		g.settings.logger.InfoContext(ctx, "リポジトリをクローンします", "url", repoURL, "path", g.localPath)
		repo, cloneErr := gogit.PlainCloneContext(ctx, g.localPath, false, &gogit.CloneOptions{
			URL:          repoURL,
			SingleBranch: false,
			Auth:         g.auth,
		})
		if cloneErr != nil {
			return fmt.Errorf("クローンに失敗しました: %w", cloneErr)
		}
		g.repo = repo
		return nil

	case err == nil:
		repo, openErr := gogit.PlainOpen(g.localPath)
		if openErr != nil {
			return fmt.Errorf("既存リポジトリのオープンに失敗しました: %w", openErr)
		}
		g.settings.logger.InfoContext(ctx, "既存リポジトリをオープンしました", "path", g.localPath)
		g.repo = repo
		return nil

	default:
		return fmt.Errorf("作業ディレクトリの確認に失敗しました (%s): %w", g.localPath, err)
	}
}

func (g *GoGit) fetch(ctx context.Context) error {
	g.settings.logger.InfoContext(ctx, "リモートから最新を取得します", "remote", remoteName)

	err := g.repo.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName: remoteName,
		Auth:       g.auth,
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/*:refs/remotes/" + remoteName + "/*")},
		Tags:       gogit.AllTags,
		Progress:   io.Discard,
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return fmt.Errorf("フェッチに失敗しました: %w", err)
	}
	return nil
}

// Diff は base と head のマージベースから head までの差分を返します。
func (g *GoGit) Diff(ctx context.Context, base, head string) (string, error) {
	if g.repo == nil {
		return "", fmt.Errorf("git: Prepare が完了していません")
	}

	baseCommit, err := g.commit(base)
	if err != nil {
		return "", err
	}
	headCommit, err := g.commit(head)
	if err != nil {
		return "", err
	}

	// 3 点比較にするため、マージベースを差分の起点にします。base 側で進んだコミットが
	// 差分に混ざるのを防ぎ、head で行われた変更だけをレビュー対象にできます。
	mergeBases, err := baseCommit.MergeBase(headCommit)
	if err != nil {
		return "", fmt.Errorf("マージベースの検索に失敗しました: %w", err)
	}
	if len(mergeBases) == 0 {
		return "", fmt.Errorf("%s と %s に共通の祖先が見つかりませんでした", base, head)
	}

	baseTree, err := mergeBases[0].Tree()
	if err != nil {
		return "", fmt.Errorf("ベースツリーの取得に失敗しました: %w", err)
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		return "", fmt.Errorf("ヘッドツリーの取得に失敗しました: %w", err)
	}

	changes, err := object.DiffTreeWithOptions(ctx, baseTree, headTree, object.DefaultDiffTreeOptions)
	if err != nil {
		return "", fmt.Errorf("差分の計算に失敗しました: %w", err)
	}

	patch, err := changes.PatchContext(ctx)
	if err != nil {
		return "", fmt.Errorf("パッチの生成に失敗しました: %w", err)
	}
	return patch.String(), nil
}

// CheckoutHead は、head を作業ツリーへ強制チェックアウトし、そのパスを返します。
// review.WorkspaceProvider の実装です。
//
// go-git のチェックアウトは context を受け取れないため、ctx は取り消しには効かず
// ログにだけ使います（シグネチャはポート側の統一です）。
func (g *GoGit) CheckoutHead(ctx context.Context, head string) (string, error) {
	if g.repo == nil {
		return "", fmt.Errorf("git: Prepare が完了していません")
	}

	hash, err := g.resolve(head)
	if err != nil {
		return "", fmt.Errorf("head の参照を解決できませんでした: %w", err)
	}

	worktree, err := g.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("ワークツリーの取得に失敗しました: %w", err)
	}

	// Force で作業ツリーのローカル変更を破棄します。クローン直後は既定ブランチの内容の
	// ままであり、レビュアーが読むのは必ず head の状態でなければならないためです。
	if err := worktree.Checkout(&gogit.CheckoutOptions{Hash: hash, Force: true}); err != nil {
		return "", fmt.Errorf("head のチェックアウトに失敗しました (%s): %w", head, err)
	}

	g.settings.logger.InfoContext(ctx, "head をチェックアウトしました", "path", g.localPath, "head", head)
	return g.localPath, nil
}

// Close は作業ディレクトリを削除します。
func (g *GoGit) Close(ctx context.Context) error {
	g.settings.logger.InfoContext(ctx, "作業ディレクトリを削除します", "path", g.localPath)
	g.repo = nil

	if err := os.RemoveAll(g.localPath); err != nil {
		return fmt.Errorf("作業ディレクトリの削除に失敗しました (%s): %w", g.localPath, err)
	}
	return nil
}

// commit は参照文字列をコミットオブジェクトへ解決します。
func (g *GoGit) commit(ref string) (*object.Commit, error) {
	hash, err := g.resolve(ref)
	if err != nil {
		return nil, err
	}

	commit, err := g.repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("コミットの取得に失敗しました (%s): %w", ref, err)
	}
	return commit, nil
}

// resolve は、参照文字列をコミットハッシュへ解決します。
// 解決できない場合は review.ErrRefNotFound を包んだエラーを返します。
func (g *GoGit) resolve(ref string) (plumbing.Hash, error) {
	if ref == "" {
		return plumbing.ZeroHash, fmt.Errorf("%w: 参照名が空です", review.ErrRefNotFound)
	}

	for _, candidate := range refCandidates(ref) {
		if candidate.isBranch {
			name := plumbing.NewRemoteReferenceName(remoteName, localBranchName(candidate.ref))
			if reference, err := g.repo.Reference(name, false); err == nil {
				return reference.Hash(), nil
			}
			continue
		}

		// ^{commit} を付けることで、アノテートタグもコミットまで剥がして解決します。
		hash, err := g.repo.ResolveRevision(plumbing.Revision(candidate.ref + "^{commit}"))
		if err != nil {
			hash, err = g.repo.ResolveRevision(plumbing.Revision(candidate.ref))
			if err != nil {
				continue
			}
		}
		if _, err := g.repo.CommitObject(*hash); err != nil {
			continue
		}
		return *hash, nil
	}

	return plumbing.ZeroHash, fmt.Errorf("%w: %s", review.ErrRefNotFound, ref)
}
