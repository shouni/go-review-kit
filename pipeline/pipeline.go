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
	Sources   review.DiffSourceFactory
	Prompts   review.PromptGenerator
	Reviewer  review.Reviewer
	Publisher review.Publisher
	Notifier  review.Notifier
}

func (d Deps) validate() error {
	var missing []string
	for _, dep := range []struct {
		name  string
		value any
	}{
		{"Sources", d.Sources},
		{"Prompts", d.Prompts},
		{"Reviewer", d.Reviewer},
		{"Publisher", d.Publisher},
		{"Notifier", d.Notifier},
	} {
		if dep.value == nil {
			missing = append(missing, dep.name)
		}
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
// 差分が無かった場合は StatusSkipped の Result と nil を返します（失敗ではないため）。
// 呼び出し側が「実行はしたが成果物は無い」を判別できるよう、Result.Published で確認できます。
//
// 公開と通知は呼び出し元の締切から切り離して実行します。レビューは重く、呼び出し元が
// タイムアウト付きの context を渡すことがあります。そのまま使うと、レビューが締切で
// 打ち切られた直後は context が既に期限切れなので、失敗を報告する通知まで道連れで
// 失敗します。いちばん報告が必要な場面でだけ通知が届かない、という裏返った挙動になるため、
// context.WithoutCancel で締切を外し、代わりに publishTimeout を与えます。
func (p *Pipeline) Run(ctx context.Context, req review.Request) (review.Result, error) {
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
		return result, nil

	case err != nil:
		return p.fail(ctx, req, nil, err, elapsed)
	}

	publishCtx, cancel := p.detach(ctx)
	defer cancel()

	if err := p.deps.Publisher.Publish(publishCtx, req, report); err != nil {
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

	return result, nil
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

	report, err := p.deps.Reviewer.Review(ctx, req.Model, prompt)
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
) (review.Result, error) {
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
) (review.Result, error) {
	result := review.Failed(req, elapsed, err)

	p.logger.ErrorContext(ctx, "レビューパイプラインが失敗しました",
		"job_id", req.JobID, "repo_url", req.RepoURL, "step", review.StepOf(err), "error", err)
	p.notifyOn(ctx, review.Notification{Request: req, Result: result, Report: report, Err: err})

	return result, err
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
