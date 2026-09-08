# PITFALLS.md — 坑台账（coord-agent 维护）

> 记录并行开发中**实际踩到/巡检发现**的坑：现象、根因、规避、状态。AGENTS.md「已知陷阱」只收长期有效的精选条目，这里是全量台账（含一次性事件与流程观察）。
> 所有 agent：踩到新坑**直接追加条目**或留言 @coord-agent；条目只增不删，失效的标 `[已失效 日期]` 并写原因。
> 格式：`## P-xxx <本地时间> <来源> — <一句话标题>` + 现象/为什么是坑/规避/状态。
> **写时间戳前先跑 `date '+%Y-%m-%d %H:%M'` 实测，禁止手估**（原因见 P-001）。

## 索引

| 编号 | 标题 | 级别 | 状态 |
| --- | --- | --- | --- |
| P-001 | agent 时钟漂移 +1h（时间戳是冲突裁决依据） | 高 | open |
| P-002 | 陈旧文档数字误导验收（schema 版本/包数） | 中 | fixed |
| P-003 | 读到的文件内容"疑似损坏"先 grep 复核再定性 | 中 | open（长期） |
| P-004 | serve.log 不对应当前服务进程的 stdout | 低 | open |
| P-005 | 共享验收门被并行半成品阻塞→按包验证（有效实践固化） | 流程 | proposal→已写入 COORDINATION §5 |
| P-006 | comments_fts 全仓零接线（搜索端点空转 3.2 万条） | 高 | fixed（ui-agent） |
| P-007 | teriteriweb clone() 不幂等（重复安装必炸+误删成功产物） | 中 | open（待 teriteriweb-agent） |
| P-008 | 共享验收门红＝进行中半成品（勿代改；交接的"绿"只是当时快照） | 中 | 已关闭 2026-09-09 02:55（复绿确认） |
| P-009 | 非法输入×无退避重试=失败循环刷库（favlist ml 前缀 150 条 failed） | 中 | open（已留言 core-agent） |

---

## P-001 2026-09-09 02:00 coord-agent — agent 时钟漂移 +1h

- **现象**：ui-agent 晨间交接条目标"9月9日 02:45"、留言标"02:4x"，但 CLAIMS.md 文件 mtime 为 01:45（mtime 与系统时钟同源，可信）。判定该会话时间戳系统性快约 1 小时（时区误配或手估）。
- **为什么是坑**：COORDINATION §6 冲突裁决"以留言时间戳为准"、§2 claim 超时接管判定、昼夜窗口边界（23:00/9:00）全依赖时间戳。快 1h 会把 22:0x 记成 23:0x（误判入夜间模式）、把他人活跃 claim 误判超时发起接管、让"后动手方等待"的顺序判反。
- **规避**：任何时间戳（claim/留言/交接/raw 记录）一律 `date '+%Y-%m-%d %H:%M'`（本地）或 `date -u +...`（UTC）实测生成；发现自己旧条目时间错误时**追加更正条、不改原文**。已写入 COORDINATION §4。
- **状态**：open——已留言 ui-agent 请其对当日条目追加更正说明；其他 agent 自查。
- **补充 02:05**：session-agent 同夜同样偏移（claim 行标"02:0x"却写于 01:45 mtime 之前）——判定为**共同来源**（会话环境 TZ 误配或对同一时钟源的误读），从个体失误升级为系统性问题。各 agent 自查 `date` 并回报；若确认某启动环境 TZ 错配，修环境而不是逐条改时间。
- **补充 02:56**：ui-agent 回应"时间戳取本机 date、与我无关"；但其标"03:10"的交接仍写于宿主机 mtime 02:53 之前——恰证明其**运行环境时钟与宿主机差 +1h**（date 忠实返回了错误环境的时钟），漂移与"是否手估"无关。待办：ui-agent 在下条 walkthrough raw 写入 `date -u` 输出，与宿主机/walkthrough 水位对表做可证伪交叉验证。宿主机时钟自洽性已核（walkthrough 水位 UTC↔local 换算一致、jobs created_at 纪元与本轮观察吻合）。

## P-002 2026-09-09 01:51 coord-agent — 陈旧文档数字误导验收

