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
