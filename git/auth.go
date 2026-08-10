package git

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// sshAuth は、リポジトリURLに応じた go-git の認証方式を組み立てます。
//
// HTTP/HTTPS の場合は認証なし（nil）を返します。go-git は nil の場合に匿名アクセスを
// 試みるため、公開リポジトリはそのまま扱えます。
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
// HTTP/HTTPS を先に除外するのは、Basic 認証入りの URL（https://user@host/...）に @ が
// 含まれ、scp 形式と取り違えられるためです。
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
