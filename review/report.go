package review

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Severity は、個々の指摘の重大度です。全レビューモードで共通のラベルとして扱います。
type Severity string

// 並びがそのまま重さの定義です（Severities / SortFindings が参照します）。
const (
	SeverityBlocker Severity = "Blocker"
	SeverityMajor   Severity = "Major"
	SeverityMinor   Severity = "Minor"
)

// Decision は、レビュー全体の判定です。Severity に「問題なし」を表す DecisionNone を加えたものです。
type Decision string

// 値は受け取り側の表示分岐と、そこから保存される記録に現れます。
const (
	DecisionBlocker Decision = Decision(SeverityBlocker)
	DecisionMajor   Decision = Decision(SeverityMajor)
	DecisionMinor   Decision = Decision(SeverityMinor)
	DecisionNone    Decision = "None"
)

// 列挙値の出どころはこの 4 つだけです。
//
// 利用側が組み立てる AI の出力スキーマの列挙値と、受信したレポートの検証は、どちらも
// ここを参照します。**別に写しを持つと、値を足したときに食い違い、モデルはスキーマ上
// 正当な値を返すのにデコードで弾かれます**（症状は全レビューの失敗です）。
// AI SDK のスキーマは列挙値を []string で要求するため、文字列版もここで配ります。

// Severities は、取りうる重大度を重い順に返します。
func Severities() []Severity {
	return []Severity{SeverityBlocker, SeverityMajor, SeverityMinor}
}

// Decisions は、取りうる判定を重い順に返します。
func Decisions() []Decision {
	return []Decision{DecisionBlocker, DecisionMajor, DecisionMinor, DecisionNone}
}

// SeverityStrings は、取りうる重大度を文字列で重い順に返します。
func SeverityStrings() []string {
	return toStrings(Severities())
}

// DecisionStrings は、取りうる判定を文字列で重い順に返します。
func DecisionStrings() []string {
	return toStrings(Decisions())
}

// toStrings は、文字列を基底型とする列挙のスライスを []string へ写します。
func toStrings[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, string(v))
	}
	return out
}

// Valid は、既知の重大度かどうかを返します。
func (s Severity) Valid() bool { return slices.Contains(Severities(), s) }

// Valid は、既知の判定かどうかを返します。
func (d Decision) Valid() bool { return slices.Contains(Decisions(), d) }

// Verdict は、レビュー全体の判定とその理由です。
type Verdict struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
}

// Finding は、個々の指摘です。
type Finding struct {
	Severity   Severity `json:"severity"`
	File       string   `json:"file"`
	Line       int      `json:"line,omitempty"`
	Excerpt    string   `json:"excerpt"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion,omitempty"`
	// Evidence は、指摘の根拠としてレビュアーが参照した箇所です（任意）。
	// 作業ディレクトリを調べる WorkspaceReviewer 実装が、差分の外のどのファイルを
	// 確認して判断したかを残す用途を想定しています。検証はしません（レビュアーの
	// 自己申告であり、欠けても成果物は成立するためです）。
	Evidence []string `json:"evidence,omitempty"`
}

// Report は、AI が返すレビュー結果です。
//
// JSON 文字列のまま層をまたいで受け渡さないのは、公開側で map[string]any へ再パースする
// ことになり、型も検証も失われるためです。AI アダプターが一度だけデコードし、以降は
// この構造体で扱います。
type Report struct {
	Title    string    `json:"title"`
	Summary  string    `json:"summary"`
	Verdict  Verdict   `json:"verdict"`
	Findings []Finding `json:"findings"`
}

// ParseReport は、AI が返した JSON を Report へデコードし、検証します。
//
// そのまま解釈できなかった場合にかぎり SanitizeJSON を通してもう一度試します。
// 構造化出力を指定してもモデルはフェンスや後書き、エスケープし忘れたバックスラッシュを
// 混ぜることがあり、**応答を返しきったあとの崩れなので API の再試行では直りません。**
// 補修しても解釈できなければ、元の入力に対するエラーをそのまま返します。
func ParseReport(data []byte) (Report, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return Report{}, ErrEmptyResponse
	}

	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		cleaned := SanitizeJSON(data)
		if len(cleaned) == len(data) && string(cleaned) == string(data) {
			return Report{}, fmt.Errorf("%w: JSONとして解釈できません: %w", ErrInvalidReport, err)
		}
		if err2 := json.Unmarshal(cleaned, &report); err2 != nil {
			// 補修後のエラーではなく元のエラーを返します。呼び出し側が見たいのは
			// モデルが実際に返したものの壊れ方で、補修の途中経過ではありません。
			return Report{}, fmt.Errorf("%w: JSONとして解釈できません: %w", ErrInvalidReport, err)
		}
	}

	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

// Validate は、レポートが最低限の体裁を満たしているかを検証します。
//
// 検証するのは列挙値と、欠けると成果物が成立しない項目だけです。AI の出力はスキーマで
// 制約済みであり、ここで細かく縛るとモデルの妥当な出力まで弾いてしまうためです。
func (r Report) Validate() error {
	if strings.TrimSpace(r.Title) == "" {
		return fmt.Errorf("%w: title が空です", ErrInvalidReport)
	}
	if !r.Verdict.Decision.Valid() {
		return fmt.Errorf("%w: 未知の verdict.decision です: %q", ErrInvalidReport, r.Verdict.Decision)
	}
	for i, finding := range r.Findings {
		if !finding.Severity.Valid() {
			return fmt.Errorf("%w: findings[%d] の severity が未知です: %q", ErrInvalidReport, i, finding.Severity)
		}
	}
	return nil
}

// SortFindings は、指摘を重大度の重い順に並べ替えます。
//
// プロンプトでモデルに重い順を指示することはできますが、守られる保証はありません。
// 並びは読む人の目に最初に入るものを決めるので、**受け取った側で確定させます。**
//
// 同じ重大度の中では元の順序を保ちます。モデルはたいていファイル順・行順に並べており、
// その中での読みやすさを崩す理由が無いためです。
//
// 重さの定義は Severities() の並びです。**呼び出し側で順位表を持たないでください。**
// 重大度を足したときに、こちらだけ直して食い違います。
func (r *Report) SortFindings() {
	order := Severities()
	rank := make(map[Severity]int, len(order))
	for i, severity := range order {
		rank[severity] = i
	}

	// 未知の重大度は末尾へ回します。Validate を通っていれば現れませんが、
	// 検証前に呼ばれても並びが壊れないようにします。
	unknown := len(order)
	rankOf := func(s Severity) int {
		if i, ok := rank[s]; ok {
			return i
		}
		return unknown
	}

	slices.SortStableFunc(r.Findings, func(a, b Finding) int {
		return rankOf(a.Severity) - rankOf(b.Severity)
	})
}

// Count は、指定した重大度の指摘件数を返します。通知の文面などに利用できます。
func (r Report) Count(severity Severity) int {
	n := 0
	for _, finding := range r.Findings {
		if finding.Severity == severity {
			n++
		}
	}
	return n
}
