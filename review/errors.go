package review

import (
	"errors"
	"fmt"
)

// パイプラインが返す番兵エラーです。呼び出し側は errors.Is で分岐できます。
//
// 旧実装は「エラー」と「スキップ」を構造体のフィールド（Error error / IsSkipped bool）で
// 運んでいましたが、状態が増えるたびにフィールドが増え、errors.Is も効きませんでした。
// ここでは通常の error として返し、種類は番兵エラーで表現します。
var (
	// ErrInvalidRequest は、Request の必須項目が欠けていることを示します。
	ErrInvalidRequest = errors.New("レビューリクエストが不正です")
	// ErrEmptyDiff は、base と head の間に差分が無くレビューをスキップしたことを示します。
	// これは失敗ではないため、パイプラインは StatusSkipped の Result と共に nil を返します。
	ErrEmptyDiff = errors.New("差分が存在しません")
	// ErrRefNotFound は、指定された参照がリポジトリに存在しないことを示します。
	ErrRefNotFound = errors.New("参照が見つかりません")
	// ErrEmptyResponse は、AI がエラーを返さずに空の結果を返したことを示します。
	ErrEmptyResponse = errors.New("AIが空の応答を返しました")
	// ErrInvalidReport は、AI の出力をレポートとして解釈できなかったことを示します。
	ErrInvalidReport = errors.New("レビュー結果が不正です")
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
// 旧実装が Outcome.StepName という別フィールドで運んでいた情報を、エラー自身に持たせます。
// 工程名はエラーと必ず一組で意味を持つため、両者が離れていると取り違えが起きるためです。
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
	var se *StepError
	if errors.As(err, &se) {
		return se.Step
	}
	return ""
}
