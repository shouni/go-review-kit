package git

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/shouni/go-review-kit/review"
)

// sshAuth は、リポジトリURLに応じた go-git の認証方式を組み立てます。
//
// SSH 形式でない場合は認証なし（nil）を返します。ここへ来るのはローカルパスだけです
// （http(s) は validateRepoURL が先に断ります）。
func sshAuth(repoURL, sshKeyPath string) (transport.AuthMethod, error) {
	if !isSSHURL(repoURL) {
		return nil, nil
	}

	username, err := sshUsername(repoURL)
	if err != nil {
		return nil, err
	}

	keyPath, err := expandTilde(sshKeyPath)
	if err != nil {
		return nil, fmt.Errorf("SSHキーパスの展開に失敗しました: %w", err)
	}
	if keyPath == "" {
		return nil, fmt.Errorf("SSH形式のリポジトリURLですが、SSHキーが設定されていません: %s", repoURL)
	}

	key, err := os.ReadFile(keyPath) //nolint:gosec // 鍵のパスは呼び出し側が設定する
	if err != nil {
		return nil, fmt.Errorf("SSHキーファイルの読み込みに失敗しました (%s): %w", keyPath, err)
	}

	// パスフレーズなしの鍵を前提とします。
	// HostKeyCallback は設定しません。go-git 既定の known_hosts 検証
	// （SSH_KNOWN_HOSTS、なければ ~/.ssh/known_hosts と /etc/ssh/ssh_known_hosts）に委ねます。
	auth, err := ssh.NewPublicKeys(username, key, "")
	if err != nil {
		return nil, fmt.Errorf("SSH認証キーのロードに失敗しました: %w", err)
	}
	return auth, nil
}

// isSSHURL は、リポジトリURLが SSH 形式かどうかを判定します。
//
// http(s) を先に除外するのは、認証情報入りの URL（https://user@host/...）に @ が
// 含まれ、scp 形式と取り違えられるためです。validateRepoURL が先に断るので通常は
// 到達しませんが、この関数単体でも正しく判定できるようにしておきます。
func isSSHURL(repoURL string) bool {
	if strings.HasPrefix(repoURL, "http://") || strings.HasPrefix(repoURL, "https://") {
		return false
	}
	return strings.HasPrefix(repoURL, "ssh://") || strings.Contains(repoURL, "@")
}

// sshUsername は、リポジトリURLから SSH ユーザー名を取り出します。
func sshUsername(repoURL string) (string, error) {
	if strings.HasPrefix(repoURL, "ssh://") {
		u, err := url.Parse(repoURL)
		if err != nil {
			return "", fmt.Errorf("リポジトリURLの解析に失敗しました: %w", err)
		}
		if u.User != nil && u.User.Username() != "" {
			return u.User.Username(), nil
		}
		return "git", nil
	}

	// scp 形式（user@host:path）。コロンより前に @ があるものだけを受け付けます。
	at := strings.Index(repoURL, "@")
	colon := strings.Index(repoURL, ":")
	if at > 0 && colon > at {
		return repoURL[:at], nil
	}

	return "", fmt.Errorf("未対応のSSHリポジトリURL形式です: %s", repoURL)
}

// validateRepoURL は、このパッケージが扱えるリポジトリURLかを確かめます。
//
// 認証は SSH 鍵だけを扱います（WithSSHKey → GIT_SSH_COMMAND / go-git の PublicKeys）。
// http(s) には資格情報を渡す経路が無く、公開リポジトリへ匿名で繋がるだけなので、
// 「対応しているように見えて private では必ず失敗する」状態になります。ここで
// 明示的に断り、利用者が形式を直せるようにします。
//
// 受け付けるのは SSH 形式（git@host:owner/repo.git、ssh://[user@]host/path）と、
// ローカルパスです。ローカルパスは開発とテストで使います。
func validateRepoURL(repoURL string) error {
	trimmed := strings.TrimSpace(repoURL)
	if trimmed == "" {
		return fmt.Errorf("%w: リポジトリURLが空です", review.ErrUnsupportedRepoURL)
	}

	// "-" 始まりは git のオプションとして解釈されうるため拒否します。
	// 正当なリポジトリURLがこの形になることはありません。
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("%w: %q で始まる値はオプションと区別できません", review.ErrUnsupportedRepoURL, "-")
	}

	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return fmt.Errorf(
			"%w: http(s) は未対応です（認証は SSH 鍵のみを扱います）。"+
				"git@host:owner/repo.git または ssh://host/owner/repo.git の形式を使ってください: %s",
			review.ErrUnsupportedRepoURL, redactURL(trimmed))
	}

	return nil
}
