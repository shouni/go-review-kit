package review

import (
	"errors"
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
			got, err := ParseReport([]byte(tt.input))

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
	got, err := ParseReport([]byte(validReportJSON))
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
