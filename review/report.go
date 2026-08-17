package review

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Severity は、個々の指摘の重大度です。
type Severity string

// 指摘の重大度です。全レビューモードで共通のラベルとして扱います。
const (
	SeverityBlocker Severity = "Blocker"
	SeverityMajor   Severity = "Major"
	SeverityMinor   Severity = "Minor"
)

// Decision は、レビュー全体の判定です。Severity に「問題なし」を表す DecisionNone を加えたものです。
type Decision string

// レビュー全体の判定です。
const (
	DecisionBlocker Decision = Decision(SeverityBlocker)
	DecisionMajor   Decision = Decision(SeverityMajor)
	DecisionMinor   Decision = Decision(SeverityMinor)
	DecisionNone    Decision = "None"
)

// Severities は、取りうる重大度を重い順に返します。
//
// AI の出力スキーマ（gemini）の列挙値と、受信したレポートの検証は、どちらもこの
// 一箇所を参照します。旧実装はスキーマ側にだけ文字列の配列を持っていたため、値を増やすと
// 検証側と食い違う余地がありました。
func Severities() []Severity {
	return []Severity{SeverityBlocker, SeverityMajor, SeverityMinor}
}

// Decisions は、取りうる判定を重い順に返します。
func Decisions() []Decision {
	return []Decision{DecisionBlocker, DecisionMajor, DecisionMinor, DecisionNone}
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
// 旧実装はこの内容を JSON 文字列のまま層をまたいで受け渡し、公開側で map[string]any へ
// 再パースしていました。ここでは AI アダプターが一度だけデコードして型を与え、以降は
// この構造体で扱います。
type Report struct {
	Title    string    `json:"title"`
	Summary  string    `json:"summary"`
	Verdict  Verdict   `json:"verdict"`
	Findings []Finding `json:"findings"`
}

// ParseReport は、AI が返した JSON を Report へデコードし、検証します。
func ParseReport(data []byte) (Report, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return Report{}, ErrEmptyResponse
	}

	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, fmt.Errorf("%w: JSONとして解釈できません: %w", ErrInvalidReport, err)
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
