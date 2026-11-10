package review

import (
	"encoding/json"
	"strings"
	"testing"
)

// Input that can already be interpreted is not changed by a single byte.
// Changing it would let the repair affect the normal path too.
func TestSanitizeJSONLeavesValidInputAlone(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		`{"title":"a"}`,
		`[{"a":1},{"b":2}]`,
		`{"excerpt":"win\\path"}`,
		`{"excerpt":"改行\nタブ\t引用\""}`,
		`  {"title":"a"}  `,
	} {
		if got := string(SanitizeJSON([]byte(in))); got != in {
			t.Errorf("SanitizeJSON(%q) = %q, must not be changed", in, got)
		}
	}
}

// The breakages the model tends to mix in can be repaired.
func TestSanitizeJSONRepairs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "Markdown fence", in: "```json\n{\"title\":\"a\"}\n```"},
		{name: "trailing remarks", in: `{"title":"a"} 以上がレビュー結果です。`},
		{name: "extra closing bracket", in: `{"title":"a"}}`},
		{name: "leading preamble", in: `結果は次のとおりです: {"title":"a"}`},
		// ★ The shape actually hit. It appears when a regular expression from the source is quoted into an excerpt.
		{name: "unescaped backslash (\\d)", in: `{"excerpt":"regexp.MustCompile(\d+)"}`},
		{name: "unescaped backslash (space)", in: `{"excerpt":"path\ name"}`},
		// ★ A multi-line quote, with the newlines put in unescaped.
		{name: "raw newline", in: "{\"excerpt\":\"一行目\n二行目\"}"},
		{name: "raw tab", in: "{\"excerpt\":\"\tif err != nil {\"}"},
		{name: "raw newline and fence", in: "```json\n{\"excerpt\":\"一行目\n二行目\"}\n```"},
		{name: "raw newline and backslash", in: "{\"excerpt\":\"re := regexp.MustCompile(\\d+)\n次の行\"}"},
		{name: "array root", in: "```json\n[{\"a\":1}]\n```"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := SanitizeJSON([]byte(tt.in))
			if !json.Valid(got) {
				t.Fatalf("still cannot be parsed after repair: %s", got)
			}
		})
	}
}

// Input that does not become valid after repair is returned unchanged.
// Never making it worse is the point: the caller's error keeps pointing at the original breakage.
func TestSanitizeJSONGivesUpCleanly(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		`まったく JSON ではありません`,
		`{"title":`,
		``,
	} {
		if got := string(SanitizeJSON([]byte(in))); got != in {
			t.Errorf("SanitizeJSON(%q) = %q, return the input unchanged when it cannot be fixed", in, got)
		}
	}
}

// Repairing escapes does not change the contents.
// The backslash stays as a literal, and valid escapes stay interpreted.
func TestEscapeRepairPreservesContent(t *testing.T) {
	t.Parallel()

	var v struct {
		Excerpt string `json:"excerpt"`
	}
	in := `{"excerpt":"re := regexp.MustCompile(\d+)\nnext"}`
	if err := json.Unmarshal(SanitizeJSON([]byte(in)), &v); err != nil {
		t.Fatalf("cannot parse after repair: %v", err)
	}
	if !strings.Contains(v.Excerpt, `\d+`) {
		t.Errorf("the backslash did not stay as a literal: %q", v.Excerpt)
	}
	if !strings.Contains(v.Excerpt, "\n") {
		t.Errorf("a valid \\n turned into a literal: %q", v.Excerpt)
	}
}

// Repairing raw control characters does not lose the contents of the quote.
//
// The quote is the only clue to where a finding is (the line number may be omitted), so a fix that
// changes the contents defeats the purpose.
func TestControlCharRepairPreservesContent(t *testing.T) {
	t.Parallel()

	var v struct {
		Excerpt string `json:"excerpt"`
	}
	in := "{\"excerpt\":\"一行目\n\tインデントされた二行目\"}"
	if err := json.Unmarshal(SanitizeJSON([]byte(in)), &v); err != nil {
		t.Fatalf("cannot parse after repair: %v", err)
	}
	if v.Excerpt != "一行目\n\tインデントされた二行目" {
		t.Errorf("the contents of the quote changed: %q", v.Excerpt)
	}
}

