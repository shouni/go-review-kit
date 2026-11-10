package review

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by the pipeline. Callers can branch on them with errors.Is.
//
// State is not carried in struct fields (in the shape of Error / IsSkipped) because every new
// state would add a field, and errors.Is would not work on them. Outcomes are returned as
// ordinary errors, and their kind is expressed by a sentinel.
var (
	// ErrInvalidRequest reports that a required field of the Request is missing.
	ErrInvalidRequest = errors.New("review: request is invalid")
	// ErrEmptyDiff reports that there is no diff between base and head, so the review was
	// skipped. It is not a failure, so the pipeline returns nil together with a StatusSkipped
	// Result.
	ErrEmptyDiff = errors.New("review: diff is empty")
	// ErrDiffTooLarge reports that the diff exceeded the limit (pipeline.WithMaxDiffBytes), so
	// the run stopped without sending it to the AI.
	//
	// Unlike an empty diff, this is a failure. The input would pass with a narrower range, so the
	// user has to act on it, and treating it as a skip would make it indistinguishable from
	// "reviewed, but nothing was found".
	//
	// This sentinel exists to stop before sending. Running a review over an oversized diff fails
	// anyway, by truncated output, an exceeded deadline or an input limit, but each of those is
	// only known after the model call has finished. That ordering fails after the most cost has
	// been paid.
	ErrDiffTooLarge = errors.New("review: diff is too large")
	// ErrRefNotFound reports that the given ref does not exist in the repository.
	//
	// It is not used when reading the ref failed. Folding repository corruption or disk failures
	// into it would tell the user "that branch does not exist".
	ErrRefNotFound = errors.New("review: ref not found")
	// ErrUnsupportedRepoURL reports that the repository URL is in a form this module cannot
	// handle.
	//
	// It is an input mistake, not an outage, so retrying does not fix it. The user needs to be
	// told to fix the form, while a network failure is fixed by retrying, so it is a sentinel to
	// let callers tell the two apart.
	ErrUnsupportedRepoURL = errors.New("review: unsupported repository URL")
	// ErrEmptyResponse reports that the AI returned an empty result without an error.
	ErrEmptyResponse = errors.New("review: model returned an empty response")
	// ErrInvalidReport reports that the AI output could not be interpreted as a report.
	ErrInvalidReport = errors.New("review: report is not valid")
)

// Pipeline step names. They go into StepError.Step and can be used in notification text.
//
// Callers should compare against these constants, not their text: the values were Japanese
// until 2026-11-10 and are English now, so StepError.Error() reads as one language
// ("AI review failed: ...").
const (
	StepValidate = "request validation"
	StepPrepare  = "repository preparation"
	StepDiff     = "diff retrieval"
	StepPrompt   = "prompt generation"
	StepCheckout = "head checkout"
	StepReview   = "AI review"
	StepPublish  = "result publishing"
)

// StepError is an error that records which step failed.
//
// The step name is held by the error itself rather than by a separate field. A step name only
// means something paired with its error, and keeping the two apart invites mixing them up.
type StepError struct {
	Step string
	Err  error
}

// Error returns the error message.
func (e *StepError) Error() string {
	return fmt.Sprintf("%s failed: %v", e.Step, e.Err)
}

// Unwrap returns the underlying error so that errors.Is / errors.As see through it.
func (e *StepError) Unwrap() error { return e.Err }

// WrapStep attaches a step name to err. It returns nil when err is nil.
func WrapStep(step string, err error) error {
	if err == nil {
		return nil
	}
	return &StepError{Step: step, Err: err}
}

// StepOf returns the step name carried by err, or an empty string when none is attached.
func StepOf(err error) string {
	if se, ok := errors.AsType[*StepError](err); ok {
		return se.Step
	}
	return ""
}
