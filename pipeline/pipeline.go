// Package pipeline は、レビュー要求を受け取ってから成果物を公開・通知するまでの
// 一連の流れを組み立てます。
//
// 工程は 1 階層です。全体制御と各工程を分けても、上の層は下へ委譲するだけになり、
// 追う場所が増えるわりに得るものがありません。Run が唯一の入口で、Result と Report と
// error を返します。
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

// DefaultPublishTimeout は、公開と通知に与える既定の上限です。
const DefaultPublishTimeout = 2 * time.Minute

// Deps は、パイプラインが依存する外部実装です。
//
// 位置引数ではなく構造体で受け取るのは、同じ型（インターフェース）の引数が並ぶと
// 順序を取り違えてもコンパイルが通ってしまうためです。
type Deps struct {
	Sources review.DiffSourceFactory
	// Prompts は、モードと差分から AI へ送るプロンプトを組み立てます。
	//
	// **Reviewer と WorkspaceReviewer で Pipeline を 2 つ組む場合、ここだけは
	// 共有しないでください。** プロンプトは、実行するレビュアーに何ができるかを
	// 説明するものです。差分しか読めない Reviewer に「ファイルを開いて確認しろ」
	// 「確認した根拠を挙げろ」と書いたプロンプトを渡すと、確認する手段が無いまま
	// 根拠を求めることになり、モデルはそれを捏造して埋めます。
	//
	// Generate はモードと差分しか受け取らないため、レビュアーの違いは
	// **生成器そのものを分けて**表してください。
	Prompts review.PromptGenerator
	// Reviewer は、差分だけで完結する単発のレビュアーです。
	// WorkspaceReviewer とはどちらか一方だけを設定します。
	Reviewer review.Reviewer
	// WorkspaceReviewer は、作業ディレクトリを参照するレビュアーです。
	// 設定する場合、Sources が返す DiffSource は review.WorkspaceProvider を満たす
	// 必要があります（git パッケージの 2 実装はどちらも満たします）。
	// レビュアーの使い分けはリクエスト単位ではなくパイプライン単位です。モードごとに
	// 使い分けたい呼び出し側は、Prompts 以外のアダプターを共有した Pipeline を 2 つ
	// 組んでください（Prompts を共有してはいけない理由は同フィールドを参照）。
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
		{"Publisher", d.Publisher},
		{"Notifier", d.Notifier},
	} {
		if isNil(dep.value) {
			missing = append(missing, dep.name)
		}
	}

	// レビュアーは「どちらか一方だけ」です。両方設定されていると、どちらで実行される
	// つもりだったのかが Deps から読み取れず、静かに片方を無視すると設定ミスに気付けません。
	switch {
	case isNil(d.Reviewer) && isNil(d.WorkspaceReviewer):
		missing = append(missing, "Reviewer または WorkspaceReviewer")
	case !isNil(d.Reviewer) && !isNil(d.WorkspaceReviewer):
		return fmt.Errorf("pipeline: Reviewer と WorkspaceReviewer は同時に設定できません（どちらか一方にしてください）")
	}

	if len(missing) > 0 {
		return fmt.Errorf("pipeline: 依存が未設定です: %s", strings.Join(missing, ", "))
	}
	return nil
}

// isNil は、インターフェース値が nil か、nil ポインタを収めているかを返します。
//
// 素の `== nil` では後者を見逃します。生成に失敗した実装（(*T)(nil)）を渡されると
// 検証を素通りし、**最初に呼んだ時点で nil ポインタ参照**になります。依存の検証は
// 「起動時に落とす」ためにあるので、そこで捕まえます。
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

// Pipeline は、レビューの実行から公開・通知までを担います。
type Pipeline struct {
	deps           Deps
	logger         *slog.Logger
	publishTimeout time.Duration
	// runTimeout は差分取得〜AI レビューの上限です。0 は無制限。
	runTimeout time.Duration
	// maxDiffBytes は AI へ送る差分の上限（バイト）です。0 は無制限。
	maxDiffBytes int
}

// New は Pipeline を生成します。依存に nil が含まれる場合はエラーを返します。
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
		// 条件付きでオプションを組み立てる呼び出し側は nil を混ぜがちです。
		// パニックさせるほどの誤りではないので読み飛ばします。
		if opt == nil {
			continue
		}
		opt(p)
	}
	return p, nil
}

