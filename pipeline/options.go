package pipeline

import (
	"log/slog"
	"time"
)

// Option は Pipeline の任意設定です。
type Option func(*Pipeline)

// WithLogger は、パイプラインが使うロガーを差し替えます。
//
// 既定は slog.Default() です。パッケージ変数のロガーを直接呼ぶと、出力先も属性も
// 呼び出し側から制御できなくなります。
func WithLogger(logger *slog.Logger) Option {
	return func(p *Pipeline) {
		if logger != nil {
			p.logger = logger
		}
	}
}

// WithPublishTimeout は、公開・通知・後始末に与える上限を変更します。
// 0 以下の値は無視されます。
func WithPublishTimeout(d time.Duration) Option {
	return func(p *Pipeline) {
		if d > 0 {
			p.publishTimeout = d
		}
	}
}

// WithRunTimeout は、差分取得から AI レビューまでに与える上限を設定します。
// 既定は無制限で、0 以下の値は無視されます。
//
// **公開・通知・後始末には掛かりません。** 呼び出し側が Run へ渡す context に自分で
// 締切を被せると切り離しの外側に掛かるので、代わりにこちらを使ってください
// （理由は detach のコメント）。
//
// 上限を持つこと自体の意味は、外側（ジョブキューの応答待ちなど）に打ち切られる前に
// 自分から諦めて、失敗を記録・通知してから終わることです。外側に先を越されると
// プロセスごと落ちて何も残りません。
func WithRunTimeout(d time.Duration) Option {
	return func(p *Pipeline) {
		if d > 0 {
			p.runTimeout = d
		}
	}
}
