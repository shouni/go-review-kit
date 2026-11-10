package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shouni/go-review-kit/review"
)

func TestNewRejectsMissingDeps(t *testing.T) {
	full := Deps{
		Sources:           &fakeFactory{},
		Prompts:           &fakePrompts{},
		WorkspaceReviewer: &fakeReviewer{},
		Publisher:         &fakePublisher{},
		Notifier:          &fakeNotifier{},
	}

	tests := []struct {
		name   string
		mutate func(*Deps)
	}{
		{"nil Sources", func(d *Deps) { d.Sources = nil }},
		{"nil Prompts", func(d *Deps) { d.Prompts = nil }},
		{"nil WorkspaceReviewer", func(d *Deps) { d.WorkspaceReviewer = nil }},
		{"nil Publisher", func(d *Deps) { d.Publisher = nil }},
		{"nil Notifier", func(d *Deps) { d.Notifier = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := full
			tt.mutate(&deps)

			if _, err := New(deps); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}

	if _, err := New(full); err != nil {
		t.Fatalf("failed with complete dependencies: %v", err)
	}
}

func TestRunSuccess(t *testing.T) {
	h := newHarness(t)

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != review.StatusSucceeded {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusSucceeded)
	}
	if !result.Published() {
		t.Error("Published() is false")
	}
	if !h.publisher.called {
		t.Error("Publish was not called")
	}
	if !h.source.wasClosed() {
		t.Error("the diff source was not released")
	}

	// The prompt is built from the diff and handed to the review as-is.
	if h.prompts.gotDiff != h.source.diff || h.prompts.gotMode != "detail" {
		t.Errorf("prompt generation input does not match: mode=%q diff=%q", h.prompts.gotMode, h.prompts.gotDiff)
	}
	if h.reviewer.gotPrompt != h.prompts.prompt || h.reviewer.gotModel != "gemini-2.5-pro" {
		t.Errorf("review input does not match: model=%q prompt=%q", h.reviewer.gotModel, h.reviewer.gotPrompt)
	}

	notification := h.notifier.last(t)
	if notification.Err != nil {
		t.Errorf("Err is set on success: %v", notification.Err)
	}
	if notification.Report == nil {
		t.Fatal("a Report should be passed on success")
	}
	if len(notification.Report.Findings) != 1 {
		t.Errorf("Report contents do not match: %+v", notification.Report)
	}
}

// No diff is not a failure. Nothing is published; only the notification goes out, and the run
// returns StatusSkipped and nil.
func TestRunSkipsWhenDiffIsEmpty(t *testing.T) {
	tests := []struct {
		name string
		diff string
	}{
		{"completely empty", ""},
		{"whitespace only", "  \n\t "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.source.diff = tt.diff

			result, _, err := h.pipeline.Run(context.Background(), testRequest())
			if err != nil {
				t.Fatalf("a skip should not be an error: %v", err)
			}

			if result.Status != review.StatusSkipped {
				t.Errorf("Status = %q, want %q", result.Status, review.StatusSkipped)
			}
			if result.Published() {
				t.Error("Published() is true on skip")
			}
			if h.reviewer.called {
				t.Error("the AI was called although there is no diff")
			}
			if h.publisher.called {
				t.Error("published although there is no diff")
			}
			if h.notifier.count() != 1 {
				t.Errorf("notification count = %d, want 1", h.notifier.count())
			}
			if !h.source.wasClosed() {
				t.Error("the diff source was not released")
			}
		})
	}
}

