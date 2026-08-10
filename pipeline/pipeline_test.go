package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shouni/go-review-kit/review"
)

func TestNewRejectsMissingDeps(t *testing.T) {
	full := Deps{
		Sources:   &fakeFactory{},
		Prompts:   &fakePrompts{},
		Reviewer:  &fakeReviewer{},
		Publisher: &fakePublisher{},
		Notifier:  &fakeNotifier{},
	}

	tests := []struct {
		name   string
		mutate func(*Deps)
	}{
		{"Sources が nil", func(d *Deps) { d.Sources = nil }},
		{"Prompts が nil", func(d *Deps) { d.Prompts = nil }},
		{"Reviewer が nil", func(d *Deps) { d.Reviewer = nil }},
		{"Publisher が nil", func(d *Deps) { d.Publisher = nil }},
		{"Notifier が nil", func(d *Deps) { d.Notifier = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := full
			tt.mutate(&deps)

			if _, err := New(deps); err == nil {
				t.Fatal("エラーを期待しましたが nil でした")
			}
		})
	}

	if _, err := New(full); err != nil {
		t.Fatalf("完全な依存で失敗しました: %v", err)
	}
}

func TestRunSuccess(t *testing.T) {
	h := newHarness(t)

	result, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}

	if result.Status != review.StatusSucceeded {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusSucceeded)
	}
	if !result.Published() {
		t.Error("Published() が false です")
	}
	if !h.publisher.called {
		t.Error("Publish が呼ばれていません")
	}
	if !h.source.wasClosed() {
		t.Error("差分取得元が解放されていません")
	}

	// プロンプトは差分から組み立てられ、そのままレビューへ渡ります。
	if h.prompts.gotDiff != h.source.diff || h.prompts.gotMode != "detail" {
		t.Errorf("プロンプト生成の入力が一致しません: mode=%q diff=%q", h.prompts.gotMode, h.prompts.gotDiff)
	}
	if h.reviewer.gotPrompt != h.prompts.prompt || h.reviewer.gotModel != "gemini-2.5-pro" {
		t.Errorf("レビューの入力が一致しません: model=%q prompt=%q", h.reviewer.gotModel, h.reviewer.gotPrompt)
	}

	notification := h.notifier.last(t)
	if notification.Err != nil {
		t.Errorf("成功時に Err が入っています: %v", notification.Err)
	}
	if notification.Report == nil {
		t.Fatal("成功時は Report が渡されるべきです")
	}
	if len(notification.Report.Findings) != 1 {
		t.Errorf("Report の中身が一致しません: %+v", notification.Report)
	}
}

// 差分が無いのは失敗ではありません。公開は行わず、通知だけ行って
// StatusSkipped と nil を返します。
func TestRunSkipsWhenDiffIsEmpty(t *testing.T) {
	tests := []struct {
		name string
		diff string
	}{
		{"完全に空", ""},
		{"空白のみ", "  \n\t "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.source.diff = tt.diff

			result, err := h.pipeline.Run(context.Background(), testRequest())
			if err != nil {
				t.Fatalf("スキップはエラーにすべきではありません: %v", err)
			}

			if result.Status != review.StatusSkipped {
				t.Errorf("Status = %q, want %q", result.Status, review.StatusSkipped)
			}
			if result.Published() {
				t.Error("スキップ時に Published() が true です")
			}
			if h.reviewer.called {
				t.Error("差分が無いのに AI が呼ばれています")
			}
			if h.publisher.called {
				t.Error("差分が無いのに公開が行われています")
			}
			if h.notifier.count() != 1 {
				t.Errorf("通知回数 = %d, want 1", h.notifier.count())
			}
			if !h.source.wasClosed() {
				t.Error("差分取得元が解放されていません")
			}
		})
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
			name:     "リポジトリの準備に失敗",
			arrange:  func(h *harness) { h.factory.openErr = errBoom },
			wantStep: review.StepPrepare,
		},
		{
			name:     "差分取得に失敗",
			arrange:  func(h *harness) { h.source.diffErr = errBoom },
			wantStep: review.StepDiff,
		},
		{
			name:     "プロンプト生成に失敗",
			arrange:  func(h *harness) { h.prompts.err = errBoom },
			wantStep: review.StepPrompt,
		},
		{
			name:     "AIレビューに失敗",
			arrange:  func(h *harness) { h.reviewer.err = errBoom },
			wantStep: review.StepReview,
		},
		{
			name:        "公開に失敗",
			arrange:     func(h *harness) { h.publisher.err = errBoom },
			wantStep:    review.StepPublish,
			wantPublish: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.arrange(h)

			result, err := h.pipeline.Run(context.Background(), testRequest())
			if err == nil {
				t.Fatal("エラーを期待しましたが nil でした")
			}
			if !errors.Is(err, errBoom) {
				t.Fatalf("原因まで辿れません: %v", err)
			}
			if got := review.StepOf(err); got != tt.wantStep {
				t.Errorf("工程名 = %q, want %q", got, tt.wantStep)
			}
			if result.Status != review.StatusFailed {
				t.Errorf("Status = %q, want %q", result.Status, review.StatusFailed)
			}
			if h.publisher.called != tt.wantPublish {
				t.Errorf("Publish の呼び出し = %v, want %v", h.publisher.called, tt.wantPublish)
			}

			// 失敗した場合こそ通知が必要です。
			notification := h.notifier.last(t)
			if notification.Err == nil {
				t.Error("通知に原因が渡されていません")
			}
			if notification.Result.Status != review.StatusFailed {
				t.Errorf("通知の Status = %q", notification.Result.Status)
			}
		})
	}
}

