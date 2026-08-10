package publish

import (
	"embed"
	"fmt"
	"html/template"
	"io"

	"github.com/shouni/go-prompt-kit/md/jsonconverter"
	mdports "github.com/shouni/go-prompt-kit/md/ports"
	"github.com/shouni/go-prompt-kit/md/renderer"
	"github.com/shouni/go-prompt-kit/md/runner"
)

//go:embed assets/report.html assets/report.css
var assets embed.FS

// TemplateConverter は、go-prompt-kit の jsonconverter を包んだ既定の Converter です。
type TemplateConverter struct {
	document mdports.Runner
}

// 実装が Converter を満たすことをコンパイル時に確認します。
var _ Converter = (*TemplateConverter)(nil)

// NewConverter は、同梱テンプレートを使う TemplateConverter を生成します。
func NewConverter() (*TemplateConverter, error) {
	return NewConverterWithTemplate(nil, "")
}

// NewConverterWithTemplate は、テンプレートと追加 CSS を差し替えた TemplateConverter を
// 生成します。いずれもゼロ値を渡すと同梱のものを使います。
//
// テンプレートは reportView を JSON 化したマップを受け取ります
// （title / summary / verdict / findings / repo_url / base_branch / feature_branch / reviewed_at）。
func NewConverterWithTemplate(tmpl *template.Template, extraCSS string) (*TemplateConverter, error) {
	if tmpl == nil {
		parsed, err := parseAsset("assets/report.html")
		if err != nil {
			return nil, err
		}
		tmpl = parsed
	}

	if extraCSS == "" {
		css, err := assets.ReadFile("assets/report.css")
		if err != nil {
			return nil, fmt.Errorf("publish: 同梱CSSの読み込みに失敗しました: %w", err)
		}
		extraCSS = string(css)
	}

	// レビュー固有のスタイルは既定のスタイルシートの後ろへ足します。フラグメント側へ
	// <style> を書くと body 内にスタイルが混ざるため、renderer へ渡して <head> に集約します。
	r, err := renderer.NewRenderer(renderer.WithExtraCSS(extraCSS))
	if err != nil {
		return nil, fmt.Errorf("publish: rendererの初期化に失敗しました: %w", err)
	}

	return &TemplateConverter{
		document: runner.NewDocumentRunner(jsonconverter.New(tmpl), r),
	}, nil
}

// Run は、レビュー結果の JSON を HTML ドキュメントへ変換します。
func (c *TemplateConverter) Run(content []byte) (io.Reader, error) {
	// タイトルは JSON 内の "title" から抽出されるため、ここでは空文字を渡します。
	buffer, err := c.document.Run("", content)
	if err != nil {
		return nil, fmt.Errorf("publish: JSONからHTMLへの変換に失敗しました: %w", err)
	}
	return buffer, nil
}

func parseAsset(name string) (*template.Template, error) {
	body, err := assets.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("publish: 同梱テンプレートの読み込みに失敗しました (%s): %w", name, err)
	}

	tmpl, err := template.New(name).Parse(string(body))
	if err != nil {
		return nil, fmt.Errorf("publish: 同梱テンプレートの解析に失敗しました (%s): %w", name, err)
	}
	return tmpl, nil
}