// A diff over the limit fails before it is sent to the AI. Once sent, the failure is only known
// after the model call has finished (truncated output, an exceeded deadline), failing after the
// most cost has been paid.
func TestRunFailsWhenDiffIsTooLarge(t *testing.T) {
	h := newHarness(t, WithMaxDiffBytes(16))
	h.source.diff = strings.Repeat("a", 17)

	result, report, err := h.pipeline.Run(context.Background(), testRequest())
	if !errors.Is(err, review.ErrDiffTooLarge) {
		t.Fatalf("err = %v, want review.ErrDiffTooLarge", err)
	}
	if step := review.StepOf(err); step != review.StepDiff {
		t.Errorf("StepOf(err) = %q, want %q", step, review.StepDiff)
	}
	if report != nil {
		t.Error("a Report was returned without a review")
	}
	if result.Status != review.StatusFailed {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusFailed)
	}

	// The limit exists to "stop before sending", so prompt generation is not reached either.
	if h.prompts.gotDiff != "" {
		t.Error("the prompt was built although the limit was exceeded")
	}
	if h.reviewer.called {
		t.Error("the AI was called although the limit was exceeded")
	}
	if h.publisher.called {
		t.Error("published although the limit was exceeded")
	}
	if !h.source.wasClosed() {
		t.Error("the diff source was not released")
	}

	// It is a failure, not a skip, so the notification carries the error. Without it the user
	// cannot act by narrowing the range.
	if n := h.notifier.count(); n != 1 {
		t.Fatalf("notification count = %d, want 1", n)
	}
	if last := h.notifier.last(t); !errors.Is(last.Err, review.ErrDiffTooLarge) {
		t.Errorf("notification Err = %v, want review.ErrDiffTooLarge", last.Err)
	}
}

// A diff exactly at the limit passes. Getting the boundary wrong would, for a consumer that set the
// limit from measurements, mean "only a diff of exactly that size fails for no reason".
func TestMaxDiffBytesBoundary(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{"exactly at the limit", 16, false},
		{"1 byte over", 17, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, WithMaxDiffBytes(16))
			h.source.diff = strings.Repeat("a", tt.size)

			_, _, err := h.pipeline.Run(context.Background(), testRequest())
			if got := errors.Is(err, review.ErrDiffTooLarge); got != tt.wantErr {
				t.Fatalf("ErrDiffTooLarge = %v, want %v (err: %v)", got, tt.wantErr, err)
			}
			if h.reviewer.called == tt.wantErr {
				t.Errorf("reviewer.called = %v", h.reviewer.called)
			}
		})
	}
}

// The default is unlimited. Whether to have a limit is the consumer's decision; if the library
// picked a number on its own, callers that set nothing would silently change behaviour.
func TestMaxDiffBytesDefaultsToUnlimited(t *testing.T) {
	h := newHarness(t)
	if h.pipeline.maxDiffBytes != 0 {
		t.Errorf("maxDiffBytes = %d, want 0 (unlimited)", h.pipeline.maxDiffBytes)
	}

	h.source.diff = strings.Repeat("a", 1<<20)
	if _, _, err := h.pipeline.Run(context.Background(), testRequest()); err != nil {
		t.Fatalf("must not fail on size when unset: %v", err)
	}
}

// Values of 0 or less are ignored (the same rule as WithRunTimeout / WithPublishTimeout).
func TestMaxDiffBytesIgnoresNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		h := newHarness(t, WithMaxDiffBytes(n))
		if h.pipeline.maxDiffBytes != 0 {
			t.Errorf("WithMaxDiffBytes(%d) gives maxDiffBytes = %d, want 0", n, h.pipeline.maxDiffBytes)
		}
	}
}

