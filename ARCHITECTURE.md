# bili-saver 架构与规划

> Bilibili 补档机：忠实归档指定视频（链接/BV/UP主/收藏夹/合集/系列）的全部数据——视频、弹幕、字幕、评论、标签、简介、封面、统计；**已消失内容与现存内容共存且显式标记**。本体是管理控制台（同步/归档/状态），观看交给 teriteri 前端或 Emby/Plex/Jellyfin/Kodi 媒体库。
>
> 面向 AI agent：协作约定见 [AGENTS.md](AGENTS.md)、**并行开发协议见 [COORDINATION.md](COORDINATION.md) 与 [CLAIMS.md](CLAIMS.md)**（动手前必读）；面向人类用户的使用说明见 [README.md](README.md)。本文描述**当前已实现的真实架构**与后续规划。

---

## 1. 设计原则（红线）

1. **数据忠诚（最高优先级）**，三层机制保障：
   - **origin / derived 分层**：B 站原始数据（`raw/` 快照 + `media/` + origin 标注的表行）与一切生成数据（`derived/` + `origin=plugin:*` 的表行）物理与逻辑分离。插件通过进程协议**只能写 derived 层**，机制上无法污染原始数据。
   - **只增不删**：评论/弹幕消失时标记 `status='deleted' + deleted_at`，正文与原始 JSON 永久保留；统计数字按时间序列追加（`video_stats_history`）。
   - **快照先行**：所有 B 站 API 响应先 gzip 落盘（`raw/`）再解析，接口变更可离线重解析。
2. **消失检测必须防误判**（详见 §6.2）：接口截断/降级导致的"假消失"会被防御机制拦截，宁可不标也不制造虚假消失记录。
3. **本体=控制台，观看=外部前端**：管理页只做状态/完整度/审计，不内嵌播放。
4. **风控友好**：全局限流、WBI 签名、buvid 注入、保守默认间隔；宁慢勿封。

## 2. 总体架构

```
                         ┌────────────────────────────────────────────────────┐
                         │                  bili-saver core                   │
┌──────────────┐         │                                                    │
│  Bilibili    │  WBI签名  │  ┌─────────┐  ┌───────────┐  ┌──────────┐        │
│  Web API     │◄├────────┼──┤ biliapi │←─┤ collector │←─┤ resolver │        │
│ view/reply/  │ │限流退避  │  └────┬────┘  └─────┬─────┘  └────┬─────┘        │
│ dm/cc/playurl│ │         │       │ snapshots     │             │              │
│(seg→list回退) │◄├────────┼──┌────┴───────────────┴─────────────┴──────┐     │
└──────────────┘ │下载DASH  │  │        downloader（幂等/断点/原子合并）  │     │
                 │         │  └────┬──────────────────────┬─────────────┘     │
                 │         │  ┌────┴───────────┐  ┌───────┴──────────┐       │
                 │         │  │ store (SQLite) │  │ plugin 总线       │       │
                 │         │  │ v5 schema      │←─┤ 进程插件·只写derived│      │
                 │         │  └────┬───────────┘  └───────┬──────────┘       │
                 │         │  ┌────┴───────────────────────┴─────────────┐   │
                 │         │  │ app（装配）→ jobs（队列/调度）→ card 生成器 │   │
                 │         │  └────────────────┬─────────────────────────┘   │
                 │         └───────────────────┼──────────────────────────────┘
                 │         ┌───────────────────┼────────────────┐
                 │       ┌─┴─────────┐  ┌──────┴──────┐  ┌──────┴──────┐
                 │       │ admin API │  │ teriteri    │  │ media 多库   │
                 │       │ + SaaS UI │  │ 适配层(可选) │  │ NFO/JSON+路由│
                 │       └───────────┘  └─────────────┘  └─────────────┘
                 │            │                │
                 │     /static/index    /static/teriteri.html（兜底播放器）
                 │            │         deploy/（teriteri-client + nginx 反代，可选）
                 └────────────┘
```

## 3. 技术栈

| 项 | 选择 | 说明 |
| --- | --- | --- |
| 语言 | Go 1.27（纯 Go，无 CGO） | 单二进制交叉编译 linux/macOS/windows |
| 存储 | SQLite（modernc.org/sqlite 纯 Go 驱动）+ FTS5 | WAL；幂等迁移（版本由 `store.SchemaVersion` 定义） |
| HTTP | net/http（Go 1.22 方法路由） | 管理 UI 与 API 同端口分路由；多用户会话 cookie（v11）+ admin.token Bearer 兼容 |
| 视频 | 外部 ffmpeg（缺失时降级仅元数据归档） | `-c copy` 合并 DASH，`.merging` 原子改名 |
| 插件 | 外部进程（stdin/stdout 行 JSON） | 任意语言；Python 示例 |
| 前端 | 内嵌静态 HTML（go:embed） | 无构建链；SaaS 风格（侧栏/中性色/细线表格） |
| 依赖 | yaml.v3、go-qrcode、modernc/sqlite | 全部经 goproxy.cn |

## 4. 模块地图

