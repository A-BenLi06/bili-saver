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
| 存储 | SQLite（modernc.org/sqlite 纯 Go 驱动）+ FTS5 | WAL；幂等迁移（当前 **schema v8**） |
| HTTP | net/http（Go 1.22 方法路由） | 管理 UI 与 API 同端口分路由 |
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
            终态任务滚动保留最近 1000 条、每 24h 清理）
  media/    多媒体库桥：遍历 media_libraries，按 MatchRules(tags/categories=分区名；UP主预留)路由视频，
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

## 5. 数据模型（schema v8）

通用溯源列：`origin`（'bilibili' | 'bilibili:ai-conclusion' | 'plugin:{id}' | ...）。

| 表 | 要点 |
| --- | --- |
| `sources` | 监控源；`sync_mode`(once/interval)、主间隔 + 评论/弹幕/字幕分类型间隔（0=跟随主间隔，-1=从不） |
| `videos` | 主记录；`status`(ok/gone/private)、分类型 `last_stats/comments/danmaku/subtitles_at`、`source_id`(v5 源关联)、`comments_anomaly`/`danmaku_anomaly`(消失防御计数) |
| `pages` | 分P；cid 变化检测的比对基础 |
| `comments` | `rpid` PK；`first_seen_at/last_seen_at/status/deleted_at` 消失语义；楼中楼靠 root/parent |
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

**space_posts（v10）**：UP主动态/图文（专栏）归档（origin=bilibili）。数据源=polymer 动态 feed（免 wbi；传统专栏列表接口已死，专栏经 feed 的 ARTICLE/图文类归档）。只增不删：动态消失标 deleted_at，feed 拉到底才标记、大面积消失（>50 条且 >30%）跳过（对齐弹幕防御）。每条保留官方原始 JSON（raw 列 + raw/ 快照双份）。动态视频（AV 类动态）提取 bvid 可提交归档。范围勾选 sources.space_scope：video/dynamic/dynamic_video/article/bangumi 逗号子集（bangumi=追番/追剧列表，`/x/space/bangumi/follow/list` 实测可用，kind=bangumi 归档、取关/下架可标记消失），空/`all`=全部、`none`=全关，采集页与监控源编辑均可勾选。查询端点 GET /api/v1/sources/{id}/posts。全部 `ensureColumn`/`IF NOT EXISTS` 幂等，老库启动自动升级。

## 6. 关键流程

### 6.1 归档（ArchiveVideoOpts）
view（Pages 空时用顶层 CID 兜底单 P）→ 全量快照落盘 → videos/pages/tags/官方AI小结/统计序列 → 按 CollectOptions 范围采集弹幕(seg.so，失败回退 list.so)/CC字幕/评论 → 分类型时间戳更新 → **仅本轮完整时**执行消失检测 → FTS → 事件（payload 含标题/简介/UP主/标签）。换源兼容：与库内 cid 集合对比，新增 cid 即使在范围外也强制补采弹幕字幕。

### 6.2 消失检测防御（三层，勿绕过）
1. **未登录跳过评论对账**：B 站未登录评论接口只回部分数据且伪报 is_end，缺失≠消失。
2. **两轮确认**：单轮消失占比 >30% 且 >50 条（弹幕 >100 条）视为接口截断异常，首轮只累加 anomaly 计数，连续两轮才真正标删。
3. **弹幕降级检测**：seg→list 回退时 list.so 有约 1200 条上限，消失数异常时跳过本轮。

### 6.3 调度（jobSyncSource）
展开源 → 新视频全量归档（触发补档）；已有视频按 `dueParts`（源间隔 × 分类型 last_*_at）计算到期范围，只刷新到期部分；均未到期仅 touch_stats。once 模式完成首次同步后自动停 watch。下载成功后异步重建媒体库（幂等；RebuildAllDebounced 并发合并，批量下载 N 次完成只跑一轮+补跑）。

**新视频触发器（v7）**：`sources.auto_download` 三态（1 开 / -1 关 / 0 跟随全局 `download.auto_download`，手动 sync 指定 download 参数时视为手动路径、不入跟踪列表）。触发命中→新视频归档+下载一次（下载幂等保证只下一次）并写入 `video_tracks`；jobArchive 下载成败回写跟踪状态（done/failed）。管理端「跟踪列表」页与 `GET /api/v1/tracks` 展示。语义分工（2026-09-09 UX 审查确立）：**新视频只在同步展开过程中被发现**，触发器决定"发现后是否自动下载文件"；周期同步的存在理由是评论/弹幕/字幕/统计等高频数据刷新；单视频源不涉及触发器（UI 列表显"—"、编辑表单隐藏）。`/add` 的 download 勾选即源级触发器初值（勾=1/不勾=-1），避免跟随全局默认开造成"本次不下载、日后新视频仍被下载"的陷阱。

