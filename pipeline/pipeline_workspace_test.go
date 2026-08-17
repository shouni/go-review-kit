package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shouni/go-review-kit/review"
)

// fakeWorkspaceSource は、review.WorkspaceProvider を満たす fakeSource です。
type fakeWorkspaceSource struct {
	fakeSource
	dir         string
	checkoutErr error

	gotHead string
}

func (f *fakeWorkspaceSource) CheckoutHead(_ context.Context, head string) (string, error) {
	f.gotHead = head
	if f.checkoutErr != nil {
		return "", f.checkoutErr
	}
	return f.dir, nil
}

// fakeWorkspaceReviewer は、review.WorkspaceReviewer のテスト実装です。
type fakeWorkspaceReviewer struct {
	report review.Report
	err    error

	called    bool
	gotModel  string
	gotPrompt string
	gotWS     review.Workspace
}

func (f *fakeWorkspaceReviewer) ReviewWorkspace(_ context.Context, model, prompt string, ws review.Workspace) (review.Report, error) {
	f.called = true
	f.gotModel, f.gotPrompt, f.gotWS = model, prompt, ws
	if f.err != nil {
		return review.Report{}, f.err
	}
	return f.report, nil
}

// workspaceHarness は、WorkspaceReviewer を設定したパイプライン一式です。
type workspaceHarness struct {
	source    *fakeWorkspaceSource
	reviewer  *fakeWorkspaceReviewer
	publisher *fakePublisher
	notifier  *fakeNotifier
	pipeline  *Pipeline
}

func newWorkspaceHarness(t *testing.T, factory review.DiffSourceFactory, source *fakeWorkspaceSource) *workspaceHarness {
	t.Helper()

	h := &workspaceHarness{
		source:    source,
		reviewer:  &fakeWorkspaceReviewer{report: testReport()},
		publisher: &fakePublisher{},
		notifier:  &fakeNotifier{},
	}

	p, err := New(Deps{
		Sources:           factory,
		Prompts:           &fakePrompts{prompt: "レビューしてください"},
		WorkspaceReviewer: h.reviewer,
		Publisher:         h.publisher,
		Notifier:          h.notifier,
	}, WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("パイプラインの生成に失敗: %v", err)
	}

	h.pipeline = p
	return h
}

// workspaceFactory は、fakeWorkspaceSource を返す review.DiffSourceFactory です。
type workspaceFactory struct {
	source *fakeWorkspaceSource
}

func (f *workspaceFactory) Open(context.Context, review.Request) (review.DiffSource, error) {
	return f.source, nil
}

func TestRunWithWorkspaceReviewer(t *testing.T) {
	source := &fakeWorkspaceSource{
		fakeSource: fakeSource{diff: "diff --git a/main.go b/main.go"},
		dir:        "/tmp/workdir",
	}
	h := newWorkspaceHarness(t, &workspaceFactory{source: source}, source)
	req := testRequest()

	result, report, err := h.pipeline.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run が失敗しました: %v", err)
	}
	if result.Status != review.StatusSucceeded {
		t.Errorf("Status = %v, want %v", result.Status, review.StatusSucceeded)
	}
	if report == nil {
		t.Fatal("Report が nil です")
	}

	if !h.reviewer.called {
		t.Fatal("WorkspaceReviewer が呼ばれていません")
	}
	if source.gotHead != req.Head {
		t.Errorf("CheckoutHead に渡された head = %q, want %q", source.gotHead, req.Head)
	}
	want := review.Workspace{Dir: "/tmp/workdir", Base: req.Base, Head: req.Head}
	if h.reviewer.gotWS != want {
		t.Errorf("Workspace = %+v, want %+v", h.reviewer.gotWS, want)
	}
	if !h.publisher.called {
		t.Error("Publisher が呼ばれていません")
	}
}

func TestRunWorkspaceReviewerRequiresProvider(t *testing.T) {
	// WorkspaceProvider を満たさない素の fakeSource を返す factory を使います。
	source := &fakeSource{diff: "diff --git a/main.go b/main.go"}
	reviewer := &fakeWorkspaceReviewer{report: testReport()}
	notifier := &fakeNotifier{}

	p, err := New(Deps{
		Sources:           &fakeFactory{source: source},
		Prompts:           &fakePrompts{prompt: "レビューしてください"},
		WorkspaceReviewer: reviewer,
		Publisher:         &fakePublisher{},
		Notifier:          notifier,
	}, WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("パイプラインの生成に失敗: %v", err)
	}

	_, _, err = p.Run(context.Background(), testRequest())
	if err == nil {
		t.Fatal("エラーが返っていません")
	}
	if got := review.StepOf(err); got != review.StepCheckout {
		t.Errorf("StepOf = %q, want %q", got, review.StepCheckout)
	}
	if reviewer.called {
		t.Error("能力が無いのに WorkspaceReviewer が呼ばれています")
	}
}

func TestRunWorkspaceCheckoutError(t *testing.T) {
	source := &fakeWorkspaceSource{
		fakeSource:  fakeSource{diff: "diff --git a/main.go b/main.go"},
		checkoutErr: errBoom,
	}
	h := newWorkspaceHarness(t, &workspaceFactory{source: source}, source)

	_, _, err := h.pipeline.Run(context.Background(), testRequest())
	if !errors.Is(err, errBoom) {
		t.Fatalf("errBoom が返っていません: %v", err)
	}
	if got := review.StepOf(err); got != review.StepCheckout {
		t.Errorf("StepOf = %q, want %q", got, review.StepCheckout)
	}
	if h.reviewer.called {
		t.Error("チェックアウト失敗後に WorkspaceReviewer が呼ばれています")
	}
}

func TestRunWorkspaceReviewError(t *testing.T) {
	source := &fakeWorkspaceSource{
		fakeSource: fakeSource{diff: "diff --git a/main.go b/main.go"},
		dir:        "/tmp/workdir",
	}
	h := newWorkspaceHarness(t, &workspaceFactory{source: source}, source)
	h.reviewer.err = errBoom

	_, _, err := h.pipeline.Run(context.Background(), testRequest())
	if !errors.Is(err, errBoom) {
		t.Fatalf("errBoom が返っていません: %v", err)
	}
	if got := review.StepOf(err); got != review.StepReview {
		t.Errorf("StepOf = %q, want %q", got, review.StepReview)
	}
}

func TestNewRejectsAmbiguousReviewers(t *testing.T) {
	deps := Deps{
		Sources:           &fakeFactory{source: &fakeSource{}},
		Prompts:           &fakePrompts{},
		Reviewer:          &fakeReviewer{},
		WorkspaceReviewer: &fakeWorkspaceReviewer{},
		Publisher:         &fakePublisher{},
		Notifier:          &fakeNotifier{},
	}

	if _, err := New(deps); err == nil {
		t.Fatal("両方のレビュアーを設定してもエラーになりません")
	}
}

func TestNewRequiresSomeReviewer(t *testing.T) {
	deps := Deps{
		Sources:   &fakeFactory{source: &fakeSource{}},
		Prompts:   &fakePrompts{},
		Publisher: &fakePublisher{},
		Notifier:  &fakeNotifier{},
	}

	_, err := New(deps)
	if err == nil {
		t.Fatal("レビュアー未設定でもエラーになりません")
	}
	if !strings.Contains(err.Error(), "Reviewer") {
		t.Errorf("エラーにレビュアーの不足が含まれていません: %v", err)
	}
}