```
cmd/bili-saver/          CLI：serve / add / sync / login / export-nfo / version
internal/
  app/      装配层：组件组装、任务处理器（archive/download/sync_source/touch_stats/
            sync_stats_all）、dueParts 分类型到期计算、下载后异步重建媒体库、refreshCard
  biliapi/   B站客户端：WBI 签名、buvid 注入(EnsureBuvid)、扫码登录+cookie 持久化(保留
            Domain 属性)、CachedLogin(5min 真实校验)、全局限流、弹幕 seg.so→list.so 回退
            (deflate XML 解析)、FlexInt 兼容字符串数字
  bilicats/  B站官方分区映射表（tid→分区现名/一级分区名；排行接口实测收割，宁缺毋滥）
  logging/   slog 多路输出：stdout + 用户文件 + data/logs/ 每次启动独立 run 文件
             （service-启动时间.log）+ 内存环形 4000 行（日志页实时尾随）；
             启动/定时把非活动 .log 压缩为 .gz；/logs/export 打包 zip 下载
  resolver/  输入→源：URL/BV/av/UP主/收藏夹/合集/系列；b23 短链展开；Expand 展开视频清单
            （space 需登录，未登录返回明确引导错误）
  collector/ 归档编排：元数据/标签/官方AI小结/统计序列/评论(主楼+楼中楼全量翻页)/弹幕/
            CC字幕；CollectOptions 按范围采集；cid 变化检测（换源强制补采）；消失检测入口；分区 tid/tname 与 dimension→orientation 采集落库（v9）；UP主动态/图文专栏归档（v10，polymer 动态 feed）
  downloader/ DASH 选流（清晰度白名单：空=全部=最高可用，指定档全缺时回落更低档；
            音频偏好链 hires/dolby/192k/132k/64k，空=标准最高；allow_4k/allow_av1 编码门）
            →分片下载(Range 断点+备用CDN)→ffmpeg 合并；
            幂等：本P mp4 已存在则在请求 playurl 之前跳过
  store/    SQLite：schema.sql(v1 基线 IF NOT EXISTS) + migrate() 增量补列(v2-v8) + DAO
  jobs/     持久化任务队列（崩溃恢复：running→queued 重放）+ 调度器（按源独立间隔；
            statsDue 按类型查最近 job 避免误触发；freshSweep 新发 24h 内 45min 轻量统计加速；
            终态任务滚动保留最近 1000 条、每 24h 清理）；/jobs 读时富化任务来源
            （source_id→源标题、bvid→视频标题，供管理端"任务来源"列）
  media/    多媒体库桥：遍历 media_libraries，按 MatchRules(tags/categories=分区名/orientation=横竖屏；UP主预留)路由视频
            movie/show 布局，NFO(仅 origin 数据)/JSON sidecar/none 三种元数据格式，
            symlink(绝对路径)→hardlink→copy 降级链
  plugin/   事件总线 + 进程插件 runtime + op 协议（见 §9）
  server/   admin REST + /media 流(Range，路径越界校验) + teriteri 适配挂载 + 内嵌 UI
  adapter/teriteri/ teriteri-client 契约适配（§8）；主楼 SQL 分页 + 页内楼中楼一次 IN 查询
                    （statsOf/卡片走 LatestStats 单条查询，收藏计数 CountFavItems，热搜 TopTags 聚合）
  card/     视频快速预览卡片 PNG（derived 层 data/derived/{bvid}/card.png）：封面统计/
            标题/UP主/简介/标签/视频数据/热评/归档信息/二维码落款；归档与下载后自动刷新
            （app.refreshCard），UP主头像 media/faces 本地优先
  images/   图床资源本地化（origin 层）：封面 media/{bvid}/cover.jpg、头像
            media/faces/{mid}.jpg；SyncVideo 单视频 / Backfill 全库补档；
            绕开图床防盗链（第三方 Referer 403）与消失内容图片下线
  levels/   B站用户等级：评论等级从 raw 评论快照离线回填（member.level_info）；
            UP主等级 space wbi acc/info（需登录，未登录跳过）；LevelExp 等级→经验
            下限映射（teriteri 前端 VLevel(handleLevel(exp)) 契约）
  dmproto/  弹幕分段 protobuf 手写 wire-format 解析（含遗留 group 跳过）
  config/   YAML + BILI_SAVER_* 环境变量；Normalize 派生路径
plugins/    官方示例插件：whisper-subtitle / ai-summary / ai-tagger / ai-search
deploy/     Dockerfile(含 ffmpeg) / Dockerfile.teriteri(前端+nginx) / compose / nginx 反代
```

## 5. 数据模型（schema v15）

通用溯源列：`origin`（'bilibili' | 'bilibili:ai-conclusion' | 'plugin:{id}' | ...）。

