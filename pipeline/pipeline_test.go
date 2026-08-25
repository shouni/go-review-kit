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
		{"Sources が nil", func(d *Deps) { d.Sources = nil }},
		{"Prompts が nil", func(d *Deps) { d.Prompts = nil }},
		{"WorkspaceReviewer が nil", func(d *Deps) { d.WorkspaceReviewer = nil }},
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

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
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

			result, _, err := h.pipeline.Run(context.Background(), testRequest())
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

// 上限を超えた差分は、AI へ送る前に失敗させます。送ってしまうと、失敗が分かるのは
// モデルを呼び終えたあと（出力の途中切れ・締切超過）になり、いちばんコストを払った
// あとで落ちることになります。
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
		t.Error("レビューしていないのに Report が返っています")
	}
	if result.Status != review.StatusFailed {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusFailed)
	}

	// 上限は「送る前に落とす」ためのものなので、プロンプト生成にも到達しません。
	if h.prompts.gotDiff != "" {
		t.Error("上限を超えたのにプロンプトが組み立てられています")
	}
	if h.reviewer.called {
		t.Error("上限を超えたのに AI が呼ばれています")
	}
	if h.publisher.called {
		t.Error("上限を超えたのに公開が行われています")
	}
	if !h.source.wasClosed() {
		t.Error("差分取得元が解放されていません")
	}

	// スキップではなく失敗なので、通知にはエラーが載ります。載らないと利用者は
	// 範囲を絞るという手を打てません。
	if n := h.notifier.count(); n != 1 {
		t.Fatalf("通知回数 = %d, want 1", n)
	}
	if last := h.notifier.last(t); !errors.Is(last.Err, review.ErrDiffTooLarge) {
		t.Errorf("通知の Err = %v, want review.ErrDiffTooLarge", last.Err)
	}
}

// 上限ちょうどは通します。境界を外すと、上限を実測から決めた利用側で
// 「その値ちょうどの差分だけが理由もなく落ちる」という形になります。
func TestMaxDiffBytesBoundary(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{"上限ちょうど", 16, false},
		{"1 バイト超過", 17, true},
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

// 既定は無制限です。上限を持つかどうかは利用側の判断なので、ライブラリが勝手に
// 数字を決めると、設定していない呼び出し側の挙動が黙って変わります。
func TestMaxDiffBytesDefaultsToUnlimited(t *testing.T) {
	h := newHarness(t)
	if h.pipeline.maxDiffBytes != 0 {
		t.Errorf("maxDiffBytes = %d, want 0（無制限）", h.pipeline.maxDiffBytes)
	}

	h.source.diff = strings.Repeat("a", 1<<20)
	if _, _, err := h.pipeline.Run(context.Background(), testRequest()); err != nil {
		t.Fatalf("未設定なら大きさで落ちてはいけません: %v", err)
	}
}

// 0 以下は無視されること（WithRunTimeout / WithPublishTimeout と同じ規則）。
func TestMaxDiffBytesIgnoresNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		h := newHarness(t, WithMaxDiffBytes(n))
		if h.pipeline.maxDiffBytes != 0 {
			t.Errorf("WithMaxDiffBytes(%d) で maxDiffBytes = %d, want 0", n, h.pipeline.maxDiffBytes)
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
			name:     "Headチェックアウトに失敗",
			arrange:  func(h *harness) { h.source.checkoutErr = errBoom },
			wantStep: review.StepCheckout,
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

			result, _, err := h.pipeline.Run(context.Background(), testRequest())
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

	result, _, err := h.pipeline.Run(context.Background(), req)
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

	result, _, err := h.pipeline.Run(ctx, testRequest())
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

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
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

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("後始末の失敗で全体が失敗しています: %v", err)
	}
	if result.Status != review.StatusSucceeded {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusSucceeded)
	}
}

func TestRunRecordsDuration(t *testing.T) {
	h := newHarness(t)

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
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

// Run は Report も返します。レビューの中身を必要とする処理（ジョブ状態の記録など）が、
// 通知フックへ相乗りせずに済むようにするためです。
func TestRunReturnsReport(t *testing.T) {
	h := newHarness(t)

	_, report, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if report == nil {
		t.Fatal("成功時は Report が返るべきです")
	}
	if report.Title != testReport().Title {
		t.Errorf("Title = %q, want %q", report.Title, testReport().Title)
	}
	if len(report.Findings) != 1 {
		t.Errorf("findings 件数 = %d, want 1", len(report.Findings))
	}
}

// レビューへ到達しなかった場合は Report が nil です。
func TestRunReturnsNilReportWithoutReview(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*harness)
	}{
		{"差分なし", func(h *harness) { h.source.diff = "" }},
		{"準備に失敗", func(h *harness) { h.factory.openErr = errBoom }},
		{"AIレビューに失敗", func(h *harness) { h.reviewer.err = errBoom }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.arrange(h)

			_, report, _ := h.pipeline.Run(context.Background(), testRequest())
			if report != nil {
				t.Fatalf("Report は nil であるべきです: %+v", report)
			}
		})
	}
}

