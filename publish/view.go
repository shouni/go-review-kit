package publish

import (
	"time"

	"github.com/shouni/go-utils/jst"

	"github.com/shouni/go-review-kit/review"
)

// reportView は、テンプレートへ渡す JSON の形です。
//
// レポート本体に、どのリポジトリのどの範囲をいつ見たのかを添えます。旧実装は
// map[string]any へ再パースしてトップレベルにキーを差し込んでいましたが、
// キー名がテンプレートと文字列でしか結び付いていませんでした。ここでは構造体に
// 型として持たせ、review.Report を埋め込むことで本体のキーはそのまま出力されます。
type reportView struct {
	review.Report
	RepoURL    string `json:"repo_url"`
	Base       string `json:"base_branch"`
	Head       string `json:"feature_branch"`
	ReviewedAt string `json:"reviewed_at"`
}

func newReportView(req review.Request, report review.Report, reviewedAt time.Time) reportView {
	return reportView{
		Report:     report,
		RepoURL:    req.RepoURL,
		Base:       req.Base,
		Head:       req.Head,
		ReviewedAt: reviewedAt.Format(jst.LayoutTimestamp),
	}
}