func TestRunRejectsInvalidRequest(t *testing.T) {
	h := newHarness(t)

	req := testRequest()
	req.RepoURL = ""

	result, err := h.pipeline.Run(context.Background(), req)
	if !errors.Is(err, review.ErrInvalidRequest) {
		t.Fatalf("ErrInvalidRequest を期待しましたが: %v", err)
	}
	if got := review.StepOf(err); got != review.StepValidate {
		t.Errorf("工程名 = %q, want %q", got, review.StepValidate)
	}
	if result.Status != review.StatusFailed {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusFailed)
	}
	// 検証で弾いた時点でリポジトリには触れません。
	if h.source.wasClosed() {
		t.Error("準備していない差分取得元が解放されています")
	}
	if h.notifier.count() != 1 {
		t.Errorf("通知回数 = %d, want 1", h.notifier.count())
	}
}

// 公開・通知・後始末は呼び出し元の締切から切り離します。レビューが締切で打ち切られた
// 直後こそ通知が必要なのに、期限切れの context を引き継ぐと通知まで道連れで失敗するためです。
func TestRunDetachesPublishFromCallerDeadline(t *testing.T) {
	h := newHarness(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 呼び出し元は既に打ち切られている

	result, err := h.pipeline.Run(ctx, testRequest())
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if result.Status != review.StatusSucceeded {
		t.Fatalf("Status = %q", result.Status)
	}

	if h.publisher.ctxErr != nil {
		t.Errorf("公開に期限切れの context が渡されています: %v", h.publisher.ctxErr)
	}
	if h.notifier.ctxErr != nil {
		t.Errorf("通知に期限切れの context が渡されています: %v", h.notifier.ctxErr)
	}
}

// 通知の失敗はパイプライン全体の失敗にしません。成果物は既に保存済みだからです。
func TestRunIgnoresNotifierFailure(t *testing.T) {
	h := newHarness(t)
	h.notifier.err = errBoom

	result, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("通知の失敗で全体が失敗しています: %v", err)
	}
	if result.Status != review.StatusSucceeded {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusSucceeded)
	}
}

// 後始末の失敗も同様に、レビュー結果を捨てる理由にはしません。
func TestRunIgnoresCloseFailure(t *testing.T) {
	h := newHarness(t)
	h.source.closeErr = errBoom

	result, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("後始末の失敗で全体が失敗しています: %v", err)
	}
	if result.Status != review.StatusSucceeded {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusSucceeded)
	}
}

func TestRunRecordsDuration(t *testing.T) {
	h := newHarness(t)

	result, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if result.Duration <= 0 {
		t.Fatalf("Duration = %v, want 正の値", result.Duration)
	}
}

func TestOptions(t *testing.T) {
	t.Run("既定値", func(t *testing.T) {
		h := newHarness(t)
		if h.pipeline.publishTimeout != DefaultPublishTimeout {
			t.Errorf("publishTimeout = %v, want %v", h.pipeline.publishTimeout, DefaultPublishTimeout)
		}
		if h.pipeline.logger == nil {
			t.Error("logger が nil です")
		}
	})

	t.Run("WithPublishTimeout", func(t *testing.T) {
		h := newHarness(t, WithPublishTimeout(30*time.Second))
		if h.pipeline.publishTimeout != 30*time.Second {
			t.Errorf("publishTimeout = %v", h.pipeline.publishTimeout)
		}
	})

	t.Run("不正な値は無視される", func(t *testing.T) {
		h := newHarness(t, WithPublishTimeout(0), WithLogger(nil))
		if h.pipeline.publishTimeout != DefaultPublishTimeout {
			t.Errorf("publishTimeout = %v, want %v", h.pipeline.publishTimeout, DefaultPublishTimeout)
		}
		if h.pipeline.logger == nil {
			t.Error("nil ロガーで上書きされています")
		}
	})
}
