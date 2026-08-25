package review

import "context"

// DiffSource は、レビュー対象の差分を取り出せる状態になったリポジトリを表します。
//
// clone・fetch・参照解決といった手順の順序は Git 実装の都合であってワークフローの関心では
// ないため、ここには現れません。準備は DiffSourceFactory が、参照の解決は Diff の内側が
// 引き受けるので、パイプラインは順序を知らずに済みます。
type DiffSource interface {
	// Diff は base と head の間の差分を返します。
	// 参照が存在しない場合は ErrRefNotFound を包んだエラーを返します。
	Diff(ctx context.Context, base, head string) (string, error)
	// CheckoutHead は、head を作業ツリーへチェックアウトし、そのディレクトリを返します。
	//
	// レビュアーが差分の外を調べるために必ず要るので、能力を分けずここへ置きます
	// （型アサーションで確かめる形だと、満たさない実装は実行時にしか分かりません）。
	CheckoutHead(ctx context.Context, head string) (dir string, err error)
	// Close は Diff のために確保したリソースを解放します。
	//
	// io.Closer ではなく context を受け取るのは、ローカル git コマンドを実行する実装が
	// あるためです（後始末そのものが取り消し可能な外部プロセス呼び出しになります）。
	Close(ctx context.Context) error
}

// DiffSourceFactory は、Request ごとに DiffSource を開きます。
//
// クローンやフェッチといった準備は Open の内側で完了させ、返した時点で Diff を呼べる
// 状態にします。
type DiffSourceFactory interface {
	Open(ctx context.Context, req Request) (DiffSource, error)
}

// PromptGenerator は、レビューモードと差分から AI へ送るプロンプトを組み立てます。
type PromptGenerator interface {
	Generate(mode, diff string) (string, error)
}

// Workspace は、Head をチェックアウト済みのローカル作業ディレクトリです。
//
// DiffSource.Diff は作業ツリーを使わずに差分を作るため、作業ツリーの内容が Head の状態で
// ある保証はありません。WorkspaceReviewer へ渡す前に、パイプラインが
// DiffSource.CheckoutHead で Head を明示的にチェックアウトします。
type Workspace struct {
	// Dir は、Head をチェックアウトした作業ディレクトリのパスです。
	Dir string
	// Base は、比較元の参照です。
	Base string
	// Head は、比較対象の参照です。Dir の内容はこの参照の状態です。
	Head string
}

// WorkspaceReviewer は、プロンプトと作業ディレクトリからレビュー結果を返します。
//
// 差分の外（変更されなかったファイル、前後の章など）を実装が自分で調べるエージェント型を
// 想定しています。差分そのものはプロンプトに含まれて渡ります。
//
// 戻り値が JSON 文字列ではなく Report なのは、デコードの責務を実装側に一度だけ持たせ、
// 以降の層が生の文字列を再解釈しなくて済むようにするためです。
type WorkspaceReviewer interface {
	Review(ctx context.Context, model, prompt string, ws Workspace) (Report, error)
}

// Publisher は、レビュー結果を成果物として公開します。
//
// 呼ばれるのはレビューが成立したときだけです。差分なし・失敗の場合は公開する内容が
// 存在しないため、パイプラインは Notifier だけを呼びます。
type Publisher interface {
	Publish(ctx context.Context, req Request, report Report) error
}

// Notification は、Notifier へ渡す 1 回の実行の顛末です。
type Notification struct {
	// Request は、実行のもとになった入力です。
	Request Request
	// Result は、最終状態・保存先・所要時間です。
	Result Result
	// Report は、レビューが成立した場合のみ非 nil です。
	// 指摘件数を通知に含めるなど、内容に応じた文面を組み立てる用途を想定しています。
	Report *Report
	// Err は、失敗した場合のみ非 nil です。
	// review.StepOf で工程名を取り出せます。
	Err error
}

// Notifier は、実行の顛末を外部（チャットなど）へ通知します。
//
// 通知の失敗はパイプライン全体の失敗として扱いません（成果物は既に保存済みであり、
// 通知の不達で結果を失敗に倒すと再実行の判断を誤らせるためです）。
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}
