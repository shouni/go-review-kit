package review

import (
	"errors"
	"slices"
	"testing"
)

const validReportJSON = `{
	"title": "レビュー結果",
	"summary": "概ね良好です。",
	"verdict": {"decision": "Minor", "reason": "軽微な指摘が1件あります。"},
	"findings": [
		{"severity": "Minor", "file": "main.go", "line": 12, "excerpt": "x := 1", "message": "未使用の変数です。", "suggestion": "削除してください。"}
	]
}`

func TestParseReport(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{
			name:  "正常なレポート",
			input: validReportJSON,
		},
		{
			name:  "findings が空でも成立する",
			input: `{"title":"t","summary":"s","verdict":{"decision":"None","reason":"問題なし"},"findings":[]}`,
		},
		{
			name:    "空入力",
			input:   "   ",
			wantErr: ErrEmptyResponse,
		},
		{
			name:    "JSON として壊れている",
			input:   `{"title": `,
			wantErr: ErrInvalidReport,
		},
		{
			name:    "title が空",
			input:   `{"title":"","summary":"s","verdict":{"decision":"None","reason":"r"},"findings":[]}`,
			wantErr: ErrInvalidReport,
		},
		{
			name:    "未知の decision",
			input:   `{"title":"t","summary":"s","verdict":{"decision":"Critical","reason":"r"},"findings":[]}`,
			wantErr: ErrInvalidReport,
		},
		{
			name:    "未知の severity",
			input:   `{"title":"t","summary":"s","verdict":{"decision":"None","reason":"r"},"findings":[{"severity":"Trivial","file":"a.go","excerpt":"x","message":"m"}]}`,
			wantErr: ErrInvalidReport,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := ParseReport([]byte(tt.input))

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("%v を期待しましたが: %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
			if got.Title == "" {
				t.Fatal("title がデコードされていません")
			}
		})
	}
}

func TestParseReportDecodesFields(t *testing.T) {
	got, _, err := ParseReport([]byte(validReportJSON))
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}

	if got.Verdict.Decision != DecisionMinor {
		t.Errorf("decision = %q, want %q", got.Verdict.Decision, DecisionMinor)
	}
	if len(got.Findings) != 1 {
		t.Fatalf("findings 件数 = %d, want 1", len(got.Findings))
	}

	finding := got.Findings[0]
	if finding.Severity != SeverityMinor {
		t.Errorf("severity = %q, want %q", finding.Severity, SeverityMinor)
	}
	if finding.File != "main.go" || finding.Line != 12 {
		t.Errorf("位置が一致しません: %s:%d", finding.File, finding.Line)
	}
	if finding.Suggestion == "" {
		t.Error("suggestion がデコードされていません")
	}
}

func TestReportCount(t *testing.T) {
	report := Report{
		Findings: []Finding{
			{Severity: SeverityBlocker},
			{Severity: SeverityMinor},
			{Severity: SeverityMinor},
		},
	}

	tests := []struct {
		severity Severity
		want     int
	}{
		{SeverityBlocker, 1},
		{SeverityMajor, 0},
		{SeverityMinor, 2},
	}

	for _, tt := range tests {
		if got := report.Count(tt.severity); got != tt.want {
			t.Errorf("Count(%q) = %d, want %d", tt.severity, got, tt.want)
		}
	}
}

func TestSeverityAndDecisionValidity(t *testing.T) {
	for _, s := range Severities() {
		if !s.Valid() {
			t.Errorf("Severities() が返す %q が Valid ではありません", s)
		}
	}
	for _, d := range Decisions() {
		if !d.Valid() {
			t.Errorf("Decisions() が返す %q が Valid ではありません", d)
		}
	}

	if Severity("None").Valid() {
		t.Error("None は Severity としては無効であるべきです")
	}
	if !DecisionNone.Valid() {
		t.Error("None は Decision としては有効であるべきです")
	}
}

// 文字列版が型付き版の写しであること。
//
// AI SDK の出力スキーマは列挙値を []string で要求します。詰め替えが利用側にあると
// レビュアーの実装ごとに写しが増え、値を足したときにスキーマと検証が食い違います。
func TestEnumStringsMirrorTypedValues(t *testing.T) {
	t.Parallel()

	wantSeverities := make([]string, 0, len(Severities()))
	for _, s := range Severities() {
		wantSeverities = append(wantSeverities, string(s))
	}
	if got := SeverityStrings(); !slices.Equal(got, wantSeverities) {
		t.Errorf("SeverityStrings() = %v, want %v", got, wantSeverities)
	}

	wantDecisions := make([]string, 0, len(Decisions()))
	for _, d := range Decisions() {
		wantDecisions = append(wantDecisions, string(d))
	}
	if got := DecisionStrings(); !slices.Equal(got, wantDecisions) {
		t.Errorf("DecisionStrings() = %v, want %v", got, wantDecisions)
	}
}

// 返したスライスを書き換えられても、次の呼び出しに影響しないこと。
// 呼び出し側はこれをスキーマへ直接渡すので、共有されていると危険です。
func TestEnumStringsReturnFreshSlices(t *testing.T) {
	t.Parallel()

	first := SeverityStrings()
	if len(first) == 0 {
		t.Fatal("列挙が空です")
	}
	first[0] = "書き換え"

	if second := SeverityStrings(); second[0] == "書き換え" {
		t.Error("SeverityStrings() が内部のスライスを共有しています")
	}
}

// 指摘は重大度の重い順に並ぶこと。同じ重大度の中では元の順序を保つこと
// （モデルが並べたファイル順・行順を崩さないため）。
func TestReportSortFindings(t *testing.T) {
	t.Parallel()

	report := Report{Findings: []Finding{
		{Severity: SeverityMinor, File: "a.go"},
		{Severity: SeverityBlocker, File: "b.go"},
		{Severity: SeverityMinor, File: "c.go"},
		{Severity: "Unknown", File: "d.go"},
		{Severity: SeverityMajor, File: "e.go"},
	}}
	report.SortFindings()

	want := []string{"b.go", "e.go", "a.go", "c.go", "d.go"}
	for i, file := range want {
		if report.Findings[i].File != file {
			t.Fatalf("findings[%d].File = %q, want %q（順序: %v）", i, report.Findings[i].File, file, files(report))
		}
	}
}

// 指摘が無くても落ちないこと。
func TestReportSortFindingsOnEmpty(t *testing.T) {
	t.Parallel()

	report := Report{}
	report.SortFindings()
	if len(report.Findings) != 0 {
		t.Errorf("findings = %d, want 0", len(report.Findings))
	}
}

func files(r Report) []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.File)
	}
	return out
}
