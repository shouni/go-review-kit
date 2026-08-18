package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shouni/go-review-kit/review"
)

// cleanArgs は、作業ツリーから残骸を落とすときの git clean の引数です。
//
// -x（無視ファイルも消す）と 2 つ目の -f（ネストした git リポジトリも消す）が要ります。
// これらが無いと、.gitignore に載る名前（node_modules/ や .env など）とベンダリングされた
// リポジトリが残り、**前回の実行の残骸が head の内容としてレビュアーに読まれます**。
var cleanArgs = []string{"clean", "-f", "-f", "-d", "-x"}

// killGrace は、context のキャンセル後に git の後片付けを待つ猶予です。
//
// これが無いと、git が起動した ssh が親のパイプを継承したまま生き残り、cmd.Run は
// パイプが閉じるまで返りません。相手が黙って切れた場合、キャンセルしたはずの処理が
// 数分単位で居座ります。
const killGrace = 5 * time.Second

// CLI は、ローカルの git コマンドを呼び出す review.DiffSource 実装です。
//
// Close で作業ディレクトリを消さず、基準参照へ戻して掃除するだけなので、次回以降は
// 差分の取得だけで済みます。永続的な作業ディレクトリを持てる環境向けです。
type CLI struct {
	localPath string
	settings  settings
}

// 実装がポートを満たすことをコンパイル時に確認します。
var (
	_ Source                   = (*CLI)(nil)
	_ review.WorkspaceProvider = (*CLI)(nil)
)

// NewCLI は、localPath を作業ディレクトリとする CLI を生成します。
func NewCLI(localPath string, opts ...Option) (*CLI, error) {
	if localPath == "" {
		return nil, fmt.Errorf("git: 作業ディレクトリのパスが空です")
	}

	s, err := newSettings(opts...)
	if err != nil {
		return nil, err
	}
	return &CLI{localPath: localPath, settings: s}, nil
}

// Prepare は、リポジトリをクローン（既にあればそのまま利用）し、リモートの最新を取得します。
func (c *CLI) Prepare(ctx context.Context, repoURL string) error {
	if err := validateRepoURL(repoURL); err != nil {
		return err
	}

	if err := c.cloneIfNeeded(ctx, repoURL); err != nil {
		return err
	}

	if _, err := c.run(ctx, "fetch", remoteName, "--prune"); err != nil {
		return fmt.Errorf("フェッチに失敗しました: %w", err)
	}
	return nil
}

func (c *CLI) cloneIfNeeded(ctx context.Context, repoURL string) error {
	info, err := os.Stat(c.localPath)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("作業ディレクトリのパスがディレクトリではありません: %s", c.localPath)
		}
		if _, gitErr := os.Stat(filepath.Join(c.localPath, ".git")); gitErr != nil {
			if os.IsNotExist(gitErr) {
				return fmt.Errorf("%s は存在しますが Git リポジトリではありません", c.localPath)
			}
			return fmt.Errorf("%s の .git の確認に失敗しました: %w", c.localPath, gitErr)
		}
		c.settings.logger.InfoContext(ctx, "既存のチェックアウトを再利用します", "path", c.localPath)
		return nil

	case os.IsNotExist(err):
		return c.clone(ctx, repoURL)

	default:
		return fmt.Errorf("作業ディレクトリの確認に失敗しました (%s): %w", c.localPath, err)
	}
}

func (c *CLI) clone(ctx context.Context, repoURL string) error {
	parent := filepath.Dir(c.localPath)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return fmt.Errorf("親ディレクトリの作成に失敗しました (%s): %w", parent, err)
	}

	c.settings.logger.InfoContext(ctx, "リポジトリをクローンします", "url", redactURL(repoURL), "path", c.localPath)

	// clone だけは作業ディレクトリがまだ無いため、親ディレクトリで実行します。
	// "--" を挟むのは、URL がオプションとして解釈されるのを防ぐためです
	// （"--upload-pack=..." のような値を渡されると git はオプションとして読みます）。
	if _, err := c.runIn(ctx, parent, "clone", "--", repoURL, filepath.Base(c.localPath)); err != nil {
		// 作りかけを残しません。残すと次回以降 cloneIfNeeded が「既存のチェックアウトを
		// 再利用します」と判定し、以後の fetch が not a git repository で失敗し続けます
		// （手で消すまで、そのリポジトリのレビューが永久に通りません）。
		if rmErr := os.RemoveAll(c.localPath); rmErr != nil {
			c.settings.logger.WarnContext(ctx, "クローン失敗後の作業ディレクトリを削除できませんでした",
				"path", c.localPath, "error", rmErr)
		}
		return fmt.Errorf("クローンに失敗しました: %w", err)
	}
	return nil
}

