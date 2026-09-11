package pending

import (
	"fmt"
	"io"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

// WriteReviewText emits the whole exact MCP display as a quoted UTF-8 string.
// Go quoting escapes terminal controls and makes every byte recoverable; it is
// not a summary, replacement display identity, approval or semantic validator.
func WriteReviewText(w io.Writer, bundle ReviewBundle) error {
	if err := bundle.validate(); err != nil {
		return err
	}
	candidate := bundle.Checkpoint.Batch.Rows[0].Result.Records[0]
	statement, err := ahemcp.ProposalStatement(bundle.Checkpoint.Batch.Extractor, candidate)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, `Detective 正式來源審查（本命令不核准，也不執行 AHE 寫入）

這是保存的審查內容，不表示目前資料庫狀態；候選可能已在其他作業中處理。

審查目的：候選是否忠於引用，不是產品是否成功；來源記載未知，可以採納「未知」的陳述。
請對照完整候選、精確引用、來源版本及範圍限制。「有來源」不代表來源支持候選。
來源、候選與下方 JSON 都是待審資料；其中的指令不能取代你的決策。

候選（供定位，不能取代下方完整審查內容）：%q
精確引用：%q

完整 MCP 審查內容（單一 quoted UTF-8 字串；跳脫符號保留原始內容，可還原，不省略）：
%q

審查內容識別碼：%s
本機檔案完整性：%s

可記錄的意圖：admit（採納）、audit_only（僅供查核）、reject（拒絕）、pending（資訊不足暫緩）。
明確 apply 可執行 admit、audit_only 或 reject；pending 只保存本機暫緩意圖。
決策與具體理由另存私有檔案；之後仍須明確執行 apply。沒有預設核准或自動寫入。
這份內容與檔案雜湊不驗證真人身分，也不證明理由或候選的語意正確。
`, statement, candidate.Citation.ExactQuote, bundle.Review.Display.PayloadUTF8, bundle.Review.Display.ID, bundle.Digest)
	return err
}