// Newlines and indentation outside strings are left alone.
// Rewriting formatted JSON too would make the repair break more than it fixes.
func TestControlCharRepairKeepsFormatting(t *testing.T) {
	t.Parallel()

	// Broken as a value (an extra closing bracket at the end), but the formatting newlines are legitimate input.
	in := "{\n  \"title\": \"a\",\n  \"summary\": \"s\"\n}}"
	got := string(SanitizeJSON([]byte(in)))
	if strings.Contains(got, `\n  "title"`) {
		t.Errorf("the formatting newlines were escaped too: %q", got)
	}
	if !json.Valid([]byte(got)) {
		t.Errorf("still cannot be parsed after repair: %q", got)
	}
}

// ParseReport succeeds by way of the repair.
func TestParseReportRecoversFromModelNoise(t *testing.T) {
	t.Parallel()

	raw := "```json\n" + `{"title":"レビュー","summary":"要約","verdict":{"decision":"Minor","reason":"r"},` +
		`"findings":[{"severity":"Minor","file":"x.go","excerpt":"regexp.MustCompile(\d+)","message":"m"}]}` +
		"\n```"

	report, info, err := ParseReport([]byte(raw))
	if err != nil {
		t.Fatalf("ParseReport() = %v, want nil", err)
	}
	if !info.Repaired {
		t.Error("went through repair but ParseInfo.Repaired is false")
	}
	if info.Truncated {
		t.Error("not cut off but ParseInfo.Truncated is true")
	}
	if report.Title != "レビュー" {
		t.Errorf("Title = %q", report.Title)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(report.Findings))
	}
	if !strings.Contains(report.Findings[0].Excerpt, `\d+`) {
		t.Errorf("excerpt = %q", report.Findings[0].Excerpt)
	}
}

// For input that cannot be fixed, the error points at the original breakage.
func TestParseReportKeepsOriginalError(t *testing.T) {
	t.Parallel()

	_, _, err := ParseReport([]byte(`{"title":`))
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "cannot parse as JSON") {
		t.Errorf("err = %v", err)
	}
}

// From a response cut off by the output limit, only the completed findings are recovered.
//
// ★ In one measured case the output was cut off after writing 212 KB for a 10.7 KiB diff, losing a
// completed Blocker finding along with it. Instead of losing everything, return what there is and
// say it was cut off.
func TestParseReportSalvagesTruncatedOutput(t *testing.T) {
	t.Parallel()

	// Cut off in the middle of the second finding (it ends inside a string).
	raw := `{"title":"レビュー","summary":"要約","verdict":{"decision":"Blocker","reason":"r"},` +
		`"findings":[{"severity":"Blocker","file":"a.go","excerpt":"x","message":"m1"},` +
		`{"severity":"Minor","file":"b.go","excerpt":"y","mess`

	report, info, err := ParseReport([]byte(raw))
	if err != nil {
		t.Fatalf("ParseReport() = %v, want nil", err)
	}
	if !info.Truncated {
		t.Error("cut off but ParseInfo.Truncated is false")
	}
	if report.Verdict.Decision != DecisionBlocker {
		t.Errorf("Decision = %q, want %q", report.Verdict.Decision, DecisionBlocker)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1 (only the one that was complete)", len(report.Findings))
	}
	if report.Findings[0].Message != "m1" {
		t.Errorf("findings[0].Message = %q, want m1", report.Findings[0].Message)
	}
}

// Salvage does not kick in for complete output. Going back to the recoverable part drops data, so
// if it ran when not needed, the tail of a normal result would disappear.
func TestParseReportDoesNotSalvageCompleteOutput(t *testing.T) {
	t.Parallel()

	_, info, err := ParseReport([]byte(validReportJSON))
	if err != nil {
		t.Fatalf("ParseReport() = %v, want nil", err)
	}
	if info.Repaired || info.Truncated {
		t.Errorf("ParseInfo = %+v for input that read as-is", info)
	}
}

// Output broken from the start, with nothing recoverable, is an error rather than salvaged.
func TestParseReportRejectsUnsalvageableOutput(t *testing.T) {
	t.Parallel()

	// Cut off before reaching verdict.
	_, _, err := ParseReport([]byte(`{"title":"レビュー","summary":"よ`))
	if err == nil {
		t.Fatal("want an error")
	}
}