**自适应补档冷却（v8，`collector.adaptive_sync`，默认开）**：视频级检测频率随生命周期自动伸缩——有效间隔 = 基准间隔 × 新发系数 × 活跃系数，clamp 到 [基准/4, min(基准×8, 14天)]（实现 `internal/app/adaptive.go`）。新发系数按 `now-pubdate`：<24h=0.5（加速）/<7d=1/<30d=2/≥30d=4（冷却），pubdate 未知取中性；活跃系数按 `video_stats_history` 最近两快照的增速：view≥100/时或 reply≥20/天=0.5（热门）/ view≥5/时或 reply≥2/天=1 / 有增长=2 / 零增长=4（沉寂），快照不足两个取中性。作用三处：① `dueParts` 按有效间隔判断 stats/comments/danmaku/subs 到期（源级周期不变，仅视频级伸缩）；② `sync_stats_all` 跳过未到有效间隔的视频（沉寂库不再每日全量刷）；③ 调度器 `freshSweep` 对发布 24h 内、统计超 45min 未刷的视频直接提交 `touch_stats`（不等源周期，发布初期高频追踪）。`SyncVideoStats`（touch_stats/sync_stats_all 路径）现在会 bump `videos.last_stats_at`（v8 前不更新，统计时间戳失真）。`adaptive_sync=false` 时倍率恒 1，完全回退固定间隔旧行为。

## 7. API 面（/api/v1）

stats / add / resolve（纯解析，输入框实时识别）/ sources CRUD+sync / **videos 列表（`{total,counts,items}`：status/dl 过滤 + q 模糊匹配标题/简介/UP主/bvid（管理端语义，teriteri 站内搜索走 adapter FTS）+ sort=title|pubdate|last_sync 与 order=asc|desc 排序 + limit/offset 分页；管理端为状态 Tab+下载筛选+表头点击排序+分页 UI）** + 详情（含 source、stats_history）+ **comments / danmaku 明细（消失审计）** + **videos/{bvid}/card（预览卡片 PNG，?refresh=1 重渲 / ?download=1 下载）** + archive / download / similar（向量余弦）/ **comments/search（评论全文检索富结果：q/status/limit → {query,total,hits}，hit 含评论本体+video_title/owner_name 联表；FTS 索引由采集轮末尾 HealCommentsFTS 批量补齐（幂等，评论逐条 upsert 不做索引存在性检查——FTS5 UNINDEXED 列全扫是平方级开销））** / jobs + retry / sync-now / config / login/qr(+poll) / **login/status（当前 B 站账号：nav 解析 mid/uname/level/大会员）+ login/avatar（头像服务端代理，绕图床防盗链）+ login/logout（登出并覆盖本机 cookie 文件；切换账号=登出+重出二维码）** / plugins CRUD / media/rebuild（per-lib 统计）/ **libraries CRUD** / **images/backfill（全库封面/头像补档，fetch_images 任务）** / **levels/backfill（等级补档：快照回填+UP主等级，fetch_levels 任务）** / **tracks（跟踪列表：`{total,counts,items}`，state 过滤 + q 模糊匹配标题/UP主/bvid + limit/offset 分页，items 联 videos/sources 含 source_title；管理端为状态 Tab+搜索+分页 UI）**。热门视频手动补档（/add 视频类型与重同步）先返回 `need_confirm`（阈值 `collector.hot_video_views`/`hot_video_replies`，默认 100万播放/1万评论，0=禁用），前端确认后带 `confirm_hot` 重提；触发器/调度等自动化路径不打扰。 / **media-servers/status（第三方媒体库在线探测：config `media.servers` 配 name/type/url，type=teriteri/emby/jellyfin/plex/kodi，按类型探健康端点，任意 HTTP 响应即在线，3s 超时）**。管理 token 可选（`admin.token` → Bearer）。

**config GET/PATCH 与设置页**：`GET /api/v1/config` 返回全部运行参数（含 login 态）；`PATCH /api/v1/config` 接收指针字段子集（采集 8 项/下载 8 项含 qualities 白名单/媒体 enabled+layout+link_mode+public_url/min_req_ms/teriteri_enabled/frontend_url），**先全量校验后提交**（非法值 400 不落内存），Normalize+Validate 后经 `config.SaveConfigFile` 写回 `-config` 指向的 YAML（原文件备份 `.bak`；注释会丢失，已知取舍；0600 权限）。组件均持 `*config.Config` 活读取，多数项保存即生效；`min_req_ms`/`teriteri_enabled`/worker 数等启动捕获项重启完全生效（UI 以 † 标注）。调度器同样活读取 Cfg（间隔/统计周期/adaptive 调整下一 tick 生效）。管理端「设置」页分组表单（采集与补档/下载/媒体库与站点/只读信息），data_dir/listen/media_root/plugins_dir 等路径类只读展示、引导改 YAML。

## 8. teriteri 适配契约（读源码核实）

axios `baseURL:/api`，包装 `{code,message,data}`，JWT（`localStorage.teri_token`），未登录响应头 `Message: not login`。已实现：getone（videoUrl→/media、tags 以 `\r\n` 连接、官方AI小结附加简介）/ random+cumulative 推荐 / danmu-list（timePoint 秒）/ comment/get（分页，消失评论注入`【已消失·仅存档】`前缀，现存优先排序）/ reply/get-more / search(FTS) / login / personal/info / msg-unread / WS 空实现。**封面/头像 URL 一律本地 `/media/...` 优先（images 包落盘产物），缺失才回退 B 站图床原始 URL**（图床对第三方 Referer 403 防盗链，原始 URL 在前端不可用）。**用户等级徽章**：前端按 `VLevel(handleLevel(user.exp))` 渲染，适配层把 B 站等级映射为经验下限写入 exp（levels.LevelExp），评论者等级随 reply 采集、UP主等级由 fetch_levels 拉取（需登录）。前端部署：compose 的 web 服务（nginx 反代 /api /media /ws）。

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
