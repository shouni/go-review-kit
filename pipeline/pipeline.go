// Package pipeline assembles the whole flow from receiving a review request to publishing and
// notifying the artifact.
//
// The steps form a single layer. Splitting overall control from the individual steps would leave
// the upper layer only delegating downwards, adding places to follow for nothing in return. Run is
// the only entry point, and it returns a Result, a Report and an error.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/shouni/go-review-kit/review"
)

// DefaultPublishTimeout is the default limit given to publishing and notification.
const DefaultPublishTimeout = 2 * time.Minute

// Deps holds the external implementations the pipeline depends on.
//
// They arrive as a struct rather than positional arguments because, with several arguments of the
// same type (interface) in a row, a mixed-up order would still compile.
type Deps struct {
	Sources review.DiffSourceFactory
	// Prompts assembles the prompt sent to the AI from the mode and the diff.
	Prompts review.PromptGenerator
	// WorkspaceReviewer is the reviewer that inspects the working directory and reviews.
	WorkspaceReviewer review.WorkspaceReviewer
	Publisher         review.Publisher
	Notifier          review.Notifier
}

func (d Deps) validate() error {
	var missing []string
	for _, dep := range []struct {
		name  string
		value any
	}{
		{"Sources", d.Sources},
		{"Prompts", d.Prompts},
		{"WorkspaceReviewer", d.WorkspaceReviewer},
		{"Publisher", d.Publisher},
		{"Notifier", d.Notifier},
	} {
		if isNil(dep.value) {
			missing = append(missing, dep.name)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("pipeline: missing dependencies: %s", strings.Join(missing, ", "))
	}
	return nil
}

// isNil reports whether an interface value is nil or holds a nil pointer.
//
// A plain `== nil` misses the latter. An implementation whose construction failed ((*T)(nil))
// would slip through validation and become a nil pointer dereference on its first call. Dependency
// validation exists to "fail at startup", so it is caught there.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

// Pipeline runs a review and takes it through to publishing and notification.
type Pipeline struct {
	deps           Deps
	logger         *slog.Logger
	publishTimeout time.Duration
	// runTimeout is the limit from taking the diff to the AI review. 0 means unlimited.
	runTimeout time.Duration
	// maxDiffBytes is the limit (in bytes) on the diff sent to the AI. 0 means unlimited.
	maxDiffBytes int
}

// New creates a Pipeline. It returns an error when any dependency is nil.
func New(deps Deps, opts ...Option) (*Pipeline, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}

	p := &Pipeline{
		deps:           deps,
		logger:         slog.Default(),
		publishTimeout: DefaultPublishTimeout,
	}
	for _, opt := range opts {
		// Callers that build options conditionally tend to mix in nil.
		// It is not a mistake worth panicking over, so skip it.
		if opt == nil {
			continue
		}
		opt(p)
	}
	return p, nil
}

// Run executes every step of a review request.
//
// The returned Report is non-nil only when the AI returned a review. It is nil when there was no
// diff or when the run failed before reaching the review. When storing fails, the Report stays
// non-nil and an error is returned too, so the caller can tell "reviewed, but could not keep it".
// Check for nil before using the return value.
//
// The Report is returned so that work needing the review's contents (title, verdict, findings)
// does not have to ride on the notification hook. A caller recording job state can build it from
// the return value without writing an adapter that poses as a notifier.
//
// When there was no diff, it returns a StatusSkipped Result and nil (it is not a failure).
// "It ran, but there is no artifact" can be told with Result.Published.
//
// Storing, notification and cleanup run detached from the caller's deadline, so the failure is
// recorded and notified even right after the review was cut off by a timeout. For which range each
// deadline covers, see WithRunTimeout / WithPublishTimeout.
func (p *Pipeline) Run(ctx context.Context, req review.Request) (review.Result, *review.Report, error) {
	start := time.Now()

	if err := req.Validate(); err != nil {
		return p.fail(ctx, req, outcome{}, nil, review.WrapStep(review.StepValidate, err), time.Since(start))
	}

	// The deadline covers only the review itself (see WithRunTimeout for why that range).
	runCtx := ctx
	if p.runTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, p.runTimeout)
		defer cancel()
	}

	out, err := p.produce(runCtx, req)
	elapsed := time.Since(start)
	report := out.report

	switch {
	case errors.Is(err, review.ErrEmptyDiff):
		result := out.into(review.Skipped(req, elapsed))
		p.logger.InfoContext(ctx, "skipped the review because there is no diff",
			"job_id", req.JobID, "repo_url", req.RepoURL, "base", req.Base, "head", req.Head)
		p.notify(ctx, review.Notification{Request: req, Result: result})
		return result, nil, nil

	case err != nil:
		return p.fail(ctx, req, out, nil, err, elapsed)
	}

	publishCtx, cancel := p.detach(ctx)
	defer cancel()

	if err := p.deps.Publisher.Publish(publishCtx, req, report); err != nil {
		// The review itself succeeded, so the Report is returned.
		// Pass ctx, not publishCtx. When publishing fails having used up its budget, publishCtx
		// has already expired, so handing it on to the notification would never deliver.
		return p.fail(ctx, req, out, &report, review.WrapStep(review.StepPublish, err), elapsed)
	}

	result := out.into(review.Succeeded(req, elapsed))
	p.logger.InfoContext(ctx, "review pipeline completed",
		"job_id", req.JobID,
		"repo_url", req.RepoURL,
		"status", result.Status,
		"storage_uri", result.StorageURI,
		"findings", len(report.Findings),
		"duration", elapsed,
		"truncated", result.Run.Truncated,
	)
	// The notification also gets a budget separate from publishing. Reusing the same context
	// would cut the notification's time by however long publishing took.
	p.notify(ctx, review.Notification{Request: req, Result: result, Report: &report})

	return result, &report, nil
}