func TestRunFailures(t *testing.T) {
	tests := []struct {
		name        string
		arrange     func(*harness)
		wantStep    string
		wantPublish bool
	}{
		{
			name:     "prepare fails",
			arrange:  func(h *harness) { h.factory.openErr = errBoom },
			wantStep: review.StepPrepare,
		},
		{
			name:     "diff fails",
			arrange:  func(h *harness) { h.source.diffErr = errBoom },
			wantStep: review.StepDiff,
		},
		{
			name:     "prompt generation fails",
			arrange:  func(h *harness) { h.prompts.err = errBoom },
			wantStep: review.StepPrompt,
		},
		{
			name:     "head checkout fails",
			arrange:  func(h *harness) { h.source.checkoutErr = errBoom },
			wantStep: review.StepCheckout,
		},
		{
			name:     "AI review fails",
			arrange:  func(h *harness) { h.reviewer.err = errBoom },
			wantStep: review.StepReview,
		},
		{
			name:        "publish fails",
			arrange:     func(h *harness) { h.publisher.err = errBoom },
			wantStep:    review.StepPublish,
			wantPublish: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.arrange(h)

			result, _, err := h.pipeline.Run(context.Background(), testRequest())
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !errors.Is(err, errBoom) {
				t.Fatalf("cannot reach the cause: %v", err)
			}
			if got := review.StepOf(err); got != tt.wantStep {
				t.Errorf("step name = %q, want %q", got, tt.wantStep)
			}
			if result.Status != review.StatusFailed {
				t.Errorf("Status = %q, want %q", result.Status, review.StatusFailed)
			}
			if h.publisher.called != tt.wantPublish {
				t.Errorf("Publish called = %v, want %v", h.publisher.called, tt.wantPublish)
			}

			// A failure is exactly when a notification is needed.
			notification := h.notifier.last(t)
			if notification.Err == nil {
				t.Error("the cause was not passed to the notification")
			}
			if notification.Result.Status != review.StatusFailed {
				t.Errorf("notification Status = %q", notification.Result.Status)
			}
		})
	}
}

func TestRunRejectsInvalidRequest(t *testing.T) {
	h := newHarness(t)

	req := testRequest()
	req.RepoURL = ""

	result, _, err := h.pipeline.Run(context.Background(), req)
	if !errors.Is(err, review.ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest, got: %v", err)
	}
	if got := review.StepOf(err); got != review.StepValidate {
		t.Errorf("step name = %q, want %q", got, review.StepValidate)
	}
	if result.Status != review.StatusFailed {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusFailed)
	}
	// Once rejected by validation, the repository is not touched.
	if h.source.wasClosed() {
		t.Error("a diff source that was never prepared was released")
	}
	if h.notifier.count() != 1 {
		t.Errorf("notification count = %d, want 1", h.notifier.count())
	}
}

// Publishing, notification and cleanup are detached from the caller's deadline. Right after a
// review is cut off by its deadline is exactly when a notification is needed, and inheriting the
// expired context would take the notification down with it.
func TestRunDetachesPublishFromCallerDeadline(t *testing.T) {
	h := newHarness(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller has already been cut off

	result, _, err := h.pipeline.Run(ctx, testRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != review.StatusSucceeded {
		t.Fatalf("Status = %q", result.Status)
	}

	if h.publisher.ctxErr != nil {
		t.Errorf("publishing was given an expired context: %v", h.publisher.ctxErr)
	}
	if h.notifier.ctxErr != nil {
		t.Errorf("the notification was given an expired context: %v", h.notifier.ctxErr)
	}
}

// A notification failure does not fail the whole pipeline, because the artifact is already stored.
func TestRunIgnoresNotifierFailure(t *testing.T) {
	h := newHarness(t)
	h.notifier.err = errBoom

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("the whole run failed because of a notification failure: %v", err)
	}
	if result.Status != review.StatusSucceeded {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusSucceeded)
	}
}

// Likewise, a cleanup failure is no reason to discard the review result.
func TestRunIgnoresCloseFailure(t *testing.T) {
	h := newHarness(t)
	h.source.closeErr = errBoom

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("the whole run failed because of a cleanup failure: %v", err)
	}
	if result.Status != review.StatusSucceeded {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusSucceeded)
	}
}

func TestRunRecordsDuration(t *testing.T) {
	h := newHarness(t)

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Duration <= 0 {
		t.Fatalf("Duration = %v, want a positive value", result.Duration)
	}
}

