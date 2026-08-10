// Package publish は、レビュー結果を HTML へ変換してリモートストレージへ公開する
// review.Publisher の実装を提供します。
package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/shouni/go-remote-io/remoteio"
	"github.com/shouni/go-utils/jst"

	"github.com/shouni/go-review-kit/review"
)

const contentTypeHTML = "text/html; charset=utf-8"

// Converter は、レビュー結果の JSON を完全な HTML ドキュメントへ変換する契約です。
type Converter interface {
	// Run は JSON を受け取り、HTML を読み出せる io.Reader を返します。
	Run(content []byte) (io.Reader, error)
}

// Publisher は、レビュー結果を HTML へ変換してリモートストレージへ書き込みます。
type Publisher struct {
	writer    remoteio.Writer
	converter Converter
	now       func() time.Time
	logger    *slog.Logger
}

// 実装がポートを満たすことをコンパイル時に確認します。
var _ review.Publisher = (*Publisher)(nil)

// New は Publisher を生成します。
func New(writer remoteio.Writer, converter Converter, opts ...Option) (*Publisher, error) {
	if writer == nil {
		return nil, fmt.Errorf("publish: writer が nil です")
	}
	if converter == nil {
		return nil, fmt.Errorf("publish: converter が nil です")
	}

	p := &Publisher{
		writer:    writer,
		converter: converter,
		now:       jst.Now,
		logger:    slog.Default(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

// Publish は、レビュー結果を HTML へ変換して Request.StorageURI へ書き込みます。
func (p *Publisher) Publish(ctx context.Context, req review.Request, report review.Report) error {
	content, err := p.render(req, report)
	if err != nil {
		return err
	}

	p.logger.InfoContext(ctx, "レビュー結果を公開します", "uri", req.StorageURI)

	if err := p.writer.Write(ctx, req.StorageURI, content, remoteio.WithContentType(contentTypeHTML)); err != nil {
		return fmt.Errorf("ストレージへの書き込みに失敗しました (%s): %w", req.StorageURI, err)
	}
	return nil
}

// render は、レポートに実行時のメタ情報を添えて HTML へ変換します。
func (p *Publisher) render(req review.Request, report review.Report) (io.Reader, error) {
	data, err := json.Marshal(newReportView(req, report, p.now()))
	if err != nil {
		return nil, fmt.Errorf("レビュー結果の組み立てに失敗しました: %w", err)
	}

	content, err := p.converter.Run(data)
	if err != nil {
		return nil, fmt.Errorf("HTMLへの変換に失敗しました: %w", err)
	}
	return content, nil
}