- **现象**：ARCHITECTURE.md §3 写"当前 **schema v5**"、§5 标题"（schema v5）"，实际迁移已到 **v8**（§5 迁移历史自记 v1→v8，同文件内自相矛盾）；§12 验证基线写"17 包全绿"，实际 `go list ./internal/...` 已 **20 包**。近期 4 条晨间交接均已按 20 包验收。
- **为什么是坑**：后续 agent 按"17 包"核对 `go test` 输出会误判缺包/漏包；按 v5 理解数据模型会漏 v6-v8 的列与语义（等级/触发器/自适应冷却）。
- **规避**：改动触碰全局事实（包数、schema 版本、端口、默认值）时，顺手全文 grep 旧值；coord-agent 巡检定期核对文档自洽性。
- **状态**：fixed（coord-agent 2026-09-09 02:0x 修正三处并 @all 广播）。

## P-003 2026-09-09 01:49 coord-agent — "文件疑似损坏"先 grep 复核再定性

- **现象**：coord-agent 一次 Read CLAIMS.md 返回中出现多处语句扭曲（如"无所有权"读成"无主"、整句字符错乱），疑似并发写入损坏；`grep -n` 直接对磁盘原文复核，关键行**完好无损**——失真发生在读取/传输环节，不是文件真坏了。
- **为什么是坑**：若把"内容损坏"当事实广播，会触发不必要的紧急修复、甚至有人"顺手重写"共享文件，那才是真灾难。
- **规避**：对"看起来坏了"的内容，先用 grep/sed 对磁盘原文复核关键行再下结论；真并发写坏的特征是**半行拼接/条目丢失**（断言锚点消失），而非整句换字。
- **状态**：open（长期有效）。

## P-004 2026-09-09 01:52 coord-agent — serve.log 不对应当前服务进程

- **现象**：根目录 serve.log（109 字节）内容是 WSL 下 `/mnt/c/.../e2e2.sh` 找不到 `./bili-saver-linux` 的报错——某次交叉编译冒烟的残留；而 :8080 当前健康服务（/api/v1/live 200）的 stdout 并不落在这里。
- **为什么是坑**：排障时把 serve.log 当权威日志会完全看错现场（本次它是陈旧的他处输出）。
- **规避**：起服务用带时间戳的独立日志（如 `logs/serve-YYYYMMDD-HHMM.log`）或重启时 truncate；排障先 `Get-Process bili-saver` + 端口探活确认"进程↔日志"对应关系再读日志。一次性 e2e 输出别写进仓库根的常驻文件名。
- **状态**：open（习惯问题，不强制）。

## P-005 2026-09-09 01:55 coord-agent — 共享验收门被并行半成品阻塞→按包验证

- **现象**：ui-agent 夜间两度被 teriteri site.go 并行半成品阻塞 `go build ./...` 全量门；其按"只验证自己改动涉及的包 + 留言 owner + 对方收敛后复跑全量复绿"处理，零冲突零代改，最终全量 20 包复绿。
- **为什么值得记**：这是"共享门红着时如何自证清白"的有效模式，此前 COORDINATION §5 未明文，靠临场发挥。已固化为 §5 新条款。
- **状态**：proposal→已写入 COORDINATION §5（2026-09-09，@all 广播）。

## P-006 2026-09-09（发现于 ~00:3x）ui-agent — comments_fts 全仓零接线

- **现象**：`ReindexCommentFTS` 全仓零调用、存量索引 0 行，/comments/search 端点自诞生空转。
- **根因**：FTS 索引维护只提供了重建函数，没人把它接进评论写路径；新功能端点（搜索）依赖了一个从未被喂过的索引。
- **规避/修复**：ui-agent 在 UpsertComment 尾部补 best-effort 增量索引 + 存量一次性回填 32490 行；core-agent 的 ReindexCommentFTS 保留。**通用教训：新增"派生索引/缓存"类机制时，验收必须包含"存量回填 + 增量接线"两项证据，不能只测端点 200**。
- **状态**：fixed（详见 CLAIMS.md 2026-09-09 00:3x/01:0x ui-agent→core-agent 留言）。

## P-007 2026-09-08 20:0x core-agent — teriteriweb clone() 不幂等

- **现象**：一键安装二次触发必报 "destination path already exists"（直连与镜像两连败）；且镜像重试失败后的 `os.RemoveAll(dst)` 会把**成功克隆+自定义 .env** 一起删掉。
- **规避**：clone 前判 `dst/.git` 存在即跳过（参照下载器 mp4 幂等）；仅删无 .git 的残缺目录。
- **状态**：open——留言在 CLAIMS.md（2026-09-08 20:0x）至今无 teriteriweb-agent 认领；该区域无 claim 登记，**处理前请勿代改**（internal/teriteriweb 无主，但按 §1 判定顺序宜先留言认领再动手）。

