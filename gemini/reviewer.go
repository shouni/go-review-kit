// Package gemini は、Google Gemini を使う review.Reviewer の実装を提供します。
package gemini

import (
	"context"
	"fmt"
	"strings"

	geminiclient "github.com/shouni/go-gemini-client/gemini"

	"github.com/shouni/go-review-kit/review"
)

// DefaultMaxRetries は、一時的なネットワークエラーに備えた既定のリトライ回数です。
const DefaultMaxRetries = uint64(1)

// defaultLocationID は、ProjectID 指定時に LocationID を省略した場合の既定値です。
const defaultLocationID = "global"

// Options は、Reviewer の初期化パラメータです。
type Options struct {
	// ProjectID は GCP プロジェクトID です。APIKey 未指定時に必須です。
	ProjectID string
	// LocationID は GCP ロケーションID です。未指定の場合は "global" を使います。
	LocationID string
	// APIKey は Gemini API キーです。指定された場合は ProjectID より優先されます。
	APIKey string
	// MaxRetries はリトライ回数です。0 の場合は DefaultMaxRetries を使います。
	MaxRetries uint64
}

// Reviewer は、Gemini に構造化出力でレビューを依頼する review.Reviewer 実装です。
type Reviewer struct {
	client geminiclient.Generator
}

// 実装がポートを満たすことをコンパイル時に確認します。
var _ review.Reviewer = (*Reviewer)(nil)

// New は Options からクライアントを初期化して Reviewer を生成します。
func New(ctx context.Context, opts Options) (*Reviewer, error) {
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}

	client, err := geminiclient.NewClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("gemini: クライアントの初期化に失敗しました: %w", err)
	}
	return &Reviewer{client: client}, nil
}

// NewWithGenerator は、既に構築済みのクライアントから Reviewer を生成します。
// 認証やリトライ方針を呼び出し側で組み立てたい場合や、テストで差し替える場合に使います。
func NewWithGenerator(client geminiclient.Generator) (*Reviewer, error) {
	if client == nil {
		return nil, fmt.Errorf("gemini: クライアントが nil です")
	}
	return &Reviewer{client: client}, nil
}

// buildConfig は Options から gemini.Config を組み立てます。
func buildConfig(opts Options) (geminiclient.Config, error) {
	maxRetries := opts.MaxRetries
	if maxRetries == 0 {
		maxRetries = DefaultMaxRetries
	}
	cfg := geminiclient.Config{MaxRetries: maxRetries}

	switch {
	case opts.APIKey != "":
		cfg.APIKey = opts.APIKey
	case opts.ProjectID != "":
		cfg.ProjectID = opts.ProjectID
		cfg.LocationID = opts.LocationID
		if cfg.LocationID == "" {
			cfg.LocationID = defaultLocationID
		}
	default:
		return geminiclient.Config{}, fmt.Errorf("gemini: APIKey または ProjectID の指定が必要です")
	}

	return cfg, nil
}

// Review は、プロンプトを Gemini へ送り、レビュー結果を review.Report として返します。
//
// ResponseSchema で出力を文法レベルで制約するため、自由記述の Markdown に起因する
// 出力の揺れ（見出しレベルのズレ、コードフェンスの崩れなど）が起きません。デコードは
// ここで一度だけ行い、以降の層へは型の付いた Report を渡します。
func (r *Reviewer) Review(ctx context.Context, model, prompt string) (review.Report, error) {
	if strings.TrimSpace(model) == "" {
		return review.Report{}, fmt.Errorf("gemini: モデル名が空です")
	}
	if strings.TrimSpace(prompt) == "" {
		return review.Report{}, fmt.Errorf("gemini: プロンプトが空です")
	}

	// 添付は無いが、プロンプトと生成オプションを同時に渡せる入口はこちらです。
	// GenerateWithParts を使うと Part を組み立てるためだけに genai SDK を import する
	// ことになるため使いません（SDK は go-gemini-client の内側に閉じておきます）。
	resp, err := r.client.GenerateWithAttachments(ctx, model, prompt, nil, geminiclient.GenerateOptions{
		ResponseMIMEType: "application/json",
		ResponseSchema:   reportSchema(),
	})
	if err != nil {
		return review.Report{}, fmt.Errorf("gemini: API呼び出しに失敗しました (model: %s): %w", model, err)
	}
	if resp == nil {
		return review.Report{}, review.ErrEmptyResponse
	}

	report, err := review.ParseReport([]byte(resp.Text))
	if err != nil {
		return review.Report{}, fmt.Errorf("gemini: %w", err)
	}
	return report, nil
}
