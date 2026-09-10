
## 2026-09-08T17:45:00Z session-agent — 自适应补档冷却（新发加速+活跃度冷却）
- 做了什么：人类交办"按视频新发布程度/评论/播放活跃度设计检测频率冷却"。设计并实现有效间隔=基准×新发系数(<24h 0.5/<7d 1/<30d 2/≥30d 4)×活跃系数(最近两统计快照 view/时+reply/天 分档 0.5/1/2/4)，clamp [基准/4, min(8×基准,14天)]；作用三处：dueParts 视频级伸缩（源级周期不变）、sync_stats_all 冷却跳过、Scheduler.freshSweep 新发 24h 内 45min touch_stats 加速。顺带修复 SyncVideoStats 不 bump last_stats_at 的时间戳失真。adaptive_sync=false 全回退。踩坑：全量 build 曾被并行 agent 的 teriteri 半成品阻塞，等 90s 后对方修复再跑门；gofmt 引入 CRLF 需重排。
- 涉及：internal/app/adaptive.go(新)+app.go(dueParts/jobSyncStatsAll)+adaptive_test.go(新6用例)；internal/jobs/jobs.go(Scheduler.Adaptive+freshSweep)；internal/store/ops_videos.go(RecentStats/FreshStatsDue)+schema.sql(idx_videos_pubdate)；internal/collector/collector.go(SyncVideoStats bump)；internal/config/config.go(AdaptiveSync)；internal/server/admin.go(handleConfig 一行)；cmd/bili-saver/main.go(接线)；config.example.yaml；ARCHITECTURE §4/§5(v8)/§6.3；CLAIMS 留言 core-agent。
- 结果/状态：验收门 gofmt+build+vet+20 包 test 全绿；服务新二进制实测 adaptive_sync=true 暴露、索引老库自升级、调度正常；claim done-review（L2：sync_stats_all 语义变化+collector 写路径，待人类抽查）。

## 2026-09-08T18:55:00Z session-agent — 管理端「设置」页 + PATCH /api/v1/config
- 做了什么：人类交办"admin ui 加 settings 页，补齐所有相关设置项"。交付：index.html「设置」页（采集与补档/下载/媒体库与站点/只读信息 四分组，† 标注重启生效项）；PATCH /api/v1/config（指针字段全可选、先校验后提交、Normalize+Validate、写回 config.yaml 自动 .bak 备份 0600）；config.SaveConfigFile；GET config 补齐 full_resync_comments/stats_resync_hours/download_danmaku/min_req_ms；Scheduler 由拷贝字段改为持 *config.Config 活读取（设置改动下一 tick 生效）；Deps.ConfigPath + App.CfgPath 由 main 注入。踩坑：①PATCH 响应复用 GET payload 时 CachedLogin 对 nil API panic → 拆出 login 字段仅 GET 附加；②期间两次被并行 agent 半成品阻塞（store 缺 import、media vet）——等自愈后重跑全量门；③ strm 功能 agent 协作并入设置页（link_mode strm + media_public_url 全链路由对方补齐）。
- 涉及：internal/server/{server.go(Deps.ConfigPath),admin.go(configPayload/handlePatchConfig/路由),patch_config_test.go(新)}；internal/config/{config.go(SaveConfigFile),config_test.go(新)}；internal/jobs/jobs.go(Scheduler.Cfg)；internal/app/app.go(CfgPath)；cmd/bili-saver/main.go(接线)；internal/server/static/index.html(设置页)；ARCHITECTURE §7。
- 结果/状态：全量门 20 包绿；服务实测 GET/PATCH/非法 400/.bak；UI 渲染+保存 toast 实测过；claim done-review（API 形状新增，待人类抽查）。