func TestOptions(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		h := newHarness(t)
		if h.pipeline.publishTimeout != DefaultPublishTimeout {
			t.Errorf("publishTimeout = %v, want %v", h.pipeline.publishTimeout, DefaultPublishTimeout)
		}
		if h.pipeline.logger == nil {
			t.Error("logger is nil")
		}
	})

	t.Run("WithPublishTimeout", func(t *testing.T) {
		h := newHarness(t, WithPublishTimeout(30*time.Second))
		if h.pipeline.publishTimeout != 30*time.Second {
			t.Errorf("publishTimeout = %v", h.pipeline.publishTimeout)
		}
	})

	t.Run("invalid values are ignored", func(t *testing.T) {
		h := newHarness(t, WithPublishTimeout(0), WithLogger(nil))
		if h.pipeline.publishTimeout != DefaultPublishTimeout {
			t.Errorf("publishTimeout = %v, want %v", h.pipeline.publishTimeout, DefaultPublishTimeout)
		}
		if h.pipeline.logger == nil {
			t.Error("overwritten by a nil logger")
		}
	})
}

// Run also returns the Report, so that work needing the review's contents (recording job state,
// say) does not have to ride on the notification hook.
func TestRunReturnsReport(t *testing.T) {
	h := newHarness(t)

	_, report, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report == nil {
		t.Fatal("a Report should be returned on success")
	}
	if report.Title != testReport().Title {
		t.Errorf("Title = %q, want %q", report.Title, testReport().Title)
	}
	if len(report.Findings) != 1 {
		t.Errorf("findings count = %d, want 1", len(report.Findings))
	}
}

// The Report is nil when the run did not reach the review.
func TestRunReturnsNilReportWithoutReview(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*harness)
	}{
		{"no diff", func(h *harness) { h.source.diff = "" }},
		{"prepare fails", func(h *harness) { h.factory.openErr = errBoom }},
		{"AI review fails", func(h *harness) { h.reviewer.err = errBoom }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.arrange(h)

			_, report, _ := h.pipeline.Run(context.Background(), testRequest())
			if report != nil {
				t.Fatalf("the Report should be nil: %+v", report)
			}
		})
	}
}

// When storing fails, the review itself succeeded, so both the Report and an error are returned.
// The caller can tell "reviewed, but could not keep it".
func TestRunReturnsReportWhenPublishFails(t *testing.T) {
	h := newHarness(t)
	h.publisher.err = errBoom

	_, report, err := h.pipeline.Run(context.Background(), testRequest())
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if report == nil {
		t.Fatal("the review succeeded, so a Report should be returned")
	}
	if report.Title != testReport().Title {
		t.Errorf("Title = %q", report.Title)
	}
}

// The Report is nil when validation rejects the request too.
func TestRunReturnsNilReportForInvalidRequest(t *testing.T) {
	h := newHarness(t)

	req := testRequest()
	req.RepoURL = ""

	_, report, err := h.pipeline.Run(context.Background(), req)
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if report != nil {
		t.Fatalf("the Report should be nil: %+v", report)
	}
}

// slowReviewer is a review.WorkspaceReviewer that waits until its context is cancelled.
type slowReviewer struct {
	sawDeadline bool
	hadDeadline bool
}

func (s *slowReviewer) Review(
	ctx context.Context, _, _ string, _ review.Workspace,
) (review.Report, review.RunInfo, error) {
	_, s.hadDeadline = ctx.Deadline()
	<-ctx.Done()
	s.sawDeadline = true
	return review.Report{}, review.RunInfo{}, ctx.Err()
}

// WithRunTimeout puts a deadline on the review itself only.
//
// If the caller put its own deadline on the context passed to Run, the context would have expired
// right after the cutoff, taking the failure notification down with it.
// ★ With this limit, the notification arrives even after a cutoff.
func TestRunTimeoutBoundsReviewButNotNotify(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithRunTimeout(20*time.Millisecond))
	slow := &slowReviewer{}
	h.pipeline.deps.WorkspaceReviewer = slow

	result, report, err := h.pipeline.Run(context.Background(), testRequest())

	if err == nil {
		t.Fatal("not cut off by the deadline")
	}
	if !slow.hadDeadline {
		t.Error("the context passed to the reviewer has no deadline")
	}
	if report != nil {
		t.Errorf("a report was returned on cutoff: %+v", report)
	}
	if result.Status != review.StatusFailed {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusFailed)
	}
	// This is the point: the notification arrives without being caught up in the deadline.
	if got := h.notifier.count(); got != 1 {
		t.Fatalf("notification count = %d, want 1 (the failure notification should arrive even on cutoff)", got)
	}
}

