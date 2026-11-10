package review

import (
	"fmt"
	"time"
)

// Status is the final state of the review pipeline.
type Status string

// The values appear in the receiving side's display branches and in the records stored from
// them. If you change them, deal with those records as well. SKIPPED is not folded into SUCCESS
// because callers could then no longer tell "there was no diff and nothing was published" from a
// run that has an artifact (see also Result.Published).
const (
	StatusSucceeded Status = "SUCCESS"
	StatusSkipped   Status = "SKIPPED"
	StatusFailed    Status = "FAILURE"
)

// Result is the final result of the review pipeline.
type Result struct {
	Status     Status
	StorageURI string
	PublicURL  string
	Duration   time.Duration
	Message    string

	// DiffBytes is the size of the diff sent to the AI. It is 0 when the diff could not be taken.
	//
	// Together with the duration, it is material for tuning the limits (diff size, run time) from
	// measurements. It is filled even for failed runs: the material for judging whether a limit is
	// too strict lies with the runs that were rejected, not with the ones that passed.
	DiffBytes int
	// Run is the run information reported by the reviewer. It is the zero value when the run did
	// not reach the review.
	Run RunInfo
}

// Published reports whether the artifact was published to storage.
func (r Result) Published() bool { return r.Status == StatusSucceeded }

// Succeeded builds the Result for a review result that was published.
func Succeeded(req Request, duration time.Duration) Result {
	return newResult(req, StatusSucceeded, duration, "AI review completed successfully.")
}

// Skipped builds the Result for a run that did not review because there was no diff.
func Skipped(req Request, duration time.Duration) Result {
	return newResult(req, StatusSkipped, duration, "Review skipped because there is no diff.")
}

// Failed builds the Result for a failed pipeline.
func Failed(req Request, duration time.Duration, err error) Result {
	message := fmt.Sprintf("Review pipeline failed: %v", err)
	return newResult(req, StatusFailed, duration, message)
}

func newResult(req Request, status Status, duration time.Duration, message string) Result {
	return Result{
		Status:     status,
		StorageURI: req.StorageURI,
		PublicURL:  req.PublicURL,
		Duration:   duration,
		Message:    message,
	}
}
