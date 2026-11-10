package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shouni/go-review-kit/review"
)

// testRequest returns the request used in tests.
func testRequest() review.Request {
	return review.Request{
		RepoURL:    "ssh://git@github.com/shouni/example.git",
		Base:       "main",
		Head:       "develop",
		Mode:       "detail",
		Model:      "gemini-2.5-pro",
		StorageURI: "gs://bucket/review.html",
		PublicURL:  "https://example.com/review.html",
	}
}

func testReport() review.Report {
	return review.Report{
		Title:   "レビュー結果",
		Summary: "概ね良好です。",
		Verdict: review.Verdict{Decision: review.DecisionMinor, Reason: "軽微な指摘が1件"},
		Findings: []review.Finding{
			{Severity: review.SeverityMinor, File: "main.go", Excerpt: "x := 1", Message: "未使用です。"},
		},
	}
}

// discardLogger is a logger that keeps test output clean.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSource is a test implementation of review.DiffSource.
type fakeSource struct {
	diff        string
	diffErr     error
	closeErr    error
	dir         string
	checkoutErr error

	gotHead string

	mu        sync.Mutex
	closed    bool
	closedCtx context.Context
}

func (f *fakeSource) CheckoutHead(_ context.Context, head string) (string, error) {
	f.gotHead = head
	if f.checkoutErr != nil {
		return "", f.checkoutErr
	}
	return f.dir, nil
}

func (f *fakeSource) Diff(_ context.Context, _, _ string) (string, error) {
	return f.diff, f.diffErr
}

func (f *fakeSource) Close(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed = true
	f.closedCtx = ctx
	return f.closeErr
}

func (f *fakeSource) wasClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// fakeFactory is a test implementation of review.DiffSourceFactory.
type fakeFactory struct {
	source  *fakeSource
	openErr error
}

func (f *fakeFactory) Open(context.Context, review.Request) (review.DiffSource, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.source, nil
}

// fakePrompts is a test implementation of review.PromptGenerator.
type fakePrompts struct {
	prompt string
	err    error

	gotMode string
	gotDiff string
}

func (f *fakePrompts) Generate(mode, diff string) (string, error) {
	f.gotMode, f.gotDiff = mode, diff
	if f.err != nil {
		return "", f.err
	}
	return f.prompt, nil
}

// fakeReviewer is a test implementation of review.WorkspaceReviewer.
type fakeReviewer struct {
	report review.Report
	info   review.RunInfo
	err    error

	called    bool
	gotModel  string
	gotPrompt string
	gotWS     review.Workspace
}

func (f *fakeReviewer) Review(
	_ context.Context, model, prompt string, ws review.Workspace,
) (review.Report, review.RunInfo, error) {
	f.called = true
	f.gotModel, f.gotPrompt, f.gotWS = model, prompt, ws
	if f.err != nil {
		// The tokens are spent even on failure, so the measurements are returned.
		return review.Report{}, f.info, f.err
	}
	return f.report, f.info, nil
}

// fakePublisher is a test implementation of review.Publisher.
type fakePublisher struct {
	err error

	called    bool
	gotReport review.Report
	ctxErr    error
}

func (f *fakePublisher) Publish(ctx context.Context, _ review.Request, report review.Report) error {
	f.called = true
	f.gotReport = report
	f.ctxErr = ctx.Err()
	return f.err
}

// fakeNotifier is a test implementation of review.Notifier.
type fakeNotifier struct {
	err error

	mu     sync.Mutex
	events []review.Notification
	ctxErr error
	// remaining is the time left on the context at notification. It is used to check that the
	// notification does not share its budget with publishing.
	remaining time.Duration
}

func (f *fakeNotifier) Notify(ctx context.Context, n review.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.events = append(f.events, n)
	f.ctxErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		f.remaining = time.Until(deadline)
	}
	return f.err
}

func (f *fakeNotifier) last(t *testing.T) review.Notification {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.events) == 0 {
		t.Fatal("no notification was sent")
	}
	return f.events[len(f.events)-1]
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

// harness is a full pipeline set wired with the default dependencies.
type harness struct {
	factory   *fakeFactory
	source    *fakeSource
	prompts   *fakePrompts
	reviewer  *fakeReviewer
	publisher *fakePublisher
	notifier  *fakeNotifier
	pipeline  *Pipeline
}

func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()

	source := &fakeSource{diff: "diff --git a/main.go b/main.go", dir: "/tmp/workdir"}
	h := &harness{
		source:    source,
		factory:   &fakeFactory{source: source},
		prompts:   &fakePrompts{prompt: "レビューしてください"},
		reviewer:  &fakeReviewer{report: testReport()},
		publisher: &fakePublisher{},
		notifier:  &fakeNotifier{},
	}

	p, err := New(Deps{
		Sources:           h.factory,
		Prompts:           h.prompts,
		WorkspaceReviewer: h.reviewer,
		Publisher:         h.publisher,
		Notifier:          h.notifier,
	}, append([]Option{WithLogger(discardLogger())}, opts...)...)
	if err != nil {
		t.Fatalf("failed to create the pipeline: %v", err)
	}

	h.pipeline = p
	return h
}

var errBoom = errors.New("boom")