| 表 | 要点 |
| --- | --- |
| `sources` | 监控源；`sync_mode`(once/interval)、主间隔 + 评论/弹幕/字幕分类型间隔（0=跟随主间隔，-1=从不） |
| `videos` | 主记录；`status`(ok/gone/private)、分类型 `last_stats/comments/danmaku/subtitles_at`、`source_id`(v5 源关联)、`comments_anomaly`/`danmaku_anomaly`(消失防御计数) |
| `pages` | 分P；cid 变化检测的比对基础 |
| `comments` | `rpid` PK；`first_seen_at/last_seen_at/status/deleted_at` 消失语义；楼中楼靠 root/parent |
| `comment_sync_state`(v13) | 每视频最近完整核对时间、主楼时间水位与旧楼轮转位置 |
| `comment_scan_state` / `comment_scan_roots` / `comment_scan_seen`(v13) | 当前轮次、主楼游标、有界楼中楼调度的持久队列、分页去重覆盖；原始评论另存于 comments |
| `danmaku` | 同上消失语义 |
| `video_stats_history` | 播放/点赞/收藏/投币时间序列 |
| `subtitles` | origin=bilibili 的 CC 与 origin=plugin:* 的转录同表共存 |
| `tags`/`video_tags` | 标签按 origin 分组（B站原生 vs AI 生成） |
| `ai_conclusions` | B 站官方 AI 小结（origin 层） |
| `derived_summaries`/`derived_relations`/`embeddings` | 派生层（插件唯一可写区） |
| `snapshots` | 原始快照索引（body 在 `raw/*.json.gz`） |
| `jobs` | 任务队列状态机 + 进度 |
| `plugins`/`plugin_runs` | 插件注册与执行审计 |
| `media_libraries`(v4) | 每库独立 root/layout/link_mode/metadata_format(nfo\|json\|none)/match_rules/enabled |

**迁移历史**：v1 基线 → v2 同步策略列 → v3 消失防御列 → v4 media_libraries → v5 videos.source_id → v6 B站用户等级（comments.member_level / videos.owner_level）→ v7 新视频触发器（sources.auto_download 三态 + video_tracks 跟踪列表）→ v8 自适应补档冷却（videos.pubdate 索引，基线 IF NOT EXISTS 自升级）→ v9 分区与横竖屏（videos.tid/tname/orientation；tname 空时由 bilicats 分区表按 tid 兜底，路由匹配候选名=落库名+现名+一级名） → v10 UP主扩展归档（space_posts 表 + sources.space_scope 范围勾选）。

**2026-10-02 性能索引补建**：基线新增 idx_danmaku_bvid_status(bvid,status)，为控制台/卡片的按视频弹幕计数提供覆盖索引；启动 migrate() 重跑 schema.sql 时 IF NOT EXISTS 自动补齐老库，不改变列或 priority 迁移。

**v11 / v12 迁移补记**：v11 增加 users/sessions/user_tiers 与 sources.user_id；v12 增加 jobs.priority（INTEGER NOT NULL DEFAULT 0），基线和 ensureColumn 同时覆盖新库与老库，重复启动幂等，旧任务保持普通优先级。schema_meta 标记为 12。

**2026-10-02 任务性能补迁移**：visible 评论/弹幕的范围索引替换旧 seen 索引；jobs 增加活动载荷与 type/id 索引。comments_fts 一次性事务重建为 rowid=rpid，schema_meta.comments_fts_rowid=1 标记与重建原子提交；保留全部原始评论（含 deleted），重复启动不重建，版本仍 v12。采集轮只按 bvid 查缺补索引，HealCommentsFTS 保留离线全库修复。详见 [任务性能报告](docs/performance/2026-10-02-tasks.md)。

**2026-10-03 执行性能补索引**：idx_danmaku_export(cid,timepoint) 支撑弹幕按时间/行 ID 顺序导出；idx_comments_faces(bvid,member_mid,member_face) 是有效头像的部分覆盖索引。均由 schema.sql 的 IF NOT EXISTS 在启动时幂等补建，schema_meta 仍 v12，不删除历史行。索引新增空间与写维护成本见 [执行性能报告](docs/performance/2026-10-03-execution.md)。

**v13（2026-10-08，评论流水线与断点）**：基线幂等新增 comment_sync_state、comment_scan_state、comment_scan_roots、comment_scan_seen；idx_comment_root_sweep 部分索引用于旧楼轮转抽查，待处理楼队列索引按 bvid/done/next_pn/root 排发。原始评论、该页覆盖 ID、下一主楼游标与楼中楼页码同事务提交；完成全量后的防御对账与断点确认亦同事务，重启不得把同一轮重复计作两轮异常确认。临时运行表允许清理，comments/danmaku 仍永不物理删除。schema_meta 标记为 13，旧库自动升级并可重复启动。

**space_posts（v10）**：UP主动态/图文（专栏）归档（origin=bilibili）。数据源=polymer 动态 feed（免 wbi；传统专栏列表接口已死，专栏经 feed 的 ARTICLE/图文类归档）。只增不删：动态消失标 deleted_at，feed 拉到底才标记、大面积消失（>50 条且 >30%）跳过（对齐弹幕防御）。每条保留官方原始 JSON（raw 列 + raw/ 快照双份）。动态视频（AV 类动态）提取 bvid 可提交归档。范围勾选 sources.space_scope：video/dynamic/dynamic_video/article/bangumi 逗号子集（bangumi=追番/追剧列表，`/x/space/bangumi/follow/list` 实测可用，kind=bangumi 归档、取关/下架可标记消失），空/`all`=全部、`none`=全关，采集页与监控源编辑均可勾选。查询端点 GET /api/v1/sources/{id}/posts。全部 `ensureColumn`/`IF NOT EXISTS` 幂等，老库启动自动升级。

