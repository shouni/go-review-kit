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

// テスト用のリクエストです。
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

// discardLogger は、テスト出力を汚さないロガーです。
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSource は review.DiffSource のテスト実装です。
type fakeSource struct {
	diff     string
	diffErr  error
	closeErr error

	mu        sync.Mutex
	closed    bool
	closedCtx context.Context
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

// fakeFactory は review.DiffSourceFactory のテスト実装です。
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

// fakePrompts は review.PromptGenerator のテスト実装です。
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

// fakeReviewer は review.Reviewer のテスト実装です。
type fakeReviewer struct {
	report review.Report
	err    error

	called    bool
	gotModel  string
	gotPrompt string
}

func (f *fakeReviewer) Review(_ context.Context, model, prompt string) (review.Report, error) {
	f.called = true
	f.gotModel, f.gotPrompt = model, prompt
	if f.err != nil {
		return review.Report{}, f.err
	}
	return f.report, nil
}

// fakePublisher は review.Publisher のテスト実装です。
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

// fakeNotifier は review.Notifier のテスト実装です。
type fakeNotifier struct {
	err error

	mu     sync.Mutex
	events []review.Notification
	ctxErr error
	// remaining は通知時点で context に残っていた時間です。公開と予算を
	// 食い合っていないことの確認に使います。
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
		t.Fatal("通知が1件も行われていません")
	}
	return f.events[len(f.events)-1]
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

// harness は、既定の依存を組んだパイプライン一式です。
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

	source := &fakeSource{diff: "diff --git a/main.go b/main.go"}
	h := &harness{
		source:    source,
		factory:   &fakeFactory{source: source},
		prompts:   &fakePrompts{prompt: "レビューしてください"},
		reviewer:  &fakeReviewer{report: testReport()},
		publisher: &fakePublisher{},
		notifier:  &fakeNotifier{},
	}

	p, err := New(Deps{
		Sources:   h.factory,
		Prompts:   h.prompts,
		Reviewer:  h.reviewer,
		Publisher: h.publisher,
		Notifier:  h.notifier,
	}, append([]Option{WithLogger(discardLogger())}, opts...)...)
	if err != nil {
		t.Fatalf("パイプラインの生成に失敗: %v", err)
	}

	h.pipeline = p
	return h
}

var errBoom = errors.New("boom")