// Run は、レビュー要求の全工程を実行します。
//
// 戻り値の Report は、AI がレビューを返した場合のみ非 nil です。差分が無かった場合や、
// レビューへ到達する前に失敗した場合は nil になります。保存に失敗した場合は Report が
// 非 nil のままエラーも返るので、「レビューはできたが残せなかった」を呼び出し側で
// 区別できます。**戻り値を使う前に nil を確かめてください。**
//
// Report を返すのは、レビューの中身（題目・判定・指摘）を必要とする処理を通知フックへ
// 相乗りさせずに済ませるためです。ジョブの状態を記録する呼び出し側が、通知を名乗る
// アダプターを書かなくても戻り値から組み立てられます。
//
// 差分が無かった場合は StatusSkipped の Result と nil を返します（失敗ではないため）。
// 「実行はしたが成果物は無い」は Result.Published で判別できます。
//
// 締切の掛かる範囲は detach と WithRunTimeout / WithPublishTimeout を参照してください。
func (p *Pipeline) Run(ctx context.Context, req review.Request) (review.Result, *review.Report, error) {
	start := time.Now()

	if err := req.Validate(); err != nil {
		return p.fail(ctx, req, nil, review.WrapStep(review.StepValidate, err), time.Since(start))
	}

	// 締切はレビュー本体にだけ被せます（範囲の理由は WithRunTimeout を参照）。
	runCtx := ctx
	if p.runTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, p.runTimeout)
		defer cancel()
	}

	report, err := p.produce(runCtx, req)
	elapsed := time.Since(start)

	switch {
	case errors.Is(err, review.ErrEmptyDiff):
		result := review.Skipped(req, elapsed)
		p.logger.InfoContext(ctx, "差分が無いためレビューをスキップしました",
			"job_id", req.JobID, "repo_url", req.RepoURL, "base", req.Base, "head", req.Head)
		p.notify(ctx, review.Notification{Request: req, Result: result})
		return result, nil, nil

	case err != nil:
		return p.fail(ctx, req, nil, err, elapsed)
	}

	publishCtx, cancel := p.detach(ctx)
	defer cancel()

	if err := p.deps.Publisher.Publish(publishCtx, req, report); err != nil {
		// レビュー自体は成立しているため、Report は返します。
		// publishCtx ではなく ctx を渡します。公開が予算を使い切って失敗した場合、
		// publishCtx は既に期限切れなので、そのまま通知へ渡すと必ず不達になります。
		return p.fail(ctx, req, &report, review.WrapStep(review.StepPublish, err), elapsed)
	}

	result := review.Succeeded(req, elapsed)
	p.logger.InfoContext(ctx, "レビューパイプラインが完了しました",
		"job_id", req.JobID,
		"repo_url", req.RepoURL,
		"status", result.Status,
		"storage_uri", result.StorageURI,
		"findings", len(report.Findings),
		"duration", elapsed,
	)
	// 通知にも公開と別の予算を与えます。同じ context を使い回すと、公開に時間がかかった
	// ぶんだけ通知の持ち時間が削られます。
	p.notify(ctx, review.Notification{Request: req, Result: result, Report: &report})

	return result, &report, nil
}

// produce は、差分の取得から AI レビューまでを実行します。
func (p *Pipeline) produce(ctx context.Context, req review.Request) (review.Report, error) {
	source, err := p.deps.Sources.Open(ctx, req)
	if err != nil {
		return review.Report{}, review.WrapStep(review.StepPrepare, err)
	}
	defer p.close(ctx, source)

	diff, err := source.Diff(ctx, req.Base, req.Head)
	if err != nil {
		return review.Report{}, review.WrapStep(review.StepDiff, err)
	}
	if strings.TrimSpace(diff) == "" {
		return review.Report{}, review.ErrEmptyDiff
	}

	// ★ 大きすぎる差分は、送っても出力の途中切れか締切超過で失敗します。**どちらも
	// モデルを呼び終えたあとにしか分かりません。** 送る前に落として、利用者が手を
	// 打てる形で返します（切り詰めない理由は WithMaxDiffBytes）。
	if p.maxDiffBytes > 0 && len(diff) > p.maxDiffBytes {
		return review.Report{}, review.WrapStep(review.StepDiff, fmt.Errorf(
			"%w: %d バイト（上限 %d バイト）: base と head の範囲を狭めて再実行してください",
			review.ErrDiffTooLarge, len(diff), p.maxDiffBytes))
	}

	prompt, err := p.deps.Prompts.Generate(req.Mode, diff)
	if err != nil {
		return review.Report{}, review.WrapStep(review.StepPrompt, err)
	}

	p.logger.InfoContext(ctx, "AIレビューを実行します", "mode", req.Mode, "model", req.Model, "diff_bytes", len(diff))

	return p.review(ctx, source, req, prompt)
}