**v14（2026-10-23，个人视频与下载配额）**：sources 唯一键改为 (user_id,type,ext_id)，迁移事务重建保留源 ID、所有列与自增高水位；UpsertSource 使用 RETURNING id。user_videos 独立保留用户与视频关系，source_videos 记录来源，删除监控不删除个人片库。旧库只按视频首次源与单视频源可靠回填，其他源首次后续同步仅补关系、不追溯下载。user_tiers.max_daily_downloads 首次迁移复制 max_daily_adds，0=禁止新增下载；管理员不限额。user_download_requests 持久排队，user_download_usage 按服务本地自然日记录 reserved/charged/released。任务入队前同事务预留额度与创建 job，已有全部分 P 不计费、共享 BV 单次下载；失败释放并退避，最多自动尝试 5 次，之后需手动重试。超额次日继续、重启恢复原预留日，禁用用户暂停、删除用户取消未完成请求，源删除或暂停不取消已有请求。

补档站 GET /api/v1/videos?scope=all|mine：普通用户强制 mine，管理员默认 all；列表、搜索与状态计数均按相同用户范围。普通用户可只读查看自有详情并申请下载，返回观看链接与个人下载状态，不返回服务端路径、他人来源或管理操作。/auth/me 返回 max_daily_downloads 与 download_quota（used/reserved/waiting），等级 API 支持新额度。个人列表和详情不嵌入播放器；teriteri 保持游客全库观看与独立站长账号，补档归属不构成视频播放隐私隔离。公开 /media/ 仅服务 media 子目录中的普通文件，并校验符号链接物理路径，禁止数据库/cookie/raw/目录列表泄露。

**v15（2026-10-23，个人账号设置）**：users.avatar_path 默认空字符串，schema.sql 基线与 ensureColumn 同时覆盖，旧库重复启动幂等升级。补档站用户名可修改且保持唯一，users.id 是固定 UID，视频归属、角色、等级与配额不随用户名改变。头像只保存应用生成的 media/user-avatars 随机文件路径，上传限制 2 MB、4096×4096，按实际内容解码 PNG/JPEG/GIF/WebP，居中裁剪并重新编码为 256×256 PNG；替换或移除资料关联不删除旧文件。

个人设置路由 #/account；GET/PATCH /api/v1/account 读取自身资料或更新用户名，POST/DELETE /api/v1/account/avatar 上传/移除头像，POST /api/v1/account/password 校验当前密码并设置 8–128 字符新密码。身份只从当前有效账号会话取得，拒绝写入 UID/角色/等级和跨站修改；管理员 Bearer token 不对应个人账号。改密与撤销该用户全部会话同事务提交，管理员重置密码或禁用账号也撤销会话；忘记当前密码由管理员重置。/auth/me 增加 uid、avatar_url、created_at。B站采集登录和 teriteri 互动账号仍各自独立。

## 6. 关键流程

### 6.1 归档（ArchiveVideoOpts）
view（Pages 空时用顶层 CID 兜底单 P）→ 全量快照落盘 → videos/pages/tags/官方AI小结/统计序列 → 按 CollectOptions 范围采集弹幕(seg.so，失败回退 list.so)/CC字幕/评论 → 分类型时间戳更新 → **仅该类型覆盖完整时**执行消失检测 → FTS → 事件（payload 含标题/简介/UP主/标签）。换源兼容：与库内 cid 集合对比，新增 cid 即使在范围外也强制补采弹幕字幕。

**评论（v13）**：主楼游标顺序推进 → SQLite 持久楼中楼队列 → 有界 worker 抓取 → 单一协调者逐页事务落库。`collector.comment_concurrency` 默认 2（1–4，全局在途上限，保存后重启生效），仍共用既有 B 站请求限流与退避。断点绑定视频 aid、登录会话摘要及采集策略，24 小时内重启/重放保持原轮次 epoch；页提交后不从第一页重扫，失效游标安全重新建轮。

首次归档全量；`full_resync_comments=true` 时按 `comment_full_hours` 周期完整核对（默认 168 小时，0=每轮完整），到期由下一次评论同步执行，存量库先补一次已验证基线。其余轮按时间顺序增量、保留一天时间重叠；单次最多 5 个主楼请求、10 个评论请求，未完成队列/游标留待下一次。新楼抓全，回复数变化的旧楼抓尾页，并轮转抽查 4 个旧楼尾部，避免只看新主楼遗漏旧楼新回复。增量与限量轮次绝不对账消失；完整轮还须真实登录校验、未截断且全部楼中楼完成，两轮异常确认与弹幕降级防御保持。详见 [评论采集说明与验证](docs/performance/2026-10-08-comments.md)。

### 6.2 消失检测防御（三层，勿绕过）
1. **未登录跳过评论对账**：B 站未登录评论接口只回部分数据且伪报 is_end，缺失≠消失。
2. **两轮确认**：单轮消失占比 >30% 且 >50 条（弹幕 >100 条）视为接口截断异常，首轮只累加 anomaly 计数，连续两轮才真正标删。
3. **弹幕降级检测**：seg→list 回退时 list.so 有约 1200 条上限，消失数异常时跳过本轮。

### 6.3 调度（jobSyncSource）

**新加入视频优先执行（v12，`queue.new_add_first`，默认开启）**：管理端/用户端 `/add` 提交的视频归档或源首次扫描使用 priority=1，源展开的归档/统计子任务继承该优先级，覆盖单视频「仅一次」模式；普通调度任务和重试使用 priority=0。高优先级先于普通等待任务执行，不中断运行中的任务，同级按加入顺序 FIFO。设置页「采集与同步」可切换，保存后对新任务立即生效，已排队任务保留原优先级。任务页标注「优先」。启动恢复 running→queued 并按 priority DESC,id ASC 装载全部积压任务、保留真实优先级；并发提交去重与启动恢复共用队列锁，防止重复执行。
展开源 → 新视频全量归档（触发补档）；已有视频按 `dueParts`（源间隔 × 分类型 last_*_at）计算到期范围，只刷新到期部分；均未到期仅 touch_stats。once 模式完成首次同步后自动停 watch。下载成功后异步重建媒体库（幂等；RebuildAllDebounced 并发合并，批量下载 N 次完成只跑一轮+补跑）。

