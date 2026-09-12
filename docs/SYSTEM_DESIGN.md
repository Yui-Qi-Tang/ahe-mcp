# AHE 系統設計

本文件集中說明現行資料結構、演算法與理論邊界。安裝與權限設定見
[INSTALL](../INSTALL.md)，產品入口見 [README](../README.md)。
歷史實驗日誌、審查報告與原始量測不屬於產品文件；本文也不是部署或模型品質證明。

## 目錄

- [系統職責](#architecture)
- [資料模型與身分](#data-model)
- [審查與狀態轉移](#review)
- [圖關係與版本](#graph)
- [文字搜尋](#search)
- [Detective 擷取與恢復](#detective)
- [執行權限與完整性](#authority)
- [參考文獻](#references)

<a id="architecture"></a>

## 系統職責

```text
資料源／來源 MCP → Detective → Intake MCP → 來源、擷取紀錄、pending
                                      ↓
                        精確 review subject → 人工決定
                                      ↓
                               Review MCP → PostgreSQL
                                      ↑
Detective／下游 agent ← 唯讀證據包 ← Query MCP
```

- **Detective** 負責資料源連線、原文保存、模型擷取及人工作業介面。來源取得不進入 Core。
- **MCP domain／PostgreSQL** 保存來源身分、提案、審查決定與 canonical graph；模型輸出沒有寫入核准效力。
- **Query** 只取回有範圍、狀態與引用的材料，不替使用者生成事實結論。
- **拓撲核心** 處理節點、邊與結構演算法；關係的證據意義、時間語意與 admission 仍由 AHE 定義。

Detective 與 MCP 共用一份 Go module，但仍透過分離的 stdio 程序互動。
新 Desktop 不直接持有 DB 憑證；舊 `ahe-detective` collector 不是新 Desktop。
來源已收集、提案 pending、canonical 已採納、repository generation 已啟用與下游答案，是五個不同層次。

<a id="data-model"></a>

## 資料模型與身分

### 來源與候選

| 結構 | 保存什麼 | 不代表什麼 |
| --- | --- | --- |
| `source_blobs` | 原始內容與內容雜湊 | 來源是真實或可信的 |
| `source_snapshots` | 來源物件、版本與 metadata | 該版本在來源端仍為最新 |
| `extraction_views` | renderer／版本、呈現本文及雜湊 | 模型摘要可以取代原文 |
| `span_catalog_entries` | view 內精確 byte 範圍、行號、引用與雜湊 | 引用支持任意主張 |
| `extraction_runs`／`extraction_attempts` | 來源、extractor 定義、嘗試狀態及輸出身分 | 模型語意品質已合格 |
| `proposal_batches`／`proposal_occurrences` | 一次輸出的候選集合與各筆主張 | 已取得 admission |
| external source request／receipt | 某次交付與已保存來源的綁定 | 多次交付就是多個獨立來源 |

原始 bytes、呈現 view、精確引文與主張分開保存。清理或呈現轉換要留下 renderer 與來源關係，
不能以模型摘要覆寫原始證據。同一文件的多段引用也不會自然變成互相獨立的佐證。

外部來源使用 provider 物件身分及 revision；同一 revision 的內容或來源 metadata 改變會衝突。
外部 envelope 要求 `content_fidelity=verbatim`；`exact_excerpt`／`truncated_document` 要說明限制，
`full_document` 的 `limitations` 必須為空。這是提交契約，不是 AHE 對 provider 身分或完整性的外部認證。

來源交付 request 與 extraction request 是不同命名空間。相同 extraction request 只允許相同來源、
view 與 extractor 定義；不同輸入須使用新 request。相同輸出可重播，改輸出不得追加另一組候選。
`producer_session_ref` 是稽核註記，不用來替換語意身分。成功的零候選輸出是 abstention。
晚到的失敗只能將 `started` 改為 `failed`，不能覆寫先前已提交的終態。

### Canonical graph

`CanonicalArtifact` 是可傳遞的有界資料表示，包含 nodes、edges、payloads、provenance、temporal、
integrity 與 derivations；不是另一個持久化權威資料庫。

- 節點種類：`raw_evidence`、`source_claim`、`derived_claim`、`candidate`。
- 邊包含 `from`、`to`、relation 與 provenance reference。
- payload 分開保留來源內容／定位、claim、適用範圍與 target anchors；不將標題當成完整證據。
- temporal 記錄與 Supersession currentness 是不同概念；後者是查詢時計算的投影。
- derivation 記錄完整 parent 集合，表達 **AND 依賴**，不是任一 parent 都足夠。

種類存在不等於標準 MCP writer 可以建立該種類。
資料型別見 [canonical.go](../internal/evidencegraph/canonical.go)，來源契約見
[external_source.go](../internal/evidenceingestion/external_source.go)，實際持久化約束見 [migrations](../migrations)。

<a id="review"></a>

## 審查與狀態轉移

| 人工決定 | 保存結果 | Canonical 效果 |
| --- | --- | --- |
| 暫不決定 | 留在 `pending`；不是 writer outcome | 無 |
| `admit` | `admitted` 決定及 exact-review binding | 原子建立或驗證允許重用的 canonical materialization |
| `reject` | `rejected` 終態及審查理由 | 不建立 canonical 節點／邊 |
| `audit_only` | `audit_only` 終態及審查理由 | 不建立 canonical 節點／邊 |

精確審查的綁定鏈為 manifest → submission receipt → proposal basis → review package → displayed subject。
顯示本文也在 binding 內，不只核對 proposal ID。Review getter 只接受 pending；writer 在同一
Repeatable Read 交易鎖定提案、重新建構 subject、核對完整 display，才原子保存決定與相應 materialization。
Intake、Query 與 model 不會因看到一份 review package 就取得核准權。

重播須維持原 outcome、subject、extraction attempt、principal 與 reason。修改理由、引用或 ID
不是恢復程序；不確定的回覆也不代表遠端交易已回滾。終態重試走原 writer request，不要求重新取得 pending review。

普通 source-backed admission 產生 `raw_evidence → source_claim` 的 `supports_claim`。
mutation manifest、node／edge binding 與 first-materializer 身分用來約束重用與精確重播；
相同 ID 的本文或關係不同要拒絕，不能覆寫既有證據。
DB 保存的是被誰、以什麼理由採納的主張，不能證明該人確實看過畫面，也不能證明主張為真。

實作入口： [review contract](../internal/evidenceingestion/reviewable_ingestion.go)、
[display binding](../internal/evidenceingestion/reviewable_ingestion_display.go)、
[admission](../internal/evidenceingestion/reviewable_ingestion_admission.go)、
[disposition](../internal/evidenceingestion/reviewable_ingestion_disposition.go)。

<a id="graph"></a>

## 圖關係與版本

### 結構演算法不等於證據推論

| 關係 | 語意／方向 |
| --- | --- |
| `supports_claim` | 原始證據 → 來源主張 |
| `derived_from` | 此實作為 parent → derived target；完整 parent manifest 才表達 AND 依賴 |
| `contradicts` | 對稱關係；端點排序後以同一 node pair 表示 |
| `supersedes` | 新 replacement → 舊 target |
| `references`／`implements` | 型別化參照／實作關係；不能只靠相似度或任意邊寫入認定 |

Graph adapter 從同一讀取快照取得 AHE 選定的關係與有界範圍，再交給通用拓撲演算法。
路徑只證明該視圖內有結構連通；cycle、SCC 或 contradiction component 不是語意矛盾的自動證明，
更不代表 component 內任兩點都互相矛盾。需要 DAG 的檢查必須指定關係，不可假定整張證據圖無環。
程序內 read-view handle 綁定 principal 與 DB／schema／role；被淘汰或程序重啟後要重開。
有界 ReadView 不能冒充全域完整圖。

### 已實作的內部領域契約

以下寫入路徑存在於 domain／typed registry，但**目前標準 ingestion runtime 不開放這些 writer**。
不可套用 `source-claim-reviewer` 憑證繞過入口，也不可把它們當成 Desktop 按鈕。

- **Derived admission**：最多 64 個已採納 parents、新的 immutable derived target、完整精確的
  `parent → target` 邊；交易內以鎖與 cycle preflight 驗證，不從鬆散二元邊猜測必要前提。
- **Contradiction**：先建立完整提案再核准；node pair 身分為單次使用，包含 `rejected`／`audit_only`
  終態。改 rationale 不會變成新版本，不得複製節點繞過限制。
- **Supersession**：新外部來源 pending proposal、fresh replacement、完整 targets 與 head 條件一起核准；
  不從 provider revision 排序自動建立替代關係。

見 [admission](../internal/evidenceingestion/admission.go)、
[contradiction](../internal/evidenceingestion/canonical_contradiction.go)、
[supersession admission](../internal/evidenceingestion/supersession_admission.go)。

### Supersession：lineage、CAS 與不可變歷史

Lineage 由六欄 basis 決定：`source_system`、`source_namespace`、`object_type`、`object_id`、
`slot_kind`、`slot_id`。前四欄須符合已保存來源，後兩欄是經審閱的穩定語意槽宣告。
內容、provider revision、時間、模型與 session 不參與 lineage 身分；相同內容不等於同一 lineage。

每次替代最多 64 targets，產生 fresh source claim、`new → old` edges、append-only event、
member／target mirrors、decision 與新 head。完整集合要同一交易原子成立；不能部分採納或事後追加 targets。
Bootstrap 只把符合來源物件條件的既有 admitted claim 登記為舊成員，不重建或重審原節點。

所有 lineage 共用一個 global revision／head。Command 帶入讀到的 expected revision 與 head event ID；
鎖定 head 後重新比對，只有符合才從 N 推進到 N+1。這是資料庫內序列化點，不是 provider 時間，
也不是多節點共識協定。request／decision／event hashes 綁定各自完整語意，差異不能當 exact replay。
PostgreSQL 約束與 deferred triggers 檢查 relational mirrors、append-only 與 head progression；
Go domain 重算語意雜湊，兩者不是兩套可各自改寫的 JSON canonicalization。

### Currentness：同一快照的可證明範圍

唯讀 loader 讀取 head、完整 global event chain、所選 lineage members／edges／targets 與同物件 claims，
核對雜湊、連續性、無環及分類完整性，再計算沒有 incoming `supersedes` 的 frontier。
以下為逐節點狀態：有有效 incoming `supersedes` 者維持 `superseded`；
只有其餘 frontier 節點再依 closure 完整性與 frontier 數量判定，不將所有節點一起改為 `current` 或 `unknown`。

| 結果 | 條件 |
| --- | --- |
| `superseded` | 存在有效 incoming `supersedes` |
| `current` | closure 完整，且只有一個 frontier |
| `ambiguous` | closure 完整，但有多個 frontier |
| `unknown` | 同物件尚有未分類 claim，無法證明 frontier 完整性 |

資料結構損壞、事件缺漏、hash／mirror 不符、cycle 或超出上限直接報錯，不偽裝成 `unknown`。
現行硬上限：10,000 global events、10,000 lineage members、100,000 lineage edges、50,000 object claims。
Witness 綁定當次快照，不是簽署憑證；`current` 不代表來源端最新或現實為真。
Query 只接受 lineage key，不接受 caller 宣告 winner、members 或 completeness。

見 [lineage / event](../internal/evidencesupersession/supersession.go)、
[closure / currentness](../internal/evidencesupersession/closure.go)。
Migration 42 的 pair-v1 退場不猜測舊資料：既有 pair authority 非空會停止，不能用舊邊偽造 fresh-v2 歷史。

<a id="search"></a>

## 文字搜尋

搜尋分成「哪些資料可入選」及「入選資料如何排序」。兩者都不判斷主張真假。
`search_evidence_records` 保持 exact lexical lookup；下表是 grounded brief 可選模式，不能混用預設。

| `query_mode` | 方法 |
| --- | --- |
| `exact_lexical` | statement 上 PostgreSQL `simple` 正規化後的全詞匹配；不是原始 bytes 相等 |
| `deterministic_lexical_recovery` | grounded brief 預設；記錄 simple 嘗試，再採 English morphology；仍零結果且 English 至少 3 詞才放寬至至少 2 詞重疊 |
| `experimental_han_lexical_recovery_v1` | recovery-v2 最終零結果後才加字面 Han 全詞匹配；混合 ASCII 另依規則處理 |
| `experimental_multisurface_lexical_v1` | 研究路線；既有 baseline 候選與 statement／有界來源本文候選聯集 |
| `practical_multisurface_lexical_v1` | Detective 實務路線；有條件零命中補查與 Han 輔助詞限制 |

`simple` 有命中並不會讓 recovery-v2 提早停止；它仍執行 English morphology。
Han 字面 fallback 與 multisurface bigrams 是不同演算法，都不是通用中文斷詞、翻譯或語意推論。

### 實務 multisurface plan v2

1. 將連續 Han 字元轉為重疊雙字片段，其他文字使用 PostgreSQL English 正規化；保留原始 query。
2. 保留 baseline 候選；statement 與來源本文的英文門檻為 `min(2, 詞數)`，Han 依選入政策補充候選。
3. 固定輔助片段如「影響、造成、是否、如何」不能單獨觸發 Han 補充命中。
   若問句只有輔助 Han 片段且無英文詞，保留明示的廣查例外。這不是語意主題辨識。
4. 只有首輪完整零結果、有英文詞、baseline 未截斷且沒有來源被上限排除，才再以 1 個英文詞查一次。
   非空結果、錯誤、取消或資料超限不會觸發擴張。
5. SQL 在排序與 limit 前套用選入條件。以 distinct matched-term count 降冪、建立時間降冪、ID 升冪排序；
   原詞仍用於匹配說明與計分，不改來源字詞或候選引文。

實務 mode 名稱仍為 v1，實際 plan 為 `practical-multisurface-lexical-v2`，回應為
`grounded-evidence-brief-v7`；三個版本代表不同契約。Detective 明確選用 mode／schema，
不相容時回報錯誤，不默默降級。研究 v6 保留獨立模式，不把固定案例結果當成一般品質保證。

### 範圍與限制

- 完整來源補查只涵蓋 `manual_text`／`manual-text-identity/v1`，不是所有 external-document renderer。
- Source view 最多 1 MiB、4,096 spans，單一 span 最多 8,192 bytes；超限明示排除，不聲稱查遍。
- 每個來源至多回傳 8 個匹配 spans，超出明示 truncation；另以 `within_proposal_source_refs`
  區分新找回的上下文與候選原引用，不修改原提案。
- Query 最多 256 個 Unicode 字元、16 個空白分隔詞，結果 limit 最大 100；filter 與可見範圍不可因補查放寬。
- 一次 evidence query 使用 Repeatable Read／Read Only 快照；逾時或完整性失敗是錯誤，不是假空集合。
- 沒有向量搜尋、模型 query rewrite 或語意蘊涵判定。零命中只表示在本次範圍與規則內沒找到。

程式依據：[query modes](../internal/evidenceingestion/query_execution.go)、
[lexical recovery](../internal/evidenceingestion/query_recovery.go)、
[Han fallback](../internal/evidenceingestion/query_han_recovery.go)、
[multisurface](../internal/evidenceingestion/query_multisurface.go)、
[source bounds](../internal/evidenceingestion/source_view_bounded.go)。
理論背景見 [搜尋文獻](#search-references)；目前排序不是 BM25、PathSim 或論文評分器的實作。

<a id="detective"></a>

## Detective 擷取與恢復

### 短摘要與可讀證據

現行 Brief 採 WorldMonitor-style：把有界本文交給模型，一次產生 1–2 句短摘要；
prompt v2 要求保留主體、行為／狀態、範圍與未知，不額外分析。
單次呼叫、無 tools、無自動重試；本文最多 32 KiB，請求上限 768 output tokens、2 分鐘。
保存原文、projection、來源／輸入雜湊及原始輸出；摘要不是來源，也不是 review approval。

可讀性以人能理解完整主張為優先：誰做了什麼、必要的對象、條件與範圍不能只剩零碎關鍵字。
原始證據用詞不做地區詞彙改寫。這是擷取與人工審閱原則，不宣稱程式可驗證所有語意或英文文法。
不以「刪掉文字後模型答案不變」證明剩餘片段充分；理由見 [Feng 等人的研究](#extraction-references)。
舊版摘要引導、多步推理與 persona 對照是研究背景，不是本 Brief 的額外步驟。

人選擇可讀 statement 及連續 1–12 行精確引用；controller 驗證行／bytes 與保存來源一致，
語意是否支持主張仍由人判斷。Brief 對應的原始 body 經 `submit_text_source` 保存為 `manual_text`，
以本文雜湊標識版本；模型摘要另存為候選材料。
外部 URL／revision 在這條路徑是 caller-declared metadata，不能冒充 provider-qualified 外部來源。

### 本機狀態與不確定結果

```text
保存來源／Brief → 提交 pending → Query 精確讀回 → 取得 native review display
→ 人選 outcome／reason → 先保存 frozen decision → 呼叫 writer → Query 獨立核對
```

Checkpoint、來源收據、Brief 與決定檔是私有不可變資料，不能用它們取代 DB 查回。
重開檔案預設是 `historical_not_rechecked`；App 重啟回離線模式，不自動連來源、model 或 MCP。
離線聊天提示沒有核准效果，輸入 `admit` 不會改 DB。

遠端可能成功但本機收據未取得；恢復只允許凍結的原決定與明確選定 launcher。
符合原完整 subject、理由與確認條件後才 exact replay；衝突、缺少必要資料或不支援的契約須停止。
Query 讀回終態不等於驗證了原 decision ID／reason，不能據此編造遺失收據。

Workspace 使用受保護目錄、固定 directory handle 與非阻塞檔案鎖；鎖只協調遵守協定的本機程序。
它不是權限沙箱，也不提供多使用者或分散式協調。工作區設定與 launcher／DB 憑證不進產品 repository。

見 [Brief extractor](../apps/detective/internal/sourcepilot/brief.go)、
[source-to-pending adapter](../apps/detective/internal/ahemcp/brief.go)、
[Desktop workflow](../apps/detective/internal/desktop/brief_workflow.go)、
[query client](../apps/detective/internal/ahemcp/search.go)。

<a id="authority"></a>

## 執行權限與完整性

| 程序／profile | 現行公開範圍 |
| --- | --- |
| Query | 13 個唯讀工具；不呼叫模型、不寫證據 |
| Intake | 5 個 source／extractor tools；只能來源／擷取／pending |
| `source-claim-reviewer` | 3 個 exact source-review tools；admit／reject／audit_only |
| `legacy-reviewer`／`legacy-operator` | CLI 拒絕啟動；內部 43-tool registry 不是對外能力清單 |

以執行時 `tools/list` 為介面權威。Tool arguments 不能切換 schema、DB role 或 reviewer principal。
Separate LOGIN／NOLOGIN role、固定 launcher 與每次連線／重用檢查限制執行權限；
Query 可見的是選定 schema，不是逐列租戶隔離。Reviewer 的 table ACL 仍屬 trusted raw DML，
不能單靠 ACL 宣稱所有直接 SQL 都經過 exact review。DB owner／superuser 是受信任維運邊界。

MCP 使用自己的 migration ledger，目前至 46；不能把另一個 Core repository 的同號 migration 直接接入。
Migration／runtime 核對名稱、checksum 及受保護 schema 物件；失敗不自動刪資料或放寬 ACL。
舊資料轉換、持久 DB 部署與服務角色配置需要獨立操作授權。

見 [runtime profile gate](../cmd/ahe-ingest-mcp/main.go)、
[ingestion authorization](../internal/mcpadmin/authorization.go)、
[query authorization](../internal/mcpquery/authorization.go)、[role policy](../internal/dbrole)。
安裝步驟集中在 [INSTALL](../INSTALL.md#runtime-role-provisioning-gate)，不以本設計文件取代操作檢查。

<a id="references"></a>

## 參考文獻

以下收錄可從既有 AHE／Detective 設計及研究紀錄追溯的文獻；論文、教科書、規格與外部工程方法分開列出。
引用是設計背景，不會讓研究候選變成已實作功能，也不是對 AHE 的形式證明。
未找到具體書目的理論關鍵字，不補造「曾經參考」的論文。

<a id="extraction-references"></a>

### 擷取、引用與可讀性

- Alexander Fabbri、Chien-Sheng Wu、Wenhao Liu、Caiming Xiong（2022），
  [QAFactEval: Improved QA-Based Factual Consistency Evaluation for Summarization](https://aclanthology.org/2022.naacl-main.187/)，NAACL。
  摘要／來源一致性的研究背景；未採用其訓練流程或評分器。
- James Thorne、Andreas Vlachos、Christos Christodoulopoulos、Arpit Mittal（2018），
  [FEVER: a Large-scale Dataset for Fact Extraction and VERification](https://aclanthology.org/N18-1074/)，NAACL。
  分開處理主張、證據與資訊不足；FEVER 標籤不是 AHE admission outcome。
- Jay DeYoung 等（2020），[ERASER: A Benchmark to Evaluate Rationalized NLP Models](https://aclanthology.org/2020.acl-main.408/)，ACL。
  區分人可理解的理由與對模型預測的忠實性；未實作 ERASER 的充分性／comprehensiveness 評分。
- Shi Feng 等（2018），[Pathologies of Neural Models Make Interpretations Difficult](https://aclanthology.org/D18-1407/)，EMNLP。
  輸入刪減後模型仍高信心回答，不足以證明剩餘文字可讀或充分；保留人能理解的脈絡是 AHE 的設計選擇，不採其微調方案。

<a id="search-references"></a>

### 搜尋與排序

- Christopher D. Manning、Prabhakar Raghavan、Hinrich Schütze（2008），教科書 *Introduction to Information Retrieval*：
  [第 6 章：Scoring, term weighting and the vector space model](https://nlp.stanford.edu/IR-book/html/htmledition/scoring-term-weighting-and-the-vector-space-model-1.html)、
  [Inverse document frequency](https://nlp.stanford.edu/IR-book/html/htmledition/inverse-document-frequency-1.html)。
  區分候選匹配與排序；罕見詞權重不是證據真假或必須命中的語意條件。
- Kalervo Järvelin、Jaana Kekäläinen（2002），[Cumulated Gain-based Evaluation of IR Techniques](https://doi.org/10.1145/582415.582418)，ACM TOIS 20(4), 422–446。
  歷史排序研究的分級相關性與 nDCG 背景；不是 runtime 的可信度分數。
- Ellen M. Voorhees（2003），[Evaluating the Evaluation: A Case Study Using the TREC 2002 Question Answering Track](https://aclanthology.org/N03-1034/)，HLT-NAACL，260–267。
  歷史 reciprocal-rank 評估背景；第一筆可用結果不代表所有重點都已找齊。
- Richard Sproat、Thomas Emerson（2003），[The First International Chinese Word Segmentation Bakeoff](https://aclanthology.org/W03-1719/)，SIGHAN。
  中文斷詞標準差異的研究背景；Han 字元匹配不宣稱是該基準的斷詞器。

### 圖關係、版本與一致性

- Andrian Marcus、Jonathan I. Maletic（2003），[Recovering Documentation-to-Source-Code Traceability Links using Latent Semantic Indexing](https://ieeexplore.ieee.org/abstract/document/1201194/)，ICSE，DOI `10.1109/ICSE.2003.1201194`。
  文件／程式追溯候選的研究背景；相似度不足以自動核准 `implements`。
- Yizhou Sun、Jiawei Han、Xifeng Yan、Philip S. Yu、Tianyi Wu（2011），[PathSim: Meta Path-Based Top-K Similarity Search in Heterogeneous Information Networks](https://www.vldb.org/pvldb/vol4/p992-sun.pdf)，PVLDB 4(11)。
  歷史異質圖路徑研究；未實作 PathSim，也不把同型對稱公式直接套成有向 `implements` 判準。
- Maurice P. Herlihy、Jeannette M. Wing（1990），[Linearizability: A Correctness Condition for Concurrent Objects](https://www.cs.cmu.edu/~wing/publications/HerlihyWing90.pdf)，ACM TOPLAS 12(3), 463–492。
  並行操作與合法循序歷史的規格背景；不是 AHE 已取得完整線性化證明的宣告。

### 尚未移植的 admission 理論研究

- Paul H. Morris、Robert A. Nado（1986），[Representing Actions with an Assumption-Based Truth Maintenance System](https://cdn.aaai.org/AAAI/1986/AAAI86-003.pdf)，AAAI。
  多假設環境與支持集合的研究背景；未實作 ATMS，移除一條支持不等於移除其他獨立支持。
- Akhil A. Dixit、Phokion G. Kolaitis（2021），[Consistent Answers of Aggregation Queries using SAT Solvers](https://arxiv.org/pdf/2103.03314v3)，arXiv:2103.03314v3。
  指定完整性約束下的修復與一致查詢答案研究；未實作 AggCAvSAT 或 SAT 聚合查詢。
- Alexandra Meliou、Wolfgang Gatterbauer、Katherine F. Moore、Dan Suciu（2010），[The Complexity of Causality and Responsibility for Query Answers and non-Answers](https://homes.cs.washington.edu/~suciu/file22_main.pdf)，PVLDB 4(1)。
  查詢 lineage 與因果責任的區分；AHE 依賴邊不是因果證明，也不計算責任度。

### 正式規格與工程方法

- W3C PROV（2013）：[Overview](https://www.w3.org/TR/prov-overview/)、[PROV-DM](https://www.w3.org/TR/prov-dm/)、[PROV-CONSTRAINTS](https://www.w3.org/TR/prov-constraints/)。
  來源實體、活動、產生者與推導關係的概念背景；不宣稱完整 PROV 相容性，`supports_claim` 也不直接等同 `wasDerivedFrom`。
- IETF RFC 9110（2022）：[Entity Tags §8.8.3](https://www.rfc-editor.org/rfc/rfc9110.html#section-8.8.3)、[If-Match §13.1.1](https://www.rfc-editor.org/rfc/rfc9110.html#section-13.1.1)。
  不透明版本識別與預期版本條件；不能從 opaque revision 推導先後或 lineage。
- Unicode UAX #29，Revision 47：[Unicode Text Segmentation](https://www.unicode.org/reports/tr29/tr29-47.html)。
  文字邊界及語言特化的規格背景；AHE 不宣稱實作此版本的完整斷詞器。
- Clark Barrett、Pascal Fontaine、Cesare Tinelli，[The SMT-LIB Standard, Version 2.7，2025-07-07](https://smt-lib.org/papers/smt-lib-reference-v2.7-r2025-07-07.pdf)。
  歷史形式一致性研究；未導入 SMT solver，unsat core 不判定自然語言事實。
- PostgreSQL 18 官方文件：[全文搜尋](https://www.postgresql.org/docs/18/textsearch-controls.html)、[交易隔離](https://www.postgresql.org/docs/18/transaction-iso.html)、
  [`ON CONFLICT`](https://www.postgresql.org/docs/18/sql-insert.html#SQL-ON-CONFLICT)、
  [`SECURITY DEFINER`](https://www.postgresql.org/docs/18/sql-createfunction.html#SQL-CREATEFUNCTION-SECURITY)、[prepared statements](https://www.postgresql.org/docs/18/sql-prepare.html)。
  搜尋、交易與重播的資料庫參考；資料庫機制本身不證明應用程式協定正確。
- WorldMonitor：[固定版本的摘要提示建構](https://github.com/koala73/worldmonitor/blob/af4e6da5642f0fe6ddba62fd52ebe6dbcc341ef5/server/worldmonitor/news/v1/_shared.ts#L38-L108)。
  外部工程方法，不是論文；保留既有查核版本，本輪未能重新載入此上游頁面。
  Detective 借用短摘要工作方式，不宣稱複製整套資料流或保證相同模型品質。
