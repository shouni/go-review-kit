package pipeline

import (
	"log/slog"
	"time"
)

// Option は Pipeline の任意設定です。
type Option func(*Pipeline)

// WithLogger は、パイプラインが使うロガーを差し替えます。
//
// 旧実装はパッケージ変数の既定ロガー（slog.Info など）を直接呼んでいたため、
// 呼び出し側が出力先や属性を制御できませんでした。
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