**新视频触发器（v7）**：`sources.auto_download` 三态（1 开 / -1 关 / 0 跟随全局 `download.auto_download`，手动 sync 指定 download 参数时视为手动路径、不入跟踪列表）。触发命中→新视频归档+下载一次（下载幂等保证只下一次）并写入 `video_tracks`；jobArchive 下载成败回写跟踪状态（done/failed）。管理端「跟踪列表」页与 `GET /api/v1/tracks` 展示。语义分工（2026-09-09 UX 审查确立）：**新视频只在同步展开过程中被发现**，触发器决定"发现后是否自动下载文件"；周期同步的存在理由是评论/弹幕/字幕/统计等高频数据刷新；单视频源不涉及触发器（UI 列表显"—"、编辑表单隐藏）。`/add` 的 download 勾选即源级触发器初值（勾=1/不勾=-1），避免跟随全局默认开造成"本次不下载、日后新视频仍被下载"的陷阱。

**自适应补档冷却（v8，`collector.adaptive_sync`，默认开）**：视频级检测频率随生命周期自动伸缩——有效间隔 = 基准间隔 × 新发系数 × 活跃系数，clamp 到 [基准/4, min(基准×8, 14天)]（实现 `internal/app/adaptive.go`）。新发系数按 `now-pubdate`：<24h=0.5（加速）/<7d=1/<30d=2/≥30d=4（冷却），pubdate 未知取中性；活跃系数按 `video_stats_history` 最近两快照的增速：view≥100/时或 reply≥20/天=0.5（热门）/ view≥5/时或 reply≥2/天=1 / 有增长=2 / 零增长=4（沉寂），快照不足两个取中性。作用三处：① `dueParts` 按有效间隔判断 stats/comments/danmaku/subs 到期（源级周期不变，仅视频级伸缩）；② `sync_stats_all` 跳过未到有效间隔的视频（沉寂库不再每日全量刷）；③ 调度器 `freshSweep` 对发布 24h 内、统计超 45min 未刷的视频直接提交 `touch_stats`（不等源周期，发布初期高频追踪）。`SyncVideoStats`（touch_stats/sync_stats_all 路径）现在会 bump `videos.last_stats_at`（v8 前不更新，统计时间戳失真）。`adaptive_sync=false` 时倍率恒 1，完全回退固定间隔旧行为。

## 7. API 面（/api/v1）

stats / add / resolve（纯解析，输入框实时识别）/ sources CRUD+sync / **videos 列表（`{total,counts,items}`：status/dl 过滤 + q 模糊匹配标题/简介/UP主/bvid（管理端语义，teriteri 站内搜索走 adapter FTS）+ sort=title|pubdate|last_sync 与 order=asc|desc 排序 + limit/offset 分页；管理端为状态 Tab+下载筛选+表头点击排序+分页 UI）** + 详情（含 source、stats_history）+ **comments / danmaku 明细（消失审计）** + **videos/{bvid}/card（预览卡片 PNG，?refresh=1 重渲 / ?download=1 下载）** + archive / download / similar（向量余弦）/ **comments/search（评论全文检索富结果：q/status/limit → {query,total,hits}，hit 含评论本体+video_title/owner_name 联表；FTS 索引由采集轮末尾 HealCommentsFTS 批量补齐（幂等，评论逐条 upsert 不做索引存在性检查——FTS5 UNINDEXED 列全扫是平方级开销））** / jobs + retry / sync-now / config / login/qr(+poll) / **login/status（当前 B 站账号：nav 解析 mid/uname/level/大会员）+ login/avatar（头像服务端代理，绕图床防盗链）+ login/logout（登出并覆盖本机 cookie 文件；切换账号=登出+重出二维码）** / plugins CRUD / media/rebuild（per-lib 统计）/ **libraries CRUD** / **images/backfill（全库封面/头像补档，fetch_images 任务）** / **levels/backfill（等级补档：快照回填+UP主等级，fetch_levels 任务）** / **tracks（跟踪列表：`{total,counts,items}`，state 过滤 + q 模糊匹配标题/UP主/bvid + limit/offset 分页，items 联 videos/sources 含 source_title；管理端为状态 Tab+搜索+分页 UI）**。热门视频手动补档（/add 视频类型与重同步）先返回 `need_confirm`（阈值 `collector.hot_video_views`/`hot_video_replies`，默认 100万播放/1万评论，0=禁用），前端确认后带 `confirm_hot` 重提；触发器/调度等自动化路径不打扰。 / **media-servers/status（第三方媒体库在线探测：config `media.servers` 配 name/type/url，type=teriteri/emby/jellyfin/plex/kodi，按类型探健康端点，任意 HTTP 响应即在线，3s 超时）** / **system/res（系统资源快照：internal/sysres + gopsutil——CPU 总利用率/进程 CPU（全核归一）/进程 RSS/宿主内存/网络累计字节 + TopProcesses 按 CPU 前 8；总览「资源监控」2s 轮询：CPU 与内存双 donut（进程段+系统其余段）、网络速率 60s 滚动面积图（前端差分）、进程排行）。管理 token 可选（`admin.token` → Bearer）。

