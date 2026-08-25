package pipeline

import (
	"context"
	"testing"

	"github.com/shouni/go-review-kit/review"
)

// レビュアーが受け取る作業ディレクトリは、Head をチェックアウトした状態であること。
//
// Diff は作業ツリーに触れずオブジェクト比較だけで差分を作るため、チェックアウトを省くと
// **レビュアーは前回の実行が残した別参照の内容を読みます。**
func TestRunChecksOutHeadBeforeReview(t *testing.T) {
	h := newHarness(t)
	req := testRequest()

	if _, _, err := h.pipeline.Run(context.Background(), req); err != nil {
		t.Fatalf("Run が失敗しました: %v", err)
	}

	if h.source.gotHead != req.Head {
		t.Errorf("CheckoutHead に渡された head = %q, want %q", h.source.gotHead, req.Head)
	}
	want := review.Workspace{Dir: h.source.dir, Base: req.Base, Head: req.Head}
	if h.reviewer.gotWS != want {
		t.Errorf("Workspace = %+v, want %+v", h.reviewer.gotWS, want)
	}
}

// 作業ディレクトリを用意できない DiffSource は、レビューへ進む前に落とすこと。
//
// レビュアーは 1 種類しかないため、この能力は常に要ります。型アサーションで確かめるので、
// 失敗するのは Open のあと（StepCheckout）です。
func TestRunRequiresWorkspaceProvider(t *testing.T) {
	reviewer := &fakeReviewer{report: testReport()}
	notifier := &fakeNotifier{}

	p, err := New(Deps{
		Sources:           &bareFactory{source: &bareSource{diff: "diff --git a/main.go b/main.go"}},
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
		t.Error("能力が無いのにレビュアーが呼ばれています")
	}
	if notifier.count() != 1 {
		t.Errorf("通知回数 = %d, want 1", notifier.count())
	}
}