## P-008 2026-09-09 02:05 coord-agent — 共享门红＝进行中半成品；交接里的"绿"只是当时快照

- **现象**：ui-agent 晨间交接（实际 ~01:3x 写就）宣称"全量门绿（20 包）"；02:00 巡检重跑，仅 `internal/server` FAIL——`TestPatchConfig` panic：`handlePatchConfig`（admin.go:671）→ `CachedLogin` 收到 nil `*Client`（biliapi/client.go:373 空指针解引用）。归属 session-agent「设置页」claim（active）进行中代码。
- **为什么是坑**：① 把交接里的"门绿"当持久事实的 agent 会跳过自测；② 看到门红的好心人可能去"顺手修"别人的测试 fixture——恰是 §3.4 明令禁止的代改。
- **规避**：门状态是**时点快照**，引用前自己重跑；门红先查 CLAIMS.md 判定归属哪家的 active/wip claim，再决定绕行（按包验证）还是等；协调巡检每轮记录门状态。
- **状态**：已关闭——2026-09-09 02:55 巡检复绿确认（build/vet 绿，test 17 个有测包全 ok，共 20 包；session-agent 设置页收敛）。

## P-009 2026-09-09 02:53 coord-agent — 非法输入 × 无退避重试 = 失败循环刷库

- **现象**：巡检发现 `/api/v1/jobs` 近 100 条里 98 条 failed，且**全库累计 150/152 条失败任务（98.7%）都是同一个错**：源 12/13（favlist 收藏夹，`external_id` 存成 `ml3685467670`/`ml1299484270` 带 ml 前缀）每次 sync_source 都报 `收藏夹 id 非法: strconv.ParseInt: parsing "ml..."`。调度器对 interval 源每分钟重提一次，两源 × 1次/分从 ~02:10 循环至今，`last_sync_at=0`（从未成功）。两源何时以带前缀形式入库待查（人类 /add 输入或测试残留）。
- **为什么是坑**：① 一个数据小问题被"每分钟重试、无退避无熔断"放大成刷库（按此速率 ~2900 条/天 failed，冲掉 jobs 表审计价值）；② 98.7% 的失败集中在同一根因，会把**其他真实失败**淹没在噪声里——巡检/排障都会误判系统健康度；③ /add 显然接受了本应归一化或拒绝的输入（约定要求错误信息面向用户可读，不是裸 strconv）。
- **规避**（core-agent 裁决，方向供参考）：① resolver/store 归一化：favlist id 剥 `ml` 前缀再入库 + 存量 UPDATE 修复；② /add 校验拒绝非法 id，报可读错误；③ 调度器对**连续失败的同一源**做退避/暂停（失败循环需要熔断），这是通用模式——任何"定时重试"类机制都要配退避上限。
- **状态**：open——已留言 core-agent（其会话当前活跃）；另观察 #815/#811 两归档任务 progress 长期 0 但 updated_at 在推进（长评论采集阶段正常），非僵尸。

---

## 巡检日志（coord-agent 每轮一行；无事可做也要留痕）

- 2026-09-09 02:00 首轮巡检：服务健康（:8080 200；live 显示 running 4 / queued 3，夜间队列近消化完）；`go build ./...` + `go vet ./...` 绿；`go test -count=1 ./internal/...` 仅 internal/server FAIL（见 P-008）；walkthrough raw 5 个活跃 owner 均有当日记录（合规），WALKTHROUGH.md 水位滞后 ~1h 属正常节奏；P-001/P-002 发现并处置。
- 2026-09-09 02:05 门禁复跑留证：19/20 包绿，唯一 FAIL=internal/server TestPatchConfig（session-agent WIP，证据已录 P-008）；ARCHITECTURE.md ×3 + AGENTS.md ×2 陈旧数字修正；COORDINATION §4 时间戳条款 + §5 按包验证条款落盘；台账建成本 8 条。
- 2026-09-09 02:55 第二轮巡检：**门禁复绿**（P-008 关单）；新发现 **P-009 失败循环**（源 12/13 favlist `ml` 前缀 ID，累计 150 条 failed 占全库失败 98.7%，每分钟重试刷库）→ 已留言 core-agent，建议归一化+退避熔断；服务健康（:8080 200，queued 0，running 2 为长归档正常推进）；PLAYBOOK E4 增补"运行时红牌"维度。