**config GET/PATCH 与设置页**：`GET /api/v1/config` 返回全部运行参数（含 login 态）；`PATCH /api/v1/config` 接收指针字段子集（采集 8 项/下载 8 项含 qualities 白名单/媒体 enabled+layout+link_mode+public_url/min_req_ms/teriteri_enabled/frontend_url），**先全量校验后提交**（非法值 400 不落内存），Normalize+Validate 后经 `config.SaveConfigFile` 写回 `-config` 指向的 YAML（原文件备份 `.bak`；注释会丢失，已知取舍；0600 权限）。组件均持 `*config.Config` 活读取，多数项保存即生效；`min_req_ms`/`teriteri_enabled`/worker 数等启动捕获项重启完全生效（UI 以 † 标注）。调度器同样活读取 Cfg（间隔/统计周期/adaptive 调整下一 tick 生效）。管理端「设置」页分组表单（采集与补档/下载/媒体库与站点/只读信息），data_dir/listen/media_root/plugins_dir 等路径类只读展示、引导改 YAML。

`GET /api/v1/system/res` 另返回 `storage`：`DataDir` 所在文件系统的 `total/used/free/used_percent`，不可访问时为 null；sysres/storage.go 直接读取文件系统容量，十秒缓存。`archive_storage` 返回补档文件大小与后台扫描状态：sysres/archive_storage.go 仅遍历 media/raw/derived 和 SQLite 主库/WAL/SHM，按文件逻辑字节统计视频（含下载分段缓存）/其他数据/资源库总量，不读取内容、不跟随符号链接、不扫描配置媒体库输出、站点依赖、插件程序或日志。首次/十分钟到期后台单飞扫描，HTTP 不等待；刷新保留前值，路径变化丢弃旧结果，部分失败明确标记。首页分段环形饼图展示视频/其他补档数据/磁盘其他占用/可用；资源库=视频+其他数据、总已用是汇总值，不重复入饼。图形按本盘总量绘制；文件逻辑大小超过本盘已用时只缩放图形并提示，保留真实数值。灰色余项包含保留空间。首页扁平，700ms 分段过渡、90% 中心提示色与手机紧凑排版保留。

补档大小扫描使用256项流式目录读取，不排序整个目录；初次统计每秒返回`provisional=true`的已确认下界与`files`进度，完成后转完整值。刷新已有缓存时保持上次完整结果，避免因扫描从头开始而让数值跳回零。

网络曲线使用独立 `process_network`（scope=process、instance、recv_total、sent_total、unix_ms），来自 internal/metrics/network.go 的进程 TCP 连接 Read/Write 原子计数：CLI 启动时包装默认 HTTP Transport，app 装配包装 CDN 自定义 Transport，主服务与 teriteri 两个端口包装 Listener。统计 API、图片、视频下载和静态/API/媒体响应的收发字节，包含 HTTP 头、压缩传输与 TLS 记录，不含 DNS/UDP、子进程或其他程序的流量；旧 snapshot 整机累计与 /live 下载/播放 payload 指标保持原口径兼容。前端按进程独立时戳做差分，实例变更/累计回退/缺失重建基线，不回退整机计数，网络保留10秒窗口与800ms平滑绘图（CPU/内存仍60秒）。无后台抓包线程/新依赖/配置，计数不读取磁盘 I/O。

## 8. teriteri 适配契约（读源码核实）

管理端视频详情返回 `file_links`，逐分P给出绝对归档路径、存在状态、本机定位入口与媒体库播放入口。路径以代码块/一键复制展示；播放以总览同款品牌SVG图标list展示，管理页不内嵌播放器。teriteri直达`/video/{bvid}[-pN]`，未配置且未托管真实 teriteri-client 时不生成 teriteri 播放链接，个人页明确提示管理员配置；内置 /static/teriteri.html 仅是独立兜底播放器，不能作为 teriteri 接入或验收；重复配置去重，LAN访问把托管链接的loopback主机改为当前访问主机并保留端口。库目录列表也支持完整路径复制与本机打开。`POST videos/{bvid}/local-open` 和 `POST libraries/{id}/local-open` 仅接受已鉴权管理员、直接loopback连接、本机Host与同源Origin/自定义头；路径只从已有视频/分P/库配置解析，不接受任意路径或shell命令，归档路径防目录穿越及符号链接越界。Windows使用Explorer定位文件/打开目录，其余平台使用系统文件管理器。

Emby/Jellyfin入口 `GET videos/{bvid}/play/{server}?page=N` 先用只读Items API匹配BVID/provider/path与分P，零匹配或多匹配明确报错，禁止按相似标题误播；正向结果30秒/最多256项缓存，查找有时限及响应大小上限。播放新标签通过受管理端鉴权的流转发，支持Range/206/拖动；可另开精确媒体库条目页。既有`media.servers[].url`可附`?api_key=...`供服务端认证，凭据只通过上游请求头发送，GET config/状态/页面链接脱敏，不转发管理会话Cookie或Bearer，也不跟随带凭据的重定向。直接流不做转码，依赖浏览器支持原文件编码；真实未接入的实例不能由模拟验收推定可用。