// Diff は base と head の 3 点比較差分を返します。
func (c *CLI) Diff(ctx context.Context, base, head string) (string, error) {
	baseRef, _, err := c.resolve(ctx, base)
	if err != nil {
		return "", err
	}
	headRef, _, err := c.resolve(ctx, head)
	if err != nil {
		return "", err
	}

	c.settings.logger.InfoContext(ctx, "差分を取得します", "base", baseRef, "head", headRef)

	return c.run(ctx,
		"diff",
		fmt.Sprintf("%s...%s", baseRef, headRef),
		fmt.Sprintf("--unified=%d", c.settings.contextLines),
	)
}

// CheckoutHead は、head を作業ツリーへ強制チェックアウトし、そのパスを返します。
// review.WorkspaceProvider の実装です。
//
// チェックアウト後に未追跡ファイルも削除します。作業ディレクトリは実行をまたいで
// 再利用されるため、前回の実行が Close まで到達せず落ちていた場合の残骸が、head の
// 内容としてレビュアーに読まれるのを防ぎます。
func (c *CLI) CheckoutHead(ctx context.Context, head string) (string, error) {
	headRef, isBranch, err := c.resolve(ctx, head)
	if err != nil {
		return "", fmt.Errorf("head の参照を解決できませんでした: %w", err)
	}

	if err := c.forceCheckout(ctx, headRef, isBranch); err != nil {
		return "", fmt.Errorf("head のチェックアウトに失敗しました: %w", err)
	}

	if _, err := c.run(ctx, cleanArgs...); err != nil {
		return "", fmt.Errorf("head チェックアウト後のクリーンに失敗しました: %w", err)
	}

	c.settings.logger.InfoContext(ctx, "head をチェックアウトしました", "path", c.localPath, "head", headRef)
	return c.localPath, nil
}

// Close は、基準参照へ強制的に戻したうえで未追跡ファイルを削除します。
// 作業ディレクトリ自体は次回の実行で再利用するため残します。
//
// ★ 掃除を先に、フェッチを後に行います。後始末の先頭にネットワーク I/O を置くと、
// レビュー末尾で通信が落ちていたり締切を使い切っていたりしたときに、
// **作業ツリーが head のまま・未追跡ファイルも残ったまま**次回へ持ち越されます。
// フェッチは次回の参照解決を新しく保つための best-effort なので、失敗しても
// 後始末そのものは成立させます。
func (c *CLI) Close(ctx context.Context) error {
	// 未追跡ファイルは checkout では消えないので、先に落とします。
	if _, err := c.run(ctx, cleanArgs...); err != nil {
		return fmt.Errorf("後始末のクリーンに失敗しました: %w", err)
	}

	if err := c.checkoutBase(ctx); err != nil {
		return err
	}

	if _, err := c.run(ctx, "fetch", remoteName); err != nil {
		c.settings.logger.WarnContext(ctx, "後始末のフェッチに失敗しました（掃除は完了しています）",
			"path", c.localPath, "error", err)
	}

	c.settings.logger.InfoContext(ctx, "後始末が完了しました", "path", c.localPath, "base", c.settings.base)
	return nil
}

