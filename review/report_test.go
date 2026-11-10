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
			name:  "valid report",
			input: validReportJSON,
		},
		{
			name:  "empty findings is valid",
			input: `{"title":"t","summary":"s","verdict":{"decision":"None","reason":"問題なし"},"findings":[]}`,
		},
		{
			name:    "empty input",
			input:   "   ",
			wantErr: ErrEmptyResponse,
		},
		{
			name:    "broken JSON",
			input:   `{"title": `,
			wantErr: ErrInvalidReport,
		},
		{
			name:    "empty title",
			input:   `{"title":"","summary":"s","verdict":{"decision":"None","reason":"r"},"findings":[]}`,
			wantErr: ErrInvalidReport,
		},
		{
			name:    "unknown decision",
			input:   `{"title":"t","summary":"s","verdict":{"decision":"Critical","reason":"r"},"findings":[]}`,
			wantErr: ErrInvalidReport,
		},
		{
			name:    "unknown severity",
			input:   `{"title":"t","summary":"s","verdict":{"decision":"None","reason":"r"},"findings":[{"severity":"Trivial","file":"a.go","excerpt":"x","message":"m"}]}`,
			wantErr: ErrInvalidReport,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := ParseReport([]byte(tt.input))

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("want %v, got: %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Title == "" {
				t.Fatal("title was not decoded")
			}
		})
	}
}

func TestParseReportDecodesFields(t *testing.T) {
	got, _, err := ParseReport([]byte(validReportJSON))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Verdict.Decision != DecisionMinor {
		t.Errorf("decision = %q, want %q", got.Verdict.Decision, DecisionMinor)
	}
	if len(got.Findings) != 1 {
		t.Fatalf("findings count = %d, want 1", len(got.Findings))
	}

	finding := got.Findings[0]
	if finding.Severity != SeverityMinor {
		t.Errorf("severity = %q, want %q", finding.Severity, SeverityMinor)
	}
	if finding.File != "main.go" || finding.Line != 12 {
		t.Errorf("location does not match: %s:%d", finding.File, finding.Line)
	}
	if finding.Suggestion == "" {
		t.Error("suggestion was not decoded")
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
			t.Errorf("%q returned by Severities() is not Valid", s)
		}
	}
	for _, d := range Decisions() {
		if !d.Valid() {
			t.Errorf("%q returned by Decisions() is not Valid", d)
		}
	}

	if Severity("None").Valid() {
		t.Error("None should be invalid as a Severity")
	}
	if !DecisionNone.Valid() {
		t.Error("None should be valid as a Decision")
	}
}

// The string forms mirror the typed forms.
//
// AI SDK output schemas want enum values as []string. If the conversion lived on the consumer's
// side, every reviewer implementation would add a copy, and adding a value would make the schema
// and the validation disagree.
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

// Mutating a returned slice does not affect the next call.
// Callers hand it straight to a schema, so sharing it would be dangerous.
func TestEnumStringsReturnFreshSlices(t *testing.T) {
	t.Parallel()

	first := SeverityStrings()
	if len(first) == 0 {
		t.Fatal("the enum is empty")
	}
	first[0] = "overwritten"

	if second := SeverityStrings(); second[0] == "overwritten" {
		t.Error("SeverityStrings() shares its internal slice")
	}
}

// Findings are ordered by severity, heaviest first, and keep their original order within the same
// severity (so as not to break the file and line order the model produced).
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
			t.Fatalf("findings[%d].File = %q, want %q (order: %v)", i, report.Findings[i].File, file, files(report))
		}
	}
}

// Sorting does not crash when there are no findings.
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
