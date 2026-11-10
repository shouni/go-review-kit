package review

import (
	"encoding/json"
	"testing"
)

// FuzzSanitizeJSON checks that the repair never makes the input worse.
//
// There are two invariants. If the input was changed by even one byte, the result must be valid
// JSON (if it cannot be fixed, leave it alone). And already-valid input does not change by a single
// byte. The repair is hand-written code that scans backslashes and control characters and counts
// bracket matching, and a table test only tries the breakages someone thought of. If it made
// things worse, the caller's error would point at an intermediate state of the repair instead of
// at what the model actually returned.
func FuzzSanitizeJSON(f *testing.F) {
	for _, s := range fuzzJSONSeeds() {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		got := SanitizeJSON(data)

		if json.Valid(data) {
			if string(got) != string(data) {
				t.Fatalf("valid input was altered:\n in: %q\nout: %q", data, got)
			}
			return
		}
		if string(got) == string(data) {
			return // It could not be fixed, so it was returned unchanged. That is allowed.
		}
		if !json.Valid(got) {
			t.Fatalf("input was altered but is still invalid:\n in: %q\nout: %q", data, got)
		}
	})
}

// FuzzParseReport checks that a report ParseReport succeeded on always passes Validate, and that
// truncation is reported only when the output was actually truncated.
//
// Missing a Truncated publishes a review that was cut off as a complete one.
func FuzzParseReport(f *testing.F) {
	for _, s := range fuzzJSONSeeds() {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		report, info, err := ParseReport(data)
		if err != nil {
			return
		}

		// If it succeeded, the contents always pass validation.
		if err := report.Validate(); err != nil {
			t.Fatalf("ParseReport(%q) succeeded but Validate rejects it: %v", data, err)
		}
		// If the original input was interpretable as-is, neither repair nor truncation happened.
		if json.Valid(data) && (info.Repaired || info.Truncated) {
			t.Fatalf("ParseReport(%q) reported Repaired=%v Truncated=%v for already-valid input",
				data, info.Repaired, info.Truncated)
		}
	})
}

// fuzzJSONSeeds lists the ways model output has actually been seen to break.
func fuzzJSONSeeds() []string {
	const valid = `{"title":"t","summary":"s","verdict":{"decision":"approve","reason":"r"}}`
	return []string{
		valid,
		"```json\n" + valid + "\n```",
		"Here you go:\n" + valid + "\nHope it helps.",
		valid + "}}}",
		`{"title":"C:\path\to\file"}`,
		"{\"title\":\"line1\nline2\"}",
		"{\"title\":\"tab\there\"}",
		`{"title":"t","findings":[{"file":"a.go"`,
		`{"title":`,
		"[]",
		"{}",
		"null",
		"",
		"not json at all",
		`{"title":"t","summary":"s","verdict":{"decision":"nope"}}`,
	}
}
