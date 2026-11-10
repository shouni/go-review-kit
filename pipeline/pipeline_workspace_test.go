package pipeline

import (
	"context"
	"testing"

	"github.com/shouni/go-review-kit/review"
)

// The working directory the reviewer receives has Head checked out.
//
// Diff builds the diff purely by comparing objects without touching the working tree, so skipping
// the checkout would have the reviewer read the contents of another ref left by a previous run.
func TestRunChecksOutHeadBeforeReview(t *testing.T) {
	h := newHarness(t)
	req := testRequest()

	if _, _, err := h.pipeline.Run(context.Background(), req); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if h.source.gotHead != req.Head {
		t.Errorf("head passed to CheckoutHead = %q, want %q", h.source.gotHead, req.Head)
	}
	want := review.Workspace{Dir: h.source.dir, Base: req.Base, Head: req.Head}
	if h.reviewer.gotWS != want {
		t.Errorf("Workspace = %+v, want %+v", h.reviewer.gotWS, want)
	}
}