// 保存に失敗した場合はレビュー自体は成立しているため、Report とエラーの両方が返ります。
// 呼び出し側が「レビューはできたが残せなかった」を区別できます。
func TestRunReturnsReportWhenPublishFails(t *testing.T) {
	h := newHarness(t)
	h.publisher.err = errBoom

	_, report, err := h.pipeline.Run(context.Background(), testRequest())
	if err == nil {
		t.Fatal("エラーを期待しましたが nil でした")
	}
	if report == nil {
		t.Fatal("レビューは成立しているため Report が返るべきです")
	}
	if report.Title != testReport().Title {
		t.Errorf("Title = %q", report.Title)
	}
}

// 検証で弾いた場合も Report は nil です。
func TestRunReturnsNilReportForInvalidRequest(t *testing.T) {
	h := newHarness(t)

	req := testRequest()
	req.RepoURL = ""

	_, report, err := h.pipeline.Run(context.Background(), req)
	if err == nil {
		t.Fatal("エラーを期待しましたが nil でした")
	}
	if report != nil {
		t.Fatalf("Report は nil であるべきです: %+v", report)
	}
}

// slowReviewer は、context がキャンセルされるまで待つ review.WorkspaceReviewer です。
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

// WithRunTimeout がレビュー本体にだけ締切を掛けること。
//
// 呼び出し側が Run へ渡す context に自分で締切を被せると、打ち切られた直後は
// context が期限切れなので、失敗を報告する通知まで道連れになります。
// ★ この上限を使えば、打ち切られても通知は届きます。
func TestRunTimeoutBoundsReviewButNotNotify(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithRunTimeout(20*time.Millisecond))
	slow := &slowReviewer{}
	h.pipeline.deps.WorkspaceReviewer = slow

	result, report, err := h.pipeline.Run(context.Background(), testRequest())

	if err == nil {
		t.Fatal("締切で打ち切られていません")
	}
	if !slow.hadDeadline {
		t.Error("レビュアーへ渡った context に締切がありません")
	}
	if report != nil {
		t.Errorf("打ち切り時に report が返っています: %+v", report)
	}
	if result.Status != review.StatusFailed {
		t.Errorf("Status = %q, want %q", result.Status, review.StatusFailed)
	}
	// ここが要点です。締切に巻き込まれず通知まで届くこと。
	if got := h.notifier.count(); got != 1 {
		t.Fatalf("通知回数 = %d, want 1（打ち切りでも失敗通知は届くべきです）", got)
	}
}

// 既定は無制限であること。既存の利用者の挙動を変えないための確認です。
func TestRunTimeoutDefaultsToUnlimited(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if h.pipeline.runTimeout != 0 {
		t.Errorf("runTimeout = %v, want 0（無制限）", h.pipeline.runTimeout)
	}

	probe := &deadlineProbe{report: testReport()}
	h.pipeline.deps.WorkspaceReviewer = probe
	if _, _, err := h.pipeline.Run(context.Background(), testRequest()); err != nil {
		t.Fatalf("Run が失敗しました: %v", err)
	}
	if probe.hadDeadline {
		t.Error("未設定なのにレビュアーの context へ締切が付いています")
	}
}

// 0 以下は無視されること（WithPublishTimeout と同じ規則）。
func TestRunTimeoutIgnoresNonPositive(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -time.Second} {
		h := newHarness(t, WithRunTimeout(d))
		if h.pipeline.runTimeout != 0 {
			t.Errorf("WithRunTimeout(%v) で runTimeout = %v, want 0", d, h.pipeline.runTimeout)
		}
	}
}

// deadlineProbe は、渡された context に締切があるかだけを見る review.WorkspaceReviewer です。
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

// slowPublisher は、context が切れるまで粘って失敗する review.Publisher です。
type slowPublisher struct{}

func (slowPublisher) Publish(ctx context.Context, _ review.Request, _ review.Report) error {
	<-ctx.Done()
	return ctx.Err()
}