// outcome is the result of one run that produce brings back.
//
// Measurements are returned along with the report so that the numbers survive even for failed
// runs. The material for judging whether a limit is too strict lies with the runs that were
// rejected, not with the ones that passed.
type outcome struct {
	report    review.Report
	diffBytes int
	info      review.RunInfo
}

// into copies the measurements onto a Result.
func (o outcome) into(result review.Result) review.Result {
	result.DiffBytes = o.diffBytes
	result.Run = o.info
	return result
}

// produce runs everything from taking the diff to the AI review.
//
// Even when it returns an error, the measurements known up to that point are returned in the
// outcome.
func (p *Pipeline) produce(ctx context.Context, req review.Request) (outcome, error) {
	var out outcome

	source, err := p.deps.Sources.Open(ctx, req)
	if err != nil {
		return out, review.WrapStep(review.StepPrepare, err)
	}
	defer p.close(ctx, source)

	diff, err := source.Diff(ctx, req.Base, req.Head)
	if err != nil {
		return out, review.WrapStep(review.StepDiff, err)
	}
	if strings.TrimSpace(diff) == "" {
		return out, review.ErrEmptyDiff
	}
	out.diffBytes = len(diff)

	// ★ An oversized diff fails even if sent, by truncated output or an exceeded deadline. Both
	// are only known after the model call has finished. Stop before sending and return it in a
	// form the user can act on (WithMaxDiffBytes explains why it is not truncated).
	if p.maxDiffBytes > 0 && len(diff) > p.maxDiffBytes {
		return out, review.WrapStep(review.StepDiff, fmt.Errorf(
			"%w: %d bytes (limit %d bytes): narrow the range between base and head and run again",
			review.ErrDiffTooLarge, len(diff), p.maxDiffBytes))
	}

	prompt, err := p.deps.Prompts.Generate(req.Mode, diff)
	if err != nil {
		return out, review.WrapStep(review.StepPrompt, err)
	}

	p.logger.InfoContext(ctx, "running AI review", "mode", req.Mode, "model", req.Model, "diff_bytes", len(diff))

	out.report, out.info, err = p.review(ctx, source, req, prompt)
	return out, err
}

// review checks Head out and then runs the AI review.
func (p *Pipeline) review(
	ctx context.Context, source review.DiffSource, req review.Request, prompt string,
) (review.Report, review.RunInfo, error) {
	// Check Head out explicitly (see the review.Workspace documentation for why).
	// Skipping this would have the reviewer read the contents of another ref left by a previous run.
	dir, err := source.CheckoutHead(ctx, req.Head)
	if err != nil {
		return review.Report{}, review.RunInfo{}, review.WrapStep(review.StepCheckout, err)
	}

	report, info, err := p.deps.WorkspaceReviewer.Review(ctx, req.Model, prompt, review.Workspace{
		Dir:  dir,
		Base: req.Base,
		Head: req.Head,
	})
	if err != nil {
		// Bring the measurements back even on failure. The tokens have already been spent.
		return review.Report{}, info, review.WrapStep(review.StepReview, err)
	}
	return report, info, nil
}

// close releases the DiffSource.
//
// It is handed a context stripped of the caller's deadline because, if the cleanup went down along
// with a review that was just cut off by a timeout, the local working directory would be left
// dirty.
func (p *Pipeline) close(ctx context.Context, source review.DiffSource) {
	closeCtx, cancel := p.detach(ctx)
	defer cancel()

	if err := source.Close(closeCtx); err != nil {
		p.logger.WarnContext(ctx, "failed to release the diff source", "error", err)
	}
}

// detach returns a context stripped of the caller's deadline and cancel, given publishTimeout
// instead.
//
// ★ Publishing, notification and cleanup go through here. Using the caller's context as-is, it has
// already expired right after a review is cut off by its deadline, so the notification reporting
// the failure and the cleanup of the working directory would fail along with it. That is an
// inverted behaviour: the report fails to arrive only when it is needed most.
//
// A fresh one is taken per call. Reusing one would cut the notification's time by however long
// publishing took, and when publishing held on to its limit and failed, the notification would
// never be delivered.
func (p *Pipeline) detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), p.publishTimeout)
}

// fail notifies the failure and returns a failed Result.
//
// ★ It takes the caller's context. notify does the detaching itself, so do not pass one that is
// already detached (see the comment on detach for why).
func (p *Pipeline) fail(
	ctx context.Context,
	req review.Request,
	out outcome,
	report *review.Report,
	err error,
	elapsed time.Duration,
) (review.Result, *review.Report, error) {
	result := out.into(review.Failed(req, elapsed, err))

	p.logger.ErrorContext(ctx, "review pipeline failed",
		"job_id", req.JobID, "repo_url", req.RepoURL, "step", review.StepOf(err), "error", err)
	p.notify(ctx, review.Notification{Request: req, Result: result, Report: report, Err: err})

	return result, report, err
}

// notify notifies on a context detached from the deadline.
func (p *Pipeline) notify(ctx context.Context, n review.Notification) {
	notifyCtx, cancel := p.detach(ctx)
	defer cancel()

	p.notifyOn(notifyCtx, n)
}

// notifyOn notifies on a context that is already detached.
//
// A notification failure is swallowed (the artifact is already stored, and turning the result into
// a failure because a notification did not arrive would mislead the decision to rerun).
func (p *Pipeline) notifyOn(ctx context.Context, n review.Notification) {
	if err := p.deps.Notifier.Notify(ctx, n); err != nil {
		p.logger.WarnContext(ctx, "notification failed", "status", n.Result.Status, "error", err)
	}
}