// review は、Deps に設定されている方のレビュアーで AI レビューを実行します。
func (p *Pipeline) review(ctx context.Context, source review.DiffSource, req review.Request, prompt string) (review.Report, error) {
	if p.deps.WorkspaceReviewer == nil {
		report, err := p.deps.Reviewer.Review(ctx, req.Model, prompt)
		if err != nil {
			return review.Report{}, review.WrapStep(review.StepReview, err)
		}
		return report, nil
	}

	// Head を明示的にチェックアウトします（理由は review.Workspace のドキュメントを参照）。
	// これを省くと、レビュアーは前回の実行が残した別参照の内容を読んでしまいます。
	provider, ok := source.(review.WorkspaceProvider)
	if !ok {
		return review.Report{}, review.WrapStep(review.StepCheckout,
			fmt.Errorf("差分取得元 (%T) が作業ディレクトリを提供できません: review.WorkspaceProvider を満たす実装が必要です", source))
	}

	dir, err := provider.CheckoutHead(ctx, req.Head)
	if err != nil {
		return review.Report{}, review.WrapStep(review.StepCheckout, err)
	}

	report, err := p.deps.WorkspaceReviewer.ReviewWorkspace(ctx, req.Model, prompt, review.Workspace{
		Dir:  dir,
		Base: req.Base,
		Head: req.Head,
	})
	if err != nil {
		return review.Report{}, review.WrapStep(review.StepReview, err)
	}
	return report, nil
}

// close は DiffSource を解放します。
//
// 呼び出し元の締切を外して渡すのは、レビューがタイムアウトで打ち切られた直後に
// 後始末まで道連れで失敗すると、ローカルの作業ディレクトリが汚れたまま残るためです。
func (p *Pipeline) close(ctx context.Context, source review.DiffSource) {
	closeCtx, cancel := p.detach(ctx)
	defer cancel()

	if err := source.Close(closeCtx); err != nil {
		p.logger.WarnContext(ctx, "差分取得元の解放に失敗しました", "error", err)
	}
}

// detach は、呼び出し元の締切と cancel を外し、代わりに publishTimeout を与えた
// context を返します。
//
// ★ 公開・通知・後始末がここを通ります。呼び出し元の context をそのまま使うと、
// レビューが締切で打ち切られた直後は既に期限切れなので、**失敗を報告する通知や
// 作業ディレクトリの後始末まで道連れで失敗します。** いちばん報告が必要な場面でだけ
// 届かない、という裏返った挙動になります。
//
// 呼び出しごとに新しく取ります。使い回すと、公開に時間がかかったぶんだけ通知の
// 持ち時間が削られ、公開が上限まで粘って失敗した場合は通知が必ず不達になります。
func (p *Pipeline) detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), p.publishTimeout)
}

// fail は、失敗を通知したうえで失敗の Result を返します。
//
// ★ 受け取るのは **呼び出し元の context** です。切り離しは notify が自分で行うので、
// 切り離し済みのものを渡さないでください（理由は detach のコメント）。
func (p *Pipeline) fail(
	ctx context.Context,
	req review.Request,
	report *review.Report,
	err error,
	elapsed time.Duration,
) (review.Result, *review.Report, error) {
	result := review.Failed(req, elapsed, err)

	p.logger.ErrorContext(ctx, "レビューパイプラインが失敗しました",
		"job_id", req.JobID, "repo_url", req.RepoURL, "step", review.StepOf(err), "error", err)
	p.notify(ctx, review.Notification{Request: req, Result: result, Report: report, Err: err})

	return result, report, err
}

// notify は、締切を切り離した context で通知します。
func (p *Pipeline) notify(ctx context.Context, n review.Notification) {
	notifyCtx, cancel := p.detach(ctx)
	defer cancel()

	p.notifyOn(notifyCtx, n)
}

// notifyOn は、既に切り離し済みの context で通知します。
//
// 通知の失敗は握り潰します（成果物は既に保存済みで、不達を理由に結果を失敗へ倒すと
// 再実行の判断を誤らせるため）。
func (p *Pipeline) notifyOn(ctx context.Context, n review.Notification) {
	if err := p.deps.Notifier.Notify(ctx, n); err != nil {
		p.logger.WarnContext(ctx, "通知に失敗しました", "status", n.Result.Status, "error", err)
	}
}
