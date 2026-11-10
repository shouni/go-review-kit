package pipeline

import (
	"log/slog"
	"time"
)

// Option is an optional setting of a Pipeline.
type Option func(*Pipeline)

// WithLogger replaces the logger the pipeline uses.
//
// The default is slog.Default(). Calling a package-level logger directly would take both the
// output destination and the attributes out of the caller's control.
func WithLogger(logger *slog.Logger) Option {
	return func(p *Pipeline) {
		if logger != nil {
			p.logger = logger
		}
	}
}

// WithPublishTimeout changes the limit given to publishing, notification and cleanup.
// Values of 0 or less are ignored.
func WithPublishTimeout(d time.Duration) Option {
	return func(p *Pipeline) {
		if d > 0 {
			p.publishTimeout = d
		}
	}
}

// WithRunTimeout sets the limit given to everything from taking the diff to the AI review.
// The default is unlimited, and values of 0 or less are ignored.
//
// It does not apply to publishing, notification or cleanup. Those run on a context stripped of
// the caller's deadline and cancel, under WithPublishTimeout's separate limit. A review is heavy,
// and right after it is cut off the caller's context has already expired, so using it as-is would
// drag down with it the notification reporting the failure and the cleanup of the working
// directory. Thanks to this detachment, the failure is recorded and notified even right after the
// review was cut off by a timeout.
//
// So set the limit on the review itself here, not on the context passed to Run. A deadline of
// your own applies outside the detachment above, and the notification goes down together with the
// cutoff.
//
// The point of having a limit at all is to give up on your own before the outside (a job queue
// waiting for a response, say) cuts you off, and to finish after recording and notifying the
// failure. If the outside gets there first, the whole process goes down and nothing is left.
func WithRunTimeout(d time.Duration) Option {
	return func(p *Pipeline) {
		if d > 0 {
			p.runTimeout = d
		}
	}
}

// WithMaxDiffBytes sets the limit, in bytes, on the diff sent to the AI.
// The default is 0 (unlimited), and values of 0 or less are ignored.
//
// When it is exceeded, the review does not run and fails with an error wrapping
// review.ErrDiffTooLarge (the step is review.StepDiff). It is treated as a failure, so it reaches
// the Notifier along the same path as any other failure.
//
// It measures bytes, not tokens. The byte count is at hand as soon as the diff arrives, while
// counting tokens takes a round trip to the model provider. That would mean paying the very cost
// you want to avoid in order to decide whether to avoid it, so cheapness wins over precision. As a
// rough guide, 1 MiB of Japanese text in UTF-8 is about 300,000 to 350,000 characters.
//
// The intent of this limit is: fail, do not truncate. Truncating to the limit and sending would
// have the model return a report *as a success* having seen only part of the diff, leaving a
// lower-quality review behind as the artifact. That is harder to notice than a failure.
func WithMaxDiffBytes(n int) Option {
	return func(p *Pipeline) {
		if n > 0 {
			p.maxDiffBytes = n
		}
	}
}
