package gemini

import (
	geminiclient "github.com/shouni/go-gemini-client/gemini"

	"github.com/shouni/go-review-kit/review"
)

// reportSchema は、review.Report に対応する構造化出力スキーマです。
//
// 列挙値は review パッケージの定義から組み立てます。旧実装はスキーマ側にも重大度の
// 文字列配列を持っていたため、値を足したときに検証側と食い違う余地がありました。
//
// findings に evidence が無いのは同期漏れではありません。Evidence は作業ディレクトリを
// 調べる WorkspaceReviewer が「どこを見て判断したか」を残すフィールドで、差分しか
// 見ていない単発レビュアーに出力させると根拠の捏造を促すことになります。
func reportSchema() *geminiclient.Schema {
	return &geminiclient.Schema{
		Type: geminiclient.TypeObject,
		Properties: map[string]*geminiclient.Schema{
			"title":   {Type: geminiclient.TypeString},
			"summary": {Type: geminiclient.TypeString},
			"verdict": {
				Type: geminiclient.TypeObject,
				Properties: map[string]*geminiclient.Schema{
					"decision": {Type: geminiclient.TypeString, Enum: decisionEnum()},
					"reason":   {Type: geminiclient.TypeString},
				},
				Required: []string{"decision", "reason"},
			},
			"findings": {
				Type: geminiclient.TypeArray,
				Items: &geminiclient.Schema{
					Type: geminiclient.TypeObject,
					Properties: map[string]*geminiclient.Schema{
						"severity":   {Type: geminiclient.TypeString, Enum: severityEnum()},
						"file":       {Type: geminiclient.TypeString},
						"line":       {Type: geminiclient.TypeInteger},
						"excerpt":    {Type: geminiclient.TypeString},
						"message":    {Type: geminiclient.TypeString},
						"suggestion": {Type: geminiclient.TypeString},
					},
					Required: []string{"severity", "file", "excerpt", "message"},
				},
			},
		},
		Required: []string{"title", "summary", "verdict", "findings"},
	}
}

// severityEnum は、findings[].severity が取りうる値を返します。
func severityEnum() []string {
	values := review.Severities()
	enum := make([]string, 0, len(values))
	for _, v := range values {
		enum = append(enum, string(v))
	}
	return enum
}

// decisionEnum は、verdict.decision が取りうる値を返します。
func decisionEnum() []string {
	values := review.Decisions()
	enum := make([]string, 0, len(values))
	for _, v := range values {
		enum = append(enum, string(v))
	}
	return enum
}
