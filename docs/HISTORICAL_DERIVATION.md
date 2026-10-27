# 歷史推導契約

Product 開發版本：unreleased；schema **62**，role policy **v12**。這是程式契約，並非部署或真實證物允入紀錄。

## 用途與既有能力

新的判斷 J 可以不存在於任何原文。它可以是在歸納中提出的假說，透過父項 → J 的 `derived_from`
追溯完整必要 AND 父項、原始來源、方法、producer、trace，以及提案中的範圍、不確定性與限制。
父項 quote 是前提原文，不是 J 的原文；不需要捏造一份 J 文件或先建立程式碼／`implements`。
完整依賴不證明推導正確、因果、來源真實、樣本代表性或蒐集完整。

重用既有 proposal、`derived_claim`、來源重建、PostgreSQL 交易與 exact replay。
新增的是獨立歷史允入、用途綁定的精確審查及永久 current-use 隔離。

| 必要祖先／來源條件 | 歷史允入行為 |
| --- | --- |
| 已 admitted source／derived 父項及完整必要來源可重建 | 可進入精確審查；可包含歷史衍生祖先 |
| 未 current、withdrawn、superseded、contradicted、未額外評估 | 如實顯示，不單憑狀態禁止歷史分析，也不解除原治理效果 |
| 已知失敗或不確定的來源評估 | 保留顯示；不冒充 pass；機械來源綁定仍須核對 |
| 缺件、原文／版本／hash／authority 不符、循環或超界 | 拒絕新增；不能用截斷的材料冒充完整依據 |
| subject、request、principal、decision 或 reason 不符 | 拒絕；變更後需新的精確審查 |

人仍須判斷父項是否支持 J，並可選 reject 或 audit_only。歷史用途不免除審查。

## 原生與 MCP 入口

獨立 `historical-derivation-reviewer` 僅提供：

- `get_historical_derivation_review`：重建完整 native display 與 subject，無寫入。
- `apply_historical_derivation_decision`：精確 `approved`／`reject`／`audit_only` 決策。

Native API 是 `LoadHistoricalDerivationReview`、`ApplyHistoricalDerivationDecision`、`ReadHistoricalDerivationHistory`。
Review request：

```json
{
  "proposal_occurrence_id": "occ:<exact-id>",
  "derivation": {
    "parent_node_ids": ["canon-node:<exact-id>"],
    "method": "歸納方法、範圍與限制",
    "producer": "實際提案產生者",
    "trace_ref": "可追溯且不含秘密的產生紀錄"
  }
}
```

直接父項及每層衍生祖先為 1–32 個唯一排序 ID；完整 ancestry 仍受既有 64 nodes、depth 8 等界限限制。
Product implements 的 32 parents／128 source refs 界限保留，但 historical endpoint 不得取得 implements 批准。
完整歷史 display（含所有來源）上限 1 MiB。不能省略父項或捏造中介結論來繞過界限。

決策另附 `request_id`、原 `review`、`expected_subject`、`decision`、`decision_reason`。
reviewer 由 launcher 綁定。Caller 不能提供 `use_policy`、`use_class` 或 `reviewer_id`，空值也不例外。
歷史與 current 批准不能互用；current 拒絕不自動授權改走歷史入口。

`historical-derivation-review/v1` 使用獨立 subject prefix；terminal receipt 是
`historical-derivation-admission/v1`。它們不是 current-action policy。
`basis.use_review` 僅顯示 current 狀態；Product store identity 位於 `basis.use_review.store_id`。

新 review 也重建每個衍生祖先自身的提案來源，因為該 carrier 可能不同於 AND source leaves。
`ancestor_sources` 的 raw／rendered bytes 與來源綁定納入同一 subject 與 byte 上限；它不增加 AND 父項。
已保存的 display／receipt 不重寫；exact replay 先驗證原 authority，不用今天的新材料重判舊批准。

## Manual 首次擷取前置能力

Product schema 62 加入 `source_manual_review_profiles` 原生持久化。
新的合格 manual source 必須在 `submit_text_source` 的首次 capture 同時提供：

```json
{
  "contract_version": "manual-source-review-profile/v1",
  "title": "提交材料的真實標題",
  "location": "manual:可追溯的提交位置",
  "coverage": "submitted_text",
  "limitations": []
}
```

以上物件放在 optional `manual_review_profile` 欄位。`coverage` 也可為 `exact_excerpt`，此時至少列一項限制。
Title／location 各限 1000／2000 UTF-8 bytes；limitations 最多 16 項、每項 1000 bytes；完整 profile 最多 24 KiB。
這些宣告不認證外部 provider，也不批准 claim。外部來源仍使用原 external envelope，不能改名 manual 繞過來源要求。

DB 將 profile 綁定到首次 source capture 交易、snapshot、intake request 與 hash；之後不可增補、替換或刪除。
未附 profile 的舊來源保持未合格狀態；不能把 `origin_metadata` 當作資格、重送時省略 profile，或事後補造 metadata。
使用 source-only capture → extractor submission → source review；combined `submit_manual_evidence` 不接受此資格。
未選用 profile 的舊 source 身分、請求 hash、review bytes 保持原契約。

## 保存與 current-use 隔離

`canonical_derivations.use_class` 不可變：`ordinary` 或 `historical_only`。ordinary 不表示 current。
歷史 node、parents／edges、decision、materialization authority、獨立 review／receipt 在同一交易提交。
DB deferred guards 核對相互綁定；reject／audit_only 終局保存 decision／receipt，不建立 canonical node。

Live projection 為 `evidence-use-projection/v2`，digest 包含用途。兩個 current policies 都以
`historical_derivation` 拒絕歷史節點，並沿必要 AND 依賴傳遞。父項日後 current、restore 或取得正面評估不會 promotion。
未知或錯配分類不能取得 clearance；沒有 promotion API。限制放在使用判斷，不假裝 withdrawal，以免解除其他節點的 contradiction blocker。

Query `get_evidence_governance_history` 使用 `kind=historical_derivation`、`target_id=原 occurrence` 讀回原 display／receipt。
含歷史分類的 canonical artifact 使用 `canonical-evidence-graph/v2`；ordinary v1 artifact、ID 與保存的 scoped review 不重寫。
保存的審查與任何讀取切面都不是日後行動的執行許可。

## 交易與升級

新 derived 寫入的取鎖順序為 proposal → derivation advisory → governance head；同交易重查來源、完整祖先與 subject。
相關治理變動可能需要重審，但不增加無關全域 revision 的失效條件。完全相同的 terminal retry 回原收據，不新增 authority。
Product fresh-v2 supersession 的歷史 replay 例外保持，不加回 fresh governance fence。

首次啟用歷史 writer 必須受控切換：停止舊 reader／writer／worker 與 supervisor 自動重啟、排空舊 sessions 並防止重連；
再套用 migrations 61–62、升級原角色到 policy v12、用新 binaries 驗證 readiness 與 current 防線，最後才啟用獨立歷史 reviewer。
Migration readiness 不會自動終止已運行的舊程序；此功能沒有 live-upgrade framework。
Migrations 1–60 bytes 保留。Down 依相反順序執行；存在 manual profiles 或歷史 receipts／分類時拒絕會丟失 authority 的 rollback。

參見[安裝與升級](../INSTALL.md#upgrade)、[證物與關聯範例](EVIDENCE_RELATION_EXAMPLES.md)、[current-use 說明](CURRENT_USE_GUIDANCE.md)。
