package publish

import (
	"log/slog"
	"time"
)

// Option は Publisher の任意設定です。
type Option func(*Publisher)

// WithLogger は、Publisher が使うロガーを差し替えます。
func WithLogger(logger *slog.Logger) Option {
	return func(p *Publisher) {
		if logger != nil {
			p.logger = logger
		}
	}
}

// WithClock は、成果物に埋め込む実行日時の取得方法を差し替えます。
// 既定は日本時間の現在時刻です。テストで出力を固定したい場合に使います。
func WithClock(now func() time.Time) Option {
	return func(p *Publisher) {
		if now != nil {
			p.now = now
		}
	}
}