## 2026-09-08T19:45:00Z session-agent — 分区+横竖屏采集落库（schema v9）
- 做了什么：人类交办"参考对话补分区/竖屏功能"。biliapi VideoView 补解析 tid/tname_v2/dimension（Dimension 类型，rotate 90/270 宽高互换）；videos 表 v9 加 tid/tname/orientation 三列（ensureColumn 幂等）；collector 归档落库 + SyncVideoStats 顺带自愈老数据（仅新值可信时覆盖，防空值抹旧）；orientationOf 派生 horizontal/vertical（未知不猜）；media Matches 的 categories 预留位转正（按 tname 精确匹配）；UI：归档详情「分区/类型」行（tname 空回退显示 tid N）+ 媒体库路由表单加分区输入。踩坑：UpsertVideo 占位符少一个（25 列 24 个 ?，24 values for 25 columns）——既有测试大面积失败实为同一根因。
- 涉及：internal/biliapi/models.go；internal/store/{models.go,ops_videos.go,store.go(v9),ops_libraries.go,category_test.go(新)}；internal/collector/{collector.go,orientation_test.go(新)}；internal/server/static/index.html；ARCHITECTURE §4/§5。
- 结果/状态：全量门 20 包绿；真实重同步实测 DB 落值（tid=76/orientation=horizontal）+ raw 快照核对一致 + UI 透出验证。**数据面发现**：近期 120/120 快照 tname/tname_v2 均为空串（B 站已不在 view 接口返回分区名），categories 按名路由对新增数据不命中，建议后续加官方分区表映射（tid 已落库，无需重爬）——已在 CLAIMS 留言 @human/@core-agent 裁决。claim done-review。

## 2026-09-08T20:15:00Z session-agent — B站官方分区映射表 bilicats（人类裁决"引入"）
- 做了什么：v9 裁决项落地。发现 archive/catalog 分区表接口已下线、排行接口无 cookie 被 -352 风控——用项目 cookie jar 实测收割 x/web-interface/ranking/v2 各主分区，16 个库内 tid 全部获得官方现名（含翻新名：76=美食制作、173=桌游棋牌、228=人文历史，与旧记忆表不同，验证了不手写表的决定）；152（主播相关）排行榜未覆盖，宁缺毋滥留待扩展。交付 internal/bilicats（零依赖映射包，数据来源注记）：pickTname 三级兜底（tname→tname_v2→表）；store Matches 候选名匹配（落库 tname+表现名+一级名）——存量行凭 tid 立即命中无需自愈；详情 API 显示解析。端到端：按一级名"美食"建临时库路由恰好命中 tid=76 视频，测试库/输出已清理。
- 涉及：internal/bilicats/{bilicats.go,bilicats_test.go}(新)；internal/collector/collector.go(pickTname 兜底)；internal/store/ops_libraries.go(catNames+Matches)；internal/server/admin.go(handleVideoDetail 显示解析)；ARCHITECTURE §4/§5；CLAIMS 留言闭环。
- 结果/状态：全量门 20 包绿（含 bilicats 抽查用例）；服务已重启实测详情 API 返回"美食制作"；v9 claim 补记映射交付。美食库现有规则是按标签"美食教程"，未代改——人类可在媒体库 UI 一键加分区路由。

## 2026-09-08T20:40:00Z session-agent — card 渲染并发崩溃修复（服务曾被 panic 带崩）
- 做了什么：服务 17:11 崩溃（drawGlyphOver index out of range），任务 worker 无 recover 直接带死进程。定位：①card 渲染并发共享 faceCache 的 opentype Face——无内部锁，Glyph 返回的 mask 是内部复用缓冲视图，并发 DrawString 互相打空 mask（单方法加锁不够，临界区必须覆盖 Glyph→DrawMask 全程）；②jobs.Manager.process 无 recover。修复：draw.go glyphMu（包 DrawString + measureString/runeWidth/textBaseline/textH）+ jobs.process recover（panic→任务 failed）+ app.refreshCard recover（维持只告警语义）。踩坑：font.Face 接口 6 个方法（首版漏 GlyphAdvance/GlyphBounds 编译失败）；单方法 lockedFace 方案被自己的并发测试证伪后改为绘制级互斥。
- 涉及：internal/card/{draw.go,assets.go,concurrent_test.go(新)}；internal/jobs/{jobs.go,jobs_test.go(新)}；internal/app/app.go(refreshCard)。
- 结果/状态：-race 并发回归精确复现生产栈→修复后过；全量门 20 包绿；崩溃视频 BV1J6tW63EfJ 重归档 done + card.png 正常（556KB）+ 服务存活；claim done-review，已留言 card-agent/core-agent（另发现 favlist "ml" 前缀解析回归，core 区域不代改）。

## 2026-09-08T20:55:00Z session-agent — 采集页链接输入小 tips（微小改动）
- 做了什么：人类交办"针对填入链接增加小 tips"。采集页输入框下加 hint：推荐粘贴完整链接（支持 b23 短链）、收藏夹用 space 收藏夹页/medialist 链接、纯数字会被当 UP 主——均为实测确认的陷阱。
- 涉及：internal/server/static/index.html（采集页输入框 +1 行 hint）。
- 结果/状态：JS 语法过、重建重启、浏览器实测提示渲染正确。