// The default is unlimited. This checks that existing users' behaviour does not change.
func TestRunTimeoutDefaultsToUnlimited(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if h.pipeline.runTimeout != 0 {
		t.Errorf("runTimeout = %v, want 0 (unlimited)", h.pipeline.runTimeout)
	}

	probe := &deadlineProbe{report: testReport()}
	h.pipeline.deps.WorkspaceReviewer = probe
	if _, _, err := h.pipeline.Run(context.Background(), testRequest()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if probe.hadDeadline {
		t.Error("the reviewer's context has a deadline although none was set")
	}
}

// Values of 0 or less are ignored (the same rule as WithPublishTimeout).
func TestRunTimeoutIgnoresNonPositive(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -time.Second} {
		h := newHarness(t, WithRunTimeout(d))
		if h.pipeline.runTimeout != 0 {
			t.Errorf("WithRunTimeout(%v) gives runTimeout = %v, want 0", d, h.pipeline.runTimeout)
		}
	}
}

// deadlineProbe is a review.WorkspaceReviewer that only checks whether its context has a deadline.
type deadlineProbe struct {
	report      review.Report
	hadDeadline bool
}

func (d *deadlineProbe) Review(
	ctx context.Context, _, _ string, _ review.Workspace,
) (review.Report, review.RunInfo, error) {
	_, d.hadDeadline = ctx.Deadline()
	return d.report, review.RunInfo{}, nil
}

// slowPublisher is a review.Publisher that holds on until its context expires and then fails.
type slowPublisher struct{}

func (slowPublisher) Publish(ctx context.Context, _ review.Request, _ review.Report) error {
	<-ctx.Done()
	return ctx.Err()
}

// ★ Even when publishing uses up its budget and fails, the failure notification arrives.
//
// Handing the context given to publishing on to the notification, it has already expired, so the
// notification always fails. That is the inverted behaviour of "it fails to arrive only when a
// report is needed most", so the notification gets a separate budget.
func TestPublishTimeoutDoesNotStarveFailureNotify(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithPublishTimeout(20*time.Millisecond))
	h.pipeline.deps.Publisher = slowPublisher{}

	_, report, err := h.pipeline.Run(context.Background(), testRequest())
	if err == nil {
		t.Fatal("the publish failure was not returned")
	}
	// The review itself succeeded, so the Report is returned.
	if report == nil {
		t.Error("the Report should be returned even when publishing fails")
	}

	if got := h.notifier.count(); got != 1 {
		t.Fatalf("notification count = %d, want 1", got)
	}
	if h.notifier.ctxErr != nil {
		t.Errorf("the notification ran on an expired context: %v", h.notifier.ctxErr)
	}
}

// On success too, the notification does not share its budget with publishing.
func TestPublishAndNotifyGetSeparateBudgets(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithPublishTimeout(50*time.Millisecond))
	h.pipeline.deps.Publisher = sleepyPublisher{d: 30 * time.Millisecond}

	if _, _, err := h.pipeline.Run(context.Background(), testRequest()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if h.notifier.ctxErr != nil {
		t.Errorf("the notification's context was eaten into by publishing: %v", h.notifier.ctxErr)
	}

	// The deadline passed to the notification is not shortened by the time publishing used.
	if remaining := h.notifier.remaining; remaining < 40*time.Millisecond {
		t.Errorf("notification time left = %v, sharing its budget with publishing", remaining)
	}
}

// sleepyPublisher is a review.Publisher that succeeds after taking a fixed time.
type sleepyPublisher struct{ d time.Duration }

