package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Severity is the severity of an individual finding. It is a label shared by every review mode.
type Severity string

// The order is itself the definition of weight (Severities / SortFindings rely on it).
const (
	SeverityBlocker Severity = "Blocker"
	SeverityMajor   Severity = "Major"
	SeverityMinor   Severity = "Minor"
)

// Decision is the verdict on the whole review: Severity plus DecisionNone, meaning "no issues".
type Decision string

// The values appear in the receiving side's display branches and in the records stored from them.
const (
	DecisionBlocker Decision = Decision(SeverityBlocker)
	DecisionMajor   Decision = Decision(SeverityMajor)
	DecisionMinor   Decision = Decision(SeverityMinor)
	DecisionNone    Decision = "None"
)

// These four are the only source of the enum values.
//
// Both the enum in the AI output schema the consumer builds and the validation of a received
// report refer to them. Keeping a separate copy lets the two drift apart when a value is added:
// the model returns a value the schema allows and decoding rejects it (the symptom is every review
// failing). AI SDK schemas want enum values as []string, so the string forms are provided here
// too.

// Severities returns the possible severities, heaviest first.
func Severities() []Severity {
	return []Severity{SeverityBlocker, SeverityMajor, SeverityMinor}
}

// Decisions returns the possible decisions, heaviest first.
func Decisions() []Decision {
	return []Decision{DecisionBlocker, DecisionMajor, DecisionMinor, DecisionNone}
}

// SeverityStrings returns the possible severities as strings, heaviest first.
func SeverityStrings() []string {
	return toStrings(Severities())
}

// DecisionStrings returns the possible decisions as strings, heaviest first.
func DecisionStrings() []string {
	return toStrings(Decisions())
}

// toStrings maps a slice of a string-based enum onto []string.
func toStrings[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, string(v))
	}
	return out
}

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool { return slices.Contains(Severities(), s) }

// Valid reports whether d is a known decision.
func (d Decision) Valid() bool { return slices.Contains(Decisions(), d) }

// Verdict is the decision on the whole review and its reason.
type Verdict struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
}

// Finding is an individual finding.
type Finding struct {
	Severity   Severity `json:"severity"`
	File       string   `json:"file"`
	Line       int      `json:"line,omitempty"`
	Excerpt    string   `json:"excerpt"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion,omitempty"`
	// Evidence lists the places the reviewer consulted as grounds for the finding (optional).
	// It is meant for a WorkspaceReviewer implementation that inspects the working directory to
	// record which files outside the diff it checked before deciding. It is not validated (it is
	// the reviewer's own claim, and the artifact stands without it).
	Evidence []string `json:"evidence,omitempty"`
}

// Report is the review result returned by the AI.
//
// It is not passed across layers as a JSON string because the publishing side would then reparse
// it into map[string]any, losing both the types and the validation. The AI adapter decodes it once
// and everything after that works with this struct.
type Report struct {
	Title    string    `json:"title"`
	Summary  string    `json:"summary"`
	Verdict  Verdict   `json:"verdict"`
	Findings []Finding `json:"findings"`
}

// ParseInfo reports, when the model output could not be read as-is, what it took to read it.
//
// Unless you look at it, what happened is recorded nowhere. Ignoring Truncated in particular means
// publishing a review that was cut off as if it were complete.
type ParseInfo struct {
	// Repaired reports that the output went through SanitizeJSON's repair. Nothing was lost.
	Repaired bool
	// Truncated reports that the output was cut off, so only the completed part was recovered.
	//
	// ★ This report is incomplete. How many findings lay beyond the cut is unknown. Treated as a
	// complete review, readers take it as "these are all the findings". Say so in the artifact or
	// the notification.
	Truncated bool
}