axios `baseURL:/api`，包装 `{code,message,data}`，JWT（`localStorage.teri_token`），未登录响应头 `Message: not login`。已实现：getone（videoUrl→/media、tags 以 `\r\n` 连接、官方AI小结附加简介）/ random+cumulative 推荐 / danmu-list（timePoint 秒）/ comment/get（分页，消失评论注入`【已消失·仅存档】`前缀，现存优先排序）/ reply/get-more / search(FTS) / login / personal/info / msg-unread / WS 空实现。**封面/头像 URL 一律本地 `/media/...` 优先（images 包落盘产物），缺失才回退 B 站图床原始 URL**（图床对第三方 Referer 403 防盗链，原始 URL 在前端不可用）。**用户等级徽章**：前端按 `VLevel(handleLevel(user.exp))` 渲染，适配层把 B 站等级映射为经验下限写入 exp（levels.LevelExp），评论者等级随 reply 采集、UP主等级由 fetch_levels 拉取（需登录）。前端部署：compose 的 web 服务（nginx 反代 /api /media /ws）。

**2026-10-23 teriteri 入口纠正**：个人 watch_url 与媒体入口只指向真实 teriteri-client（显式 frontend_url、已运行托管实例或已配置的 teriteri 媒体服务器）。三者均缺失返回空链接并提示配置，禁止静默回退到 bili-saver 内置播放站。本地验证使用现有 teriteri-client dist 的隔离副本，真实 Vue `/video/{bvid}`、同源适配 API 和 Range 媒体流均通过；游客可观看其他补档用户的视频，账号仍独立。

## 9. 插件协议

`data/plugins/<dir>/manifest.yaml`：id/version/entry/events/timeout_sec/config/env。事件写一行 JSON 到 stdin（含 payload+config+context.data_dir/api_base/token）；插件 stdout 每行一个 op：`save_summary|save_subtitle|save_tag|save_relation|save_embedding|log`。事件：video.archived/discovered/downloaded/gone、subtitle.missing、comments.resynced、daily.tick、search.query。核心侧异步派发（EmitAsync），短生命周期 CLI 退出前 `Bus.Wait()`。

## 10. 部署

- 二进制：`go build ./cmd/bili-saver`；五平台交叉编译已验证（linux amd64/arm64、macOS 两架构、windows）。
- Docker：`deploy/docker-compose.yml`（core:8080 含 ffmpeg；web:8081 可选 teriteri 前端）。数据单卷 `/data`。
- 媒体库：Emby/Jellyfin/Kodi 指向各库 root（NFO 只含 origin 数据）；Plex 靠命名约定。

## 11. 已知边界（诚实声明）

- 消失检测只覆盖**曾经抓到过**的内容；已 404 视频无法补抓（本质是"趁还在先存"）。
- 未登录：评论接口不完整（消失对账自动跳过）；UP 主投稿列表基本必被风控（space 源需登录）；清晰度上限 1080P。
- list.so 弹幕上限约 1200 条（seg.so 可用时不受限）。
- Docker 构建未在本机实测（无 Docker 环境）。
- Linux 运行时已于 2026-09-08 在 WSL2 Debian 实测：linux/amd64 全部 16 个测试包通过；真实服务端到端（登录态→归档→下载→ffmpeg 合并→媒体库 movie 布局）跑通。注意：ffmpeg 是运行硬依赖（Docker 镜像已含），裸机 Linux 需自行安装；`-config` 指向的文件必须存在（compose 用户需先把 config.yaml 放入挂载卷）。

## 12. 规划（按优先级）

**近期**
- 视频级"下次同步"列：等存量视频补齐 source_id 关联后加入归档项目表
- 采集端存储视频分区（tname）→ 开放媒体库分区路由 + collection 布局（收藏夹↔视频关联）
- SESSDATA 自动刷新（refresh_token 已存未用）；快照保留策略（可配置，默认永不删）
- ~~评论全文搜索 UI~~（✅ 2026-09-09 完成：管理端「搜索」页 + /comments/search 富结果；顺带修复 comments_fts 零接线缺口并回填存量索引，见 §7）；~~统计历史明细表~~（✅ 2026-09-09 完成：视频详情「统计曲线」区快照明细展开/收起）

**中期**
- 官方插件补齐：relation-graph（关系网，表+op 已就绪）、auto-organize（自动归类虚拟合集）
- ~~card 预览卡片功能收尾~~（✅ 2026-09-08 完成，见 §4/§7）
- 播放站（teriteri.html）与管理端组件级统一

**验证基线**：`go vet ./... && go test ./internal/...`（全部 internal 包绿；包数随功能增长，以 `go list ./internal/...` 实时为准）。

## 后端性能维护（2026-10-02）

快照持久化独立为 collector/snapshot.go：同接口相同正文复用 SHA-256 内容寻址 gzip 文件，每次抓取仍保留独立索引记录，历史原件不清理。media 拆分 files.go（原子/幂等文件操作）、show.go（单轮剧集计划）、metadata.go（origin NFO），每组只排序/聚合一次，复制保留源 mtime、无变化输出跳写。基准、回归与剩余审计项见 [性能报告](docs/performance/2026-10-02-backend.md)。

