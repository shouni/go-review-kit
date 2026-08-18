// Package review は、AIレビューのドメイン型とポート（インターフェース）を定義します。
//
// このパッケージは本モジュール内の他のどのパッケージにも依存しません。アダプター（git）と
// オーケストレーター（pipeline）は、すべてこのパッケージの型とインターフェースだけを
// 介して結び付きます。レビュアー・プロンプト・公開先・通知先の実装は利用側が持ちます。
package review

import (
	"fmt"
	"strings"
)

// Request は、1 回のレビュー実行に必要な入力です。
//
// Base / Head はブランチ名に限らずコミットハッシュやタグも受け付けるため、フィールド名は
// ブランチに限定しない Base / Head としています。JSON タグが base_branch / feature_branch
// のままなのは、Cloud Tasks 等のペイロードとして流通している既存の形式を壊さないためです。
//
// Request は常に値として受け渡され、パイプラインの途中で書き換えられることはありません。
type Request struct {
	// JobID は、呼び出し側が持つ相関IDです（任意）。
	//
	// 本ライブラリはこの値を生成も解釈もせず、Publisher / Notifier へそのまま渡し、
	// ログ属性に載せるだけです。ジョブ基盤を持つ呼び出し側が、成果物の保存先や進行状況の
	// 記録先を自分で決められるようにするための素通し用のフィールドで、この値の書式や
	// 一意性は呼び出し側の責務です。
	JobID      string `json:"job_id,omitempty"`
	RepoURL    string `json:"repo_url"`       // GitリポジトリのURL (例: ssh://git@github.com/user/repo)
	Base       string `json:"base_branch"`    // 比較元の参照 (ブランチ・タグ・コミットハッシュ)
	Head       string `json:"feature_branch"` // 比較対象の参照 (ブランチ・タグ・コミットハッシュ)
	Mode       string `json:"mode"`           // レビューモード。解釈は PromptGenerator の実装に委ねます
	Model      string `json:"model_name"`     // 使用する AI モデル名
	StorageURI string `json:"storage_uri"`    // 保存先 (例: gs://bucket/path, s3://bucket/path)
	PublicURL  string `json:"public_url"`     // 閲覧用URL (https://...)
}

// NewRequest は Request を検証したうえで返します。
//
// 検証の本体を Validate に置いているのは、JSON からデコードして組み立てた Request にも
// 同じ検証を後から適用できるようにするためです。
func NewRequest(req Request) (Request, error) {
	if err := req.Validate(); err != nil {
		return Request{}, err
	}
	return req, nil
}

// Validate は必須項目が埋まっているかを検証し、不足があれば ErrInvalidRequest を包んだ
// エラーを返します。
//
// Mode と PublicURL は任意です。Mode の解釈は PromptGenerator の実装次第であり、空を
// 既定モードとして扱う実装もあり得るためです。PublicURL は結果の閲覧導線でしかなく、
// 無くてもレビュー自体は完結します。
func (r Request) Validate() error {
	var missing []string
	for _, field := range []struct {
		name  string
		value string
	}{
		{"repo_url", r.RepoURL},
		{"base_branch", r.Base},
		{"feature_branch", r.Head},
		{"model_name", r.Model},
		{"storage_uri", r.StorageURI},
	} {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.name)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: %s が未設定です", ErrInvalidRequest, strings.Join(missing, ", "))
	}
	return nil
}
