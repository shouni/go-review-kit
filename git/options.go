// Package git は、review.DiffSource の Git 実装を 2 種類提供します。
//
//   - GoGit: go-git による純 Go 実装。git バイナリを必要とせず、実行のたびに作業ディレクトリを
//     破棄するため、サーバーレスのような使い捨て環境に向きます。
//   - CLI: ローカルの git コマンドを呼ぶ実装。チェックアウトを再利用して差分取得が速いため、
//     永続的な作業ディレクトリを持てるローカル開発や CI に向きます。
//
// どちらを使うかは呼び出し側が選びます（本パッケージは選択しません）。
package git

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
)

// DefaultContextLines は、CLI 実装が git diff に渡す既定の前後行数です。
const DefaultContextLines = 10

// settings は、両アダプター共通の設定です。
type settings struct {
	sshKeyPath   string
	base         string
	logger       *slog.Logger
	contextLines int
	dirNamer     func(repoURL string) string
}

func newSettings(opts ...Option) (settings, error) {
	s := settings{
		logger:       slog.Default(),
		contextLines: DefaultContextLines,
		dirNamer:     RepoDirName,
	}
	for _, opt := range opts {
		// 条件付きでオプションを組み立てる呼び出し側は nil を混ぜがちです。
		// パニックさせるほどの誤りではないので読み飛ばします。
		if opt == nil {
			continue
		}
		if err := opt(&s); err != nil {
			return settings{}, err
		}
	}
	return s, nil
}

// Option はアダプターおよび Factory の任意設定です。
//
// 生成時にだけ適用し、以降アダプターの設定は変わりません。セッターで後から書き換え
// られると、実行中に設定が変わりうる状態になります。検証が必要な値を扱えるよう
// error を返す形にしています。
type Option func(*settings) error

// WithSSHKey は、SSH 認証に使う秘密鍵のパスを設定します。
// 未設定の場合、SSH 形式のリポジトリURLに対する認証は行われません。
func WithSSHKey(path string) Option {
	return func(s *settings) error {
		s.sshKeyPath = path
		return nil
	}
}

// WithBase は、後始末の際に戻る基準参照を設定します（CLI 実装が使用します）。
// Factory 経由で利用する場合は Request.Base が自動的に設定されます。
func WithBase(ref string) Option {
	return func(s *settings) error {
		s.base = ref
		return nil
	}
}

// WithLogger は、アダプターが使うロガーを差し替えます。
func WithLogger(logger *slog.Logger) Option {
	return func(s *settings) error {
		if logger != nil {
			s.logger = logger
		}
		return nil
	}
}

// WithContextLines は、差分に含める前後の行数を設定します（CLI 実装のみ有効）。
// 負の値はエラーになります。
func WithContextLines(n int) Option {
	return func(s *settings) error {
		if n < 0 {
			return fmt.Errorf("git: 前後行数に負の値は指定できません: %d", n)
		}
		s.contextLines = n
		return nil
	}
}

// WithDirNamer は、リポジトリURLから作業ディレクトリ名を決める関数を差し替えます
// （Factory のみ有効）。
func WithDirNamer(fn func(repoURL string) string) Option {
	return func(s *settings) error {
		if fn == nil {
			return fmt.Errorf("git: dirNamer に nil は指定できません")
		}
		s.dirNamer = fn
		return nil
	}
}

// RepoDirName は、リポジトリURLから作業ディレクトリ名を組み立てる既定の実装です。
//
// スキーム・認証情報・末尾の .git を落とし、パス区切りなど扱いにくい文字をハイフンへ
// 畳み込みます。同じURLからは常に同じ名前が得られるため、CLI 実装がチェックアウトを
// 再利用できます。
func RepoDirName(repoURL string) string {
	name := strings.TrimSpace(repoURL)
	if idx := strings.Index(name, "://"); idx >= 0 {
		name = name[idx+len("://"):]
	}
	// 認証情報はホストの前にしか現れないので、最初の "/" より手前の "@" だけを見ます。
	// LastIndex や Index で丸ごと探すと、パス側の "@"（例: owner/repo@v2）を認証情報と
	// 誤認して、ホスト名ごと捨てた "v2" のような名前になり、別リポジトリと衝突します。
	if slash := strings.Index(name, "/"); slash != 0 {
		host := name
		if slash > 0 {
			host = name[:slash]
		}
		if at := strings.Index(host, "@"); at >= 0 {
			name = name[at+1:]
		}
	}
	name = strings.TrimSuffix(name, ".git")

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	cleaned := strings.Trim(b.String(), "-.")
	if cleaned == "" {
		cleaned = "repo"
	}

	// 畳み込みだけでは別のリポジトリが同じ名前になります
	// （group/sub/proj と group-sub-proj はどちらも group-sub-proj）。
	// **同じディレクトリを別リポジトリが奪い合うと、片方のブランチの内容が
	// もう片方のレビュー結果として公開されます。** 畳み込む前の形から短いハッシュを
	// 足して区別します。
	//
	// ハッシュを取る前に scp 形式の ":" を "/" へ寄せます。同じリポジトリを
	// git@host:owner/repo と https://host/owner/repo のどちらで指定しても
	// 同じ作業ディレクトリへ落ち着く性質を保つためです。
	sum := sha256.Sum256([]byte(strings.ReplaceAll(name, ":", "/")))
	return cleaned + "-" + hex.EncodeToString(sum[:])[:8]
}