// ★ 公開が予算を使い切って失敗しても、失敗通知は届くこと。
//
// 公開に渡した context をそのまま通知へ回すと、既に期限切れなので通知は必ず失敗します。
// 「いちばん報告が必要な場面でだけ届かない」という裏返った挙動になるため、
// 通知には別の予算を与えます。
func TestPublishTimeoutDoesNotStarveFailureNotify(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithPublishTimeout(20*time.Millisecond))
	h.pipeline.deps.Publisher = slowPublisher{}

	_, report, err := h.pipeline.Run(context.Background(), testRequest())
	if err == nil {
		t.Fatal("公開の失敗が返っていません")
	}
	// レビュー自体は成立しているので Report は返ります。
	if report == nil {
		t.Error("公開に失敗しても Report は返すべきです")
	}

	if got := h.notifier.count(); got != 1 {
		t.Fatalf("通知回数 = %d, want 1", got)
	}
	if h.notifier.ctxErr != nil {
		t.Errorf("通知が期限切れの context で行われています: %v", h.notifier.ctxErr)
	}
}

// 成功時も、通知が公開と予算を食い合わないこと。
func TestPublishAndNotifyGetSeparateBudgets(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithPublishTimeout(50*time.Millisecond))
	h.pipeline.deps.Publisher = sleepyPublisher{d: 30 * time.Millisecond}

	if _, _, err := h.pipeline.Run(context.Background(), testRequest()); err != nil {
		t.Fatalf("Run が失敗しました: %v", err)
	}
	if h.notifier.ctxErr != nil {
		t.Errorf("通知の context が公開に削られています: %v", h.notifier.ctxErr)
	}

	// 通知に渡った締切が、公開で消費したぶんだけ短くなっていないこと。
	if remaining := h.notifier.remaining; remaining < 40*time.Millisecond {
		t.Errorf("通知の残り時間 = %v, 公開と予算を共有しています", remaining)
	}
}

// sleepyPublisher は、一定時間かけてから成功する review.Publisher です。
type sleepyPublisher struct{ d time.Duration }

func (s sleepyPublisher) Publish(ctx context.Context, _ review.Request, _ review.Report) error {
	select {
	case <-time.After(s.d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// nilSources は、生成に失敗した実装（typed-nil）を模した型です。
type nilSources struct{}

func (*nilSources) Open(context.Context, review.Request) (review.DiffSource, error) {
	return nil, nil
}

// 生成に失敗した実装（nil ポインタ）を渡されたら、起動時に落とすこと。
//
// 素の == nil では typed-nil を見逃し、検証を通過したあと最初に呼んだ時点で
// nil ポインタ参照になります。依存の検証は起動時に落とすためにあります。
func TestValidateRejectsTypedNilDependency(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	deps := h.pipeline.deps
	deps.Sources = (*nilSources)(nil)

	_, err := New(deps)
	if err == nil {
		t.Fatal("typed-nil の依存が素通りしました")
	}
	if !strings.Contains(err.Error(), "Sources") {
		t.Errorf("エラーに項目名がありません: %v", err)
	}
}

// typed-nil のレビュアーは未設定として弾くこと。
//
// 素の == nil では見逃すため、通すと**最初に呼んだ時点で nil ポインタ参照**になります。
func TestValidateTreatsTypedNilReviewerAsUnset(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	deps := h.pipeline.deps
	deps.WorkspaceReviewer = (*deadlineProbe)(nil)

	_, err := New(deps)
	if err == nil {
		t.Fatal("typed-nil のレビュアーが素通りしました")
	}
	if !strings.Contains(err.Error(), "WorkspaceReviewer") {
		t.Errorf("エラーに項目名がありません: %v", err)
	}
}

// nil の Option を渡してもパニックしないこと。
func TestNewIgnoresNilOption(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if _, err := New(h.pipeline.deps, nil, WithPublishTimeout(time.Second), nil); err != nil {
		t.Fatalf("nil Option でエラーになりました: %v", err)
	}
}

// ★ 計測値は失敗した実行でも残ること。
//
// **上限が厳しすぎるかどうかを判断する材料は、通った実行より弾かれた実行の側にあります。**
// 成功したときだけ数字が残る作りだと、分布の裾がいちばん見たいときに欠けます。
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
		{"成功", func(*harness) {}, review.StatusSucceeded},
		{"AIレビューに失敗", func(h *harness) { h.reviewer.err = errBoom }, review.StatusFailed},
		{"公開に失敗", func(h *harness) { h.publisher.err = errBoom }, review.StatusFailed},
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

// レビューへ到達しなかった実行でも、差分の大きさまでは残ること。
// 上限に弾かれた実行こそ、どのくらい超えていたのかを後から見たい対象です。
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
		t.Errorf("レビューしていないのに Run = %+v", result.Run)
	}
}

// 差分が無い実行では、どちらもゼロ値であること。
func TestRunLeavesStatsEmptyWhenSkipped(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.source.diff = ""

	result, _, err := h.pipeline.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Run が失敗しました: %v", err)
	}
	if result.DiffBytes != 0 || result.Run != (review.RunInfo{}) {
		t.Errorf("スキップなのに DiffBytes = %d, Run = %+v", result.DiffBytes, result.Run)
	}
}