// ParseReport decodes the JSON returned by the AI into a Report and validates it.
//
// There are three stages, and each later one loses more.
//
//  1. Interpret as-is
//  2. Repair with SanitizeJSON and interpret (nothing is dropped; ParseInfo.Repaired)
//  3. Assume the output was cut off and recover only the completed part (content is dropped;
//     ParseInfo.Truncated)
//
// 2 and 3 are needed because, even with structured output specified, the model mixes in fences,
// trailing remarks and backslashes it forgot to escape, or hits its output limit and stops
// mid-sentence. All of these happen after the response has been fully returned, so retrying the
// API call does not fix them.
//
// Which stage was taken is visible only through the returned ParseInfo.
func ParseReport(data []byte) (Report, ParseInfo, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return Report{}, ParseInfo{}, ErrEmptyResponse
	}

	report, info, err := decodeReport(data)
	if err != nil {
		return Report{}, ParseInfo{}, err
	}
	if err := report.Validate(); err != nil {
		return Report{}, info, err
	}
	return report, info, nil
}

// decodeReport maps the JSON onto a Report, going through repair and salvage when needed.
func decodeReport(data []byte) (Report, ParseInfo, error) {
	var report Report
	err := json.Unmarshal(data, &report)
	if err == nil {
		return report, ParseInfo{}, nil
	}

	// The error returned is always the one for the original input. What the caller wants to see
	// is how the model's actual output was broken, not the intermediate state of repair or
	// salvage.
	invalid := fmt.Errorf("%w: cannot parse as JSON: %w", ErrInvalidReport, err)

	var info ParseInfo
	if cleaned := SanitizeJSON(data); !bytes.Equal(cleaned, data) {
		info.Repaired = true
		data = cleaned

		var repaired Report
		if err := json.Unmarshal(data, &repaired); err == nil {
			return repaired, info, nil
		}
	}

	// If it still cannot be read after repair, consider that it may have been cut off. Salvage
	// works on the repaired content (with broken escapes still in it, the recovered part would not
	// be readable either).
	salvaged := salvageJSON(data)
	if salvaged == nil {
		return Report{}, ParseInfo{}, invalid
	}

	var partial Report
	if err := json.Unmarshal(salvaged, &partial); err != nil {
		return Report{}, ParseInfo{}, invalid
	}
	info.Truncated = true
	return partial, info, nil
}

// Validate checks that the report meets the minimum shape.
//
// It checks only the enum values and the fields without which the artifact does not stand. The AI
// output is already constrained by the schema, and binding it more tightly here would reject
// legitimate model output too.
func (r Report) Validate() error {
	if strings.TrimSpace(r.Title) == "" {
		return fmt.Errorf("%w: title is empty", ErrInvalidReport)
	}
	if !r.Verdict.Decision.Valid() {
		return fmt.Errorf("%w: unknown verdict.decision %q", ErrInvalidReport, r.Verdict.Decision)
	}
	for i, finding := range r.Findings {
		if !finding.Severity.Valid() {
			return fmt.Errorf("%w: findings[%d] has unknown severity %q", ErrInvalidReport, i, finding.Severity)
		}
	}
	return nil
}

// SortFindings sorts the findings by severity, heaviest first.
//
// The prompt can tell the model to order them heaviest first, but nothing guarantees it is
// followed. The order decides what a reader sees first, so the receiving side settles it.
//
// Within the same severity the original order is kept. The model usually orders by file and line,
// and there is no reason to break the readability within that.
//
// The definition of weight is the order of Severities(). Do not keep a ranking table on the
// caller's side: when a severity is added, only one side gets updated and they drift apart.
func (r *Report) SortFindings() {
	order := Severities()
	rank := make(map[Severity]int, len(order))
	for i, severity := range order {
		rank[severity] = i
	}

	// Unknown severities go to the end. They do not appear once Validate has passed, but this
	// keeps the order intact even when called before validation.
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

// Count returns the number of findings of the given severity. It can be used in notification
// text, for example.
func (r Report) Count(severity Severity) int {
	n := 0
	for _, finding := range r.Findings {
		if finding.Severity == severity {
			n++
		}
	}
	return n
}