func (c *CLI) checkoutBase(ctx context.Context) error {
	if c.settings.base == "" {
		// Factory 経由なら Request.Base が必ず入るため、ここに来るのは直接生成した場合だけです。
		// 戻る先が決められないので、掃除だけ行います。
		c.settings.logger.WarnContext(ctx, "基準参照が未設定のため、チェックアウトを省略します")
		return nil
	}

	baseRef, isBranch, err := c.resolve(ctx, c.settings.base)
	if err != nil {
		return fmt.Errorf("後始末の基準参照を解決できませんでした: %w", err)
	}

	if err := c.forceCheckout(ctx, baseRef, isBranch); err != nil {
		return fmt.Errorf("後始末のチェックアウトに失敗しました: %w", err)
	}
	return nil
}

// forceCheckout は、解決済みの参照へ作業ツリーを強制的に切り替えます。
//
// -f を付けてローカルの変更を破棄します。前回の実行が途中で落ちていても、
// 次の実行を必ずきれいな状態から始められるようにするためです。
func (c *CLI) forceCheckout(ctx context.Context, ref string, isBranch bool) error {
	args := []string{"checkout", "-f", ref}
	if isBranch {
		args = []string{"checkout", "-f", "-B", localBranchName(ref), ref}
	}
	_, err := c.run(ctx, args...)
	return err
}

// resolve は、参照文字列をリモートブランチまたはコミットとして解決します。
func (c *CLI) resolve(ctx context.Context, ref string) (resolved string, isBranch bool, err error) {
	if strings.TrimSpace(ref) == "" {
		return "", false, fmt.Errorf("%w: 参照名が空です", review.ErrRefNotFound)
	}

	for _, candidate := range refCandidates(ref) {
		if _, err := c.run(ctx, "rev-parse", "--verify", "--quiet", candidate.ref+"^{commit}"); err == nil {
			return candidate.ref, candidate.isBranch, nil
		}
	}
	return "", false, fmt.Errorf("%w: %s", review.ErrRefNotFound, ref)
}

// run は、作業ディレクトリで git コマンドを実行し、標準出力を返します。
func (c *CLI) run(ctx context.Context, args ...string) (string, error) {
	return c.runIn(ctx, c.localPath, args...)
}

// runIn は、指定ディレクトリで git コマンドを実行し、標準出力を返します。
//
// 標準出力と標準エラーを分けて受け取るのが要点です。まとめて取ると、git が出す警告
// （detached HEAD の注意書きなど）が差分の本文に混ざり、そのまま AI へ渡ってしまいます。
func (c *CLI) runIn(ctx context.Context, dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // args はこのパッケージが組み立て、ref は resolve が検証済み
	cmd.Dir = dir
	cmd.Env = c.env()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// キャンセル後に子プロセスが居座ってもここで打ち切ります（killGrace のコメント参照）。
	cmd.WaitDelay = killGrace

	c.settings.logger.DebugContext(ctx, "gitコマンドを実行します", "dir", dir, "args", args)

	if err := cmd.Run(); err != nil {
		// git は失敗時に URL 全体を stderr へ出すため、認証情報が混じっていると
		// そのままエラーとして呼び出し側のログや通知へ流れます。
		message := redactURL(strings.TrimSpace(stderr.String()))

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			c.settings.logger.DebugContext(ctx, "gitコマンドが失敗しました",
				"args", args, "exit", exitErr.ExitCode(), "stderr", message)
			return "", fmt.Errorf("git %s が終了コード %d で失敗しました: %s: %w",
				strings.Join(args, " "), exitErr.ExitCode(), message, err)
		}
		return "", fmt.Errorf("git %s の実行に失敗しました: %w", strings.Join(args, " "), err)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// env は、SSH 鍵を指定した GIT_SSH_COMMAND を加えた環境変数を返します。
func (c *CLI) env() []string {
	env := os.Environ()
	if c.settings.sshKeyPath == "" {
		return env
	}

	// GIT_SSH_COMMAND はシェル経由で解釈されるため、パスをクォートしてから埋め込みます。
	// -F /dev/null で ssh_config を無視し、実行環境ごとの設定差に左右されないようにします。
	// ホストキー検証は ssh 既定の known_hosts に委ねます。
	sshCommand := fmt.Sprintf("ssh -i %s -F /dev/null", quotePathForShell(c.settings.sshKeyPath))
	return append(env, "GIT_SSH_COMMAND="+sshCommand)
}