func (s sleepyPublisher) Publish(ctx context.Context, _ review.Request, _ review.Report) error {
	select {
	case <-time.After(s.d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// nilSources mimics an implementation whose construction failed (a typed nil).
type nilSources struct{}

func (*nilSources) Open(context.Context, review.Request) (review.DiffSource, error) {
	return nil, nil
}

// An implementation whose construction failed (a nil pointer) fails at startup.
//
// A plain == nil misses a typed nil, which passes validation and then becomes a nil pointer
// dereference on its first call. Dependency validation exists to fail at startup.
func TestValidateRejectsTypedNilDependency(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	deps := h.pipeline.deps
	deps.Sources = (*nilSources)(nil)

	_, err := New(deps)
	if err == nil {
		t.Fatal("a typed-nil dependency slipped through")
	}
	if !strings.Contains(err.Error(), "Sources") {
		t.Errorf("the error does not name the field: %v", err)
	}
}

// A typed-nil reviewer is rejected as unset.
//
// A plain == nil misses it, and letting it through means a nil pointer dereference on the first call.
func TestValidateTreatsTypedNilReviewerAsUnset(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	deps := h.pipeline.deps
	deps.WorkspaceReviewer = (*deadlineProbe)(nil)

	_, err := New(deps)
	if err == nil {
		t.Fatal("a typed-nil reviewer slipped through")
	}
	if !strings.Contains(err.Error(), "WorkspaceReviewer") {
		t.Errorf("the error does not name the field: %v", err)
	}
}

// Passing a nil Option does not panic.
func TestNewIgnoresNilOption(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if _, err := New(h.pipeline.deps, nil, WithPublishTimeout(time.Second), nil); err != nil {
		t.Fatalf("a nil Option caused an error: %v", err)
	}
}

// ★ Measurements survive even for failed runs.
//
// The material for judging whether a limit is too strict lies with the runs that were rejected,
// not with the ones that passed. If numbers survived only on success, the tail of the distribution
// would be missing exactly when you most want to see it.
func TestRunCarriesStatsIntoResult(t *testing.T) {
	t.Parallel()

	info := review.RunInfo{
		Truncated:     true,
		PromptTokens:  219062,
		OutputTokens:  63950,
		ThoughtTokens: 1579,
		ToolCalls:     5,
	}

	tests := []struct {
		name    string
		arrange func(*harness)
		want    review.Status
	}{
		{"success", func(*harness) {}, review.StatusSucceeded},
		{"AI review fails", func(h *harness) { h.reviewer.err = errBoom }, review.StatusFailed},
		{"publish fails", func(h *harness) { h.publisher.err = errBoom }, review.StatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.reviewer.info = info
			tt.arrange(h)

			result, _, _ := h.pipeline.Run(context.Background(), testRequest())

			if result.Status != tt.want {
				t.Fatalf("Status = %q, want %q", result.Status, tt.want)
			}
			if result.DiffBytes != len(h.source.diff) {
				t.Errorf("DiffBytes = %d, want %d", result.DiffBytes, len(h.source.diff))
			}
			if result.Run != info {
				t.Errorf("Run = %+v, want %+v", result.Run, info)
			}
		})
	}
}

// Even a run that did not reach the review keeps the diff size.
// A run rejected by the limit is exactly the one where you later want to see how far over it was.
func TestRunRecordsDiffBytesWhenRejected(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithMaxDiffBytes(16))
	h.source.diff = strings.Repeat("a", 17)

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
	if !errors.Is(err, review.ErrDiffTooLarge) {
		t.Fatalf("err = %v, want ErrDiffTooLarge", err)
	}
	if result.DiffBytes != 17 {
		t.Errorf("DiffBytes = %d, want 17", result.DiffBytes)
	}
	if result.Run != (review.RunInfo{}) {
		t.Errorf("Run = %+v without a review", result.Run)
	}
}

// A run with no diff leaves both at the zero value.
func TestRunLeavesStatsEmptyWhenSkipped(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.source.diff = ""

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.DiffBytes != 0 || result.Run != (review.RunInfo{}) {
		t.Errorf("DiffBytes = %d, Run = %+v on skip", result.DiffBytes, result.Run)
	}
}
