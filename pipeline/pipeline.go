// Package pipeline は、レビュー要求を受け取ってから成果物を公開・通知するまでの
// 一連の流れを組み立てます。
//
// 旧実装は workflow（全体制御）と runner（各工程）の 2 階層に分かれていましたが、
// workflow 側は runner へ委譲するだけで、しかも組み立てた結果を返さずに捨てていました。
// ここでは 1 階層にまとめ、Run が Result と error の両方を返します。
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	Prompts review.PromptGenerator
	// Reviewer は、差分だけで完結する単発のレビュアーです。
	// WorkspaceReviewer とはどちらか一方だけを設定します。
	Reviewer review.Reviewer
	// WorkspaceReviewer は、作業ディレクトリを参照するレビュアーです。
	// 設定する場合、Sources が返す DiffSource は review.WorkspaceProvider を満たす
	// 必要があります（git パッケージの 2 実装はどちらも満たします）。
	// レビュアーの使い分けはリクエスト単位ではなくパイプライン単位です。モードごとに
	// 使い分けたい呼び出し側は、アダプターを共有した Pipeline を 2 つ組んでください。
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
		if dep.value == nil {
			missing = append(missing, dep.name)
		}
	}

	// レビュアーは「どちらか一方だけ」です。両方設定されていると、どちらで実行される
	// つもりだったのかが Deps から読み取れず、静かに片方を無視すると設定ミスに気付けません。
	switch {
	case d.Reviewer == nil && d.WorkspaceReviewer == nil:
		missing = append(missing, "Reviewer または WorkspaceReviewer")
	case d.Reviewer != nil && d.WorkspaceReviewer != nil:
		return fmt.Errorf("pipeline: Reviewer と WorkspaceReviewer は同時に設定できません（どちらか一方にしてください）")
	}

	if len(missing) > 0 {
		return fmt.Errorf("pipeline: 依存が未設定です: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Pipeline は、レビューの実行から公開・通知までを担います。
type Pipeline struct {
	deps           Deps
	logger         *slog.Logger
	publishTimeout time.Duration
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
		opt(p)
	}
	return p, nil
}

// Run は、レビュー要求の全工程を実行します。
//
// 戻り値の Report は、AI がレビューを返した場合のみ非 nil です。差分が無かった場合や、
// レビューへ到達する前に失敗した場合は nil になります。保存に失敗した場合は Report が
// 非 nil のままエラーも返るので、「レビューはできたが残せなかった」を呼び出し側で
// 区別できます。
//
// Report を返すのは、レビューの中身（題目・判定・指摘）を必要とする処理を通知フックへ
// 相乗りさせずに済ませるためです。ジョブの状態を記録する呼び出し側が、通知を名乗る
// アダプターを書かなくても Run の戻り値から組み立てられます。
//
// 差分が無かった場合は StatusSkipped の Result と nil を返します（失敗ではないため）。
// 呼び出し側が「実行はしたが成果物は無い」を判別できるよう、Result.Published で確認できます。
//
// 公開と通知は呼び出し元の締切から切り離して実行します。レビューは重く、呼び出し元が
// タイムアウト付きの context を渡すことがあります。そのまま使うと、レビューが締切で
// 打ち切られた直後は context が既に期限切れなので、失敗を報告する通知まで道連れで
// 失敗します。いちばん報告が必要な場面でだけ通知が届かない、という裏返った挙動になるため、
// context.WithoutCancel で締切を外し、代わりに publishTimeout を与えます。
func (p *Pipeline) Run(ctx context.Context, req review.Request) (review.Result, *review.Report, error) {
	start := time.Now()

	if err := req.Validate(); err != nil {
		return p.fail(ctx, req, nil, review.WrapStep(review.StepValidate, err), time.Since(start))
	}

	report, err := p.produce(ctx, req)
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
		return p.failWith(publishCtx, req, &report, review.WrapStep(review.StepPublish, err), elapsed)
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
	p.notifyOn(publishCtx, review.Notification{Request: req, Result: result, Report: &report})

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
func (p *Pipeline) detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), p.publishTimeout)
}

// fail は、締切を切り離した context で通知したうえで失敗の Result を返します。
func (p *Pipeline) fail(
	ctx context.Context,
	req review.Request,
	report *review.Report,
	err error,
	elapsed time.Duration,
) (review.Result, *review.Report, error) {
	notifyCtx, cancel := p.detach(ctx)
	defer cancel()

	return p.failWith(notifyCtx, req, report, err, elapsed)
}

// failWith は、既に切り離し済みの context を使う fail です。
func (p *Pipeline) failWith(
	ctx context.Context,
	req review.Request,
	report *review.Report,
	err error,
	elapsed time.Duration,
) (review.Result, *review.Report, error) {
	result := review.Failed(req, elapsed, err)

	p.logger.ErrorContext(ctx, "レビューパイプラインが失敗しました",
		"job_id", req.JobID, "repo_url", req.RepoURL, "step", review.StepOf(err), "error", err)
	p.notifyOn(ctx, review.Notification{Request: req, Result: result, Report: report, Err: err})

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
