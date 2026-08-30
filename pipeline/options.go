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
// 公開・通知・後始末には掛かりません。呼び出し側が Run へ渡す context に自分で
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

// WithMaxDiffBytes は、AI へ送る差分の上限をバイト数で設定します。
// 既定は 0（無制限）で、0 以下の値は無視されます。
//
// 超えた場合、レビューは実行されず review.ErrDiffTooLarge を包んだエラーで失敗します
// （工程は review.StepDiff）。失敗として扱うので、Notifier へは他の失敗と同じ経路で
// 届きます。
//
// トークンではなくバイトで測ります。バイト数は差分を受け取った時点で手元にあり、
// トークン数の算出はモデル提供元への往復を要します。避けたいコストを払って避けるか
// どうかを決めることになるため、精度より安さを取っています。目安として、UTF-8 の
// 日本語は 1 MiB でおよそ 30〜35 万文字です。
//
// 切り詰めではなく失敗にしてください、というのがこの上限の趣旨です。上限まで
// truncate して送ると、モデルは差分の一部だけを見たまま *成功として* レポートを返すため、
// 質の落ちたレビューが成果物として残ります。落ちるより気付きにくい形です。
func WithMaxDiffBytes(n int) Option {
	return func(p *Pipeline) {
		if n > 0 {
			p.maxDiffBytes = n
		}
	}
}
