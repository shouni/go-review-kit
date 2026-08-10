package git

import (
	"fmt"
	"os/user"
	"path/filepath"
	"strings"
)

// refCandidate は、参照文字列の解決候補です。
type refCandidate struct {
	// ref は実際に解決を試みる文字列です。
	ref string
	// isBranch は、候補がリモート追跡ブランチかどうかを示します。
	// 後始末でブランチとしてチェックアウトすべきかの判断に使います。
	isBranch bool
}

// refCandidates は、参照文字列の解決候補をリモートブランチ優先の順で返します。
//
// 常に「リモートブランチ → コミット」の順に試すのが要点です。逆順にすると、チケット番号を
// そのままブランチ名にした数字のみのブランチ（例: 12345）が短縮ハッシュとして解決され、
// 意図しないコミットの差分を取ってしまいます。
func refCandidates(ref string) []refCandidate {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return nil
	}

	// 既に origin/ が付いている場合は、ブランチとしてのみ解釈します。
	if strings.HasPrefix(trimmed, remotePrefix) {
		return []refCandidate{{ref: trimmed, isBranch: true}}
	}

	return []refCandidate{
		{ref: remotePrefix + trimmed, isBranch: true},
		{ref: trimmed, isBranch: false},
	}
}

// remotePrefix は、リモート追跡ブランチの接頭辞です。
const remotePrefix = remoteName + "/"

// remoteName は、対象とするリモート名です。
const remoteName = "origin"

// localBranchName は、解決済みのリモート追跡ブランチ名からローカルブランチ名を取り出します。
func localBranchName(ref string) string {
	return strings.TrimPrefix(ref, remotePrefix)
}

// quotePathForShell は、シェルコマンド文字列へ埋め込むパスをシングルクォートで囲みます。
// パス内のシングルクォートもエスケープするため、コマンドインジェクションを防げます。
func quotePathForShell(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// expandTilde は、先頭の ~/ をホームディレクトリへ展開します。
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("ホームディレクトリの取得に失敗しました: %w", err)
	}
	return filepath.Join(current.HomeDir, path[len("~/"):]), nil
}