维护源码的完整验收入口为 powershell -File scripts/verify-backend.ps1（加 -Bench 可复跑基准）；它显式覆盖 internal 与 cmd，避免根目录递归包模式扫描运行库与前端依赖。

第二轮重点优化视频任务：jobs/pending_queue.go 按优先级分桶 FIFO，仅优先级入堆；store/job_queue.go 原子查重入队与轻量恢复；store/sync_batch.go 批量源状态/活跃度读取与窄统计更新；collector/video_gate.go 同视频可取消互斥；jobs/source_backoff.go 按 tick 复用退避状态；app/video_recovery.go 在轻量检查确认恢复后立即排发到期部分。修复跳过评论时 Result.Complete 未同步导致的错误核对入口，保留现有登录/分页/异常确认防御。基准与回归详见 [视频任务性能报告](docs/performance/2026-10-02-tasks.md)。

第三轮执行优化：jobs/video_dispatch.go 与 resumed_queue.go 在排发前协调 archive/download/touch_stats 的视频执行权，等待同视频任务不占 worker，释放后保留优先级与原 FIFO；progress.go 合并 500ms 内进度写入并拒绝终态后的迟到回调。store/danmaku_export.go 与 collector/danmaku_export.go 在一致读快照中窄字段流式输出，fileio 仅在内容变化时原子替换。统计扫描按 rowid 游标分 400 条，媒体标签批读贯穿路由/电影及剧集 NFO；图片请求按目标路径合并，插件最多 4 个进程/16 个异步在途事件并施加背压；system/res 一秒内复用采样。修复楼中楼限量/失败的不完整状态被主楼末页覆盖，消失检测全部防御保持。复杂度、I/O、基准与边界见 [执行性能报告](docs/performance/2026-10-03-execution.md)。

现场慢因修复：taskflow/hooks.go 将阶段进度和页边界协作执行解耦；jobs/cooperative.go 可在长评论请求之间同步处理一个源展开/轻量统计，保留父轮cursor/epoch及视频执行权、不增加worker与HTTP额度。FullResyncComments=false对已有视频限制5页主楼/10次评论请求并禁止消失核对，首次仍全量。评论预览/置顶重复按rpid去重，子页按整个楼总数判断结束并保留预览父楼关系。levels增量只请求当前作者（单飞、有界缓存与软风控冷却），插件默认UTF-8。store/comment_advice.go按最新及3/7/14/30天锚点批量取窄统计，为万条以上、长期低活跃评论自动显示7/14/30天建议；不改用户源配置和原始数据，无schema迁移。规则、成本与验收见 [慢因修复报告](docs/performance/2026-10-03-task-fix.md)。

## Archive integrity migrations (2026-11-12T15:50:08+08:00)

Schema v16 introduces ordered, checksummed migration records, rejects future schema versions, and preserves repeatable legacy baseline repairs without lowering schema versions. danmaku_sync_state scopes confirmation to BVID/CID and a distinct completed epoch; replay cannot confirm itself, and changed missing-ID sets require a new first confirmation. list.so is always partial coverage. collection_status records attempts, successful retrieval and authoritative completion separately. API responses require successful raw-body and index persistence before parsing; CC bodies also enter the snapshot pipeline. New raw bodies use fixed two-level content-hash sharding; old paths remain readable. Failed component timestamps do not advance, and archive tasks report incomplete components. No historical deletion flags are automatically changed.

### v17 — Portable assets and page projections (2026-11-12T16:09:19+08:00)

archive_assets records BVID/CID, role/origin, selected variant, requested-policy fingerprint, SHA-256, size, portable storage key and provenance. New video objects are immutable and content-addressed under media/objects. Unambiguous pre-upgrade part bindings are frozen before metadata can change; these originals are indexed in place with unknown stream quality, never assigned to changed CIDs. page_sets/current_pages define the current projection while pages retains history. Local moderation lives in video_policies and no longer changes upstream status. archivepath resolves old separators/root prefixes and rejects traversal and symlink escapes. CC keys are root-relative; per-CID generated danmaku XML lives under derived/. Players, personal completion and exports use part identity; downlisted media remains exportable. Plugin subtitle files include CID and accept only derived references. New snapshot layouts are also consumed by the levels backfill through snapshot indexes.

### v18 — Transactional settings (2026-11-12T16:29:54+08:00)

Runtime console changes are stored in runtime_settings in the same SQLite transaction as default-library output settings. Startup precedence is defaults, YAML, persisted runtime values, then environment-controlled fields. Unknown YAML and malformed environment values fail visibly; array overrides use JSON. Settings readers use immutable Config.View snapshots; only serialized Update publishes a validated, persisted candidate. settings_revision/If-Match prevents stale console saves. The API exposes desired values, effective startup values, pending_restart and environment-controlled fields. Library updates use immutable IDs and RETURNING, not LastInsertId on name conflicts; invalid routing JSON fails closed. YAML serialization uses synced temporary files and replacement and is reserved for explicit startup-file operations.

### Archive ownership and recovery

`archiveguard` owns a nonblocking OS file lock for the data root throughout an App lifetime. Maintenance commands take the same lock without starting workers or running schema migrations. Stop pre-lock-version binaries explicitly before the first upgrade. New HTTP servers drain requests for up to 15 seconds; plugin execution and installers are canceled before the store is closed.

See [archive recovery](docs/archive-recovery.md) for commands and backup/catalog boundaries.
