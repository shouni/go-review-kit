package review

import (
	"errors"
	"fmt"
)

// パイプラインが返す番兵エラーです。呼び出し側は errors.Is で分岐できます。
//
// 状態を構造体のフィールド（Error / IsSkipped のような形）で運ばないのは、状態が増える
// たびにフィールドが増えるうえ errors.Is が効かないためです。通常の error として返し、
// 種類は番兵エラーで表します。
var (
	// ErrInvalidRequest は、Request の必須項目が欠けていることを示します。
	ErrInvalidRequest = errors.New("review: request is invalid")
	// ErrEmptyDiff は、base と head の間に差分が無くレビューをスキップしたことを示します。
	// これは失敗ではないため、パイプラインは StatusSkipped の Result と共に nil を返します。
	ErrEmptyDiff = errors.New("review: diff is empty")
	// ErrDiffTooLarge は、差分が上限（pipeline.WithMaxDiffBytes）を超えたため、
	// AI へ送らずに中断したことを示します。
	//
	// 差分なしと違い、これは失敗です。範囲を絞れば通る入力なので利用者に手を
	// 打ってもらう必要があり、スキップとして扱うと「レビューはしたが指摘が無かった」
	// と見分けが付きません。
	//
	// 送る前に落とすための番兵です。大きすぎる差分はレビューを実行しても、出力の
	// 途中切れ・締切超過・入力上限のいずれかで結局失敗しますが、そのどれもが
	// モデルを呼び終えたあとにしか分かりません。いちばんコストを払ったあとで
	// 落ちる順序になります。
	ErrDiffTooLarge = errors.New("review: diff is too large")
	// ErrRefNotFound は、指定された参照がリポジトリに存在しないことを示します。
	//
	// 「読もうとして失敗した」場合はこれではありません。リポジトリの破損やディスク障害まで
	// 畳み込むと、利用者へ「そのブランチはありません」と伝えてしまうためです。
	ErrRefNotFound = errors.New("review: ref not found")
	// ErrUnsupportedRepoURL は、リポジトリURLの形式を扱えないことを示します。
	//
	// 入力の誤りであって障害ではないので、再試行しても直りません。利用者へ形式を
	// 直してもらう案内が要る一方、ネットワーク起因の失敗は再試行で直るため、
	// 呼び出し側が両者を分けられるように番兵にしています。
	ErrUnsupportedRepoURL = errors.New("review: unsupported repository URL")
	// ErrEmptyResponse は、AI がエラーを返さずに空の結果を返したことを示します。
	ErrEmptyResponse = errors.New("review: model returned an empty response")
	// ErrInvalidReport は、AI の出力をレポートとして解釈できなかったことを示します。
	ErrInvalidReport = errors.New("review: report is not valid")
)

// パイプラインの工程名です。StepError.Step に入り、通知の文面などに利用できます。
const (
	StepValidate = "リクエスト検証"
	StepPrepare  = "リポジトリの準備"
	StepDiff     = "コード差分取得"
	StepPrompt   = "プロンプト生成"
	StepCheckout = "Headチェックアウト"
	StepReview   = "AIレビュー"
	StepPublish  = "結果公開"
)

// StepError は、どの工程で失敗したかを保持するエラーです。
//
// 工程名を別フィールドではなくエラー自身に持たせます。工程名はエラーと一組でしか
// 意味を持たないため、両者が離れていると取り違えが起きます。
type StepError struct {
	Step string
	Err  error
}

// Error はエラーメッセージを返します。
func (e *StepError) Error() string {
	return fmt.Sprintf("%s に失敗しました: %v", e.Step, e.Err)
}

// Unwrap は元のエラーを返し、errors.Is / errors.As を通します。
func (e *StepError) Unwrap() error { return e.Err }

// WrapStep は err に工程名を付与します。err が nil の場合は nil を返します。
func WrapStep(step string, err error) error {
	if err == nil {
		return nil
	}
	return &StepError{Step: step, Err: err}
}

// StepOf は err に含まれる工程名を返します。工程名が付与されていない場合は空文字を返します。
func StepOf(err error) string {
	if se, ok := errors.AsType[*StepError](err); ok {
		return se.Step
	}
	return ""
}
