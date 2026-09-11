# Walkthrough 汇总

> 素材来源：[`raw/`](raw/) 原始记录（对汇总方只读、永不改动）。本文件由 walkthrough-agent 的每小时后台自动化整理归纳，其他 agent 请把记录写进 raw/。
> 水位：2026-09-11T20:00:42Z（= 上轮处理时刻的真实 UTC。**水位不用条目自报时间戳**：部分 agent 时钟偏快或把本地时间误标 UTC（coord-agent 坑台账 P-001），自报时间仅作排序参考；每轮以「自报时间戳晚于水位、且不在下方异常登记中」为准收集条目，处理完把水位推进到本轮处理时刻；另需兜底检查「上轮水位之后 mtime 有变化、但自报时间戳早于水位」的 raw 文件——已多次靠此抓到新追加条目）
> 时间戳异常登记（已归纳，勿因自报时间戳晚于水位而重复归纳）：
> - core-agent-20260908.md「热门视频补档二次确认」自报 2026-09-09T02:10:00Z，实写约 2026-09-08T17:06Z，已归纳；
> - core-agent-20260908.md「总览页第三方媒体库在线状态」自报 2026-09-09T02:55:00Z，实写约 2026-09-08T18:49Z，已归纳；
> - ui-agent-20260908.md「跟踪列表 UI/UX 优化」自报 2026-09-08T19:15:00Z，实写约 2026-09-08T18:42Z，已归纳；
> - core-agent-20260908.md「账号页当前账号信息完成」自报 2026-09-11T03:30:00Z（未来时间戳），实写约 2026-09-10T18:3xZ，已归纳；
> - core-agent-20260908.md「B站登出+切换账号完成」自报 2026-09-11T04:00:00Z（未来时间戳），实写约 2026-09-10T19:0xZ，已归纳；
> - session-agent-20260910.md「媒体库管理细化：分区/类型路由 + 路由预览」自报 2026-09-10T19:35:00Z（略超前真实时钟），本轮已归纳。

## 按主题

### walkthrough 机制搭建 — walkthrough-agent（2026-09-07T17:58:40Z，done）
- 按人类交办建立 `docs/walkthrough/` 记录体系：README 协议、`raw/` 追加式原始记录区（各 agent 只写自己的 `<owner>-<UTC日期>.md`）、WALKTHROUGH.md 汇总文件。
- 在 CLAIMS.md 登记长期 claim 并 @all 通知全员：执行任务时随手记 raw（UTC 时间戳、只增不删）；进行中工作（card、图片本地化）下一个节点补记。
- 注册每小时后台自动化：只读 raw、按水位增量整理归纳进本文件，无新增自动跳过；本轮为首轮验证，链路（raw → 汇总 → 水位推进）走通。

### 图片本地化（teriteri 封面/头像防盗链治理）— core-agent（2026-09-08T02:05:00Z → 03:39:00Z，done）
- 起因与诊断（02:05）：teriteri 全部封面/头像不显示。实测根因＝适配层直传 B 站图床原始 URL，浏览器带第三方 Referer 请求 i*.hdslb.com 被防盗链 403；另确认头像全链路从未落盘，封面仅 download 任务落盘（187/189）且适配层未使用。方案定为图床资源本地化进 origin 层 `media/`（封面沿用 `media/{bvid}/cover.jpg` 约定，头像 `media/faces/{mid}.jpg`）。
- 实现（02:20–02:25）：新建 `internal/images`（Localizer：SyncVideo/Backfill/CoverURL/FaceURL；原子落盘 .part→rename、Referer/UA 头、20ms 限速、单图 20MB 上限、失败只告警不中断）；store 新增 `ListCommentFaces(bvid)`（按 mid 去重、含已消失评论）；app.go 挂 Img 组件、archive/download 任务后 SyncVideo（非致命）、新增 fetch_images 任务类型；admin.go 新增 `POST /api/v1/images/backfill`；teriteri 适配层四处（getone/推荐卡/评论/UP主空间）URL 改写为本地优先、缺失回退 CDN，附契约测试 localurl_test.go。
- 踩坑（02:25）：整体一度 blocked——internal/card 半成品编译失败（render.go 引用未有方法、withAlpha 重复声明），按协议留言 @card-agent、不代改；card 侧恢复后解除。
- 验证与结果（03:39，自动化阶梯 3/3）：card 恢复后 17 包 `go vet`+`go test` 全绿；存量补档 10449/10449（新增 6272 文件，临时 runner 直连 data 跑完即删）；实测 getone/推荐卡/评论的封面与头像 URL 已切本地 `/media/...` 且 HTTP 200 image/jpeg。存量覆盖：封面 389/389、UP主头像 4/4、评论者头像 10926/11128（缺口＝图床 404 的注销账号，前端回退 CDN 占位）。
- 遗留注意：`media/faces` 占 1.1GB 磁盘；teriteri SPA 前端需强刷一次才能看到本地图。

### 下载提速（分段并行 + 超时治理）— perf-agent（2026-09-08T08:18:14Z，done）
- 诊断"下载慢"：主因＝任务队列积压 + 单连接约 1.5MB/s + 60s 总超时掐断大流 + 无音轨视频 merge 必失败（job #276/#667）。
- 改动：downloader 修空音轨 merge 路径；biliapi 新增 DownloadClient()（无总超时、cookie jar 共享、30s 空闲 watchdog）；实现分段并行 Range 下载（.segs 断点续传 + 稀疏预分配 + 镜像逐段重试），音视频流并行；新增配置 `download.segments`（默认 4）全链路打通；分段流量接入 metrics；config.yaml concurrency 2→4。
- 验证：实测 107MB 用时 3.3s（≈32MB/s，约为原来 20 倍），md5 与旧产物一致，无音轨视频重下成功；build+vet+test 12 包全绿（新增 segment_test 5 用例），服务已重启。

### 视频卡片收尾（新会话接管）— card-agent 新会话（2026-09-08T08:40:30Z，done）
- 接管背景：原 card-agent 会话失效，人类交办"找到进度并继续"。盘点确认其已完成 card 包本体、`GET /api/v1/videos/{bvid}/card` 端点（?refresh=1/?download=1/404 三态）、管理端 UI 接线、389 张卡片生成；最后动作是 16:13（本地）修 draw.go 背景渲染，但修复未落盘到存量卡。
- 关键发现：存量卡（11:16 生成）底部"归档信息"区出现高饱和蓝/黄/粉色带（调色板中无黄色，属旧代码 bug）；用修复后代码重渲同一视频无色带，确认修复有效。
- 本次改动：card.go loadAvatar 改为 `media/faces/{mid}.jpg` 本地优先（复用 images 包落盘）；重建二进制并重启服务；refresh=1 四并发全量重生成 389/389 张卡（连接级失败两轮重试清零）。
- 验证：17 包 build+vet+test 全绿；端点冒烟 200 / 200(download) / 404；逐像素扫描 + 目检两张样卡（无评论/有热评各一），色带消失、热评头像与排版正确。已知小瑕疵：评论折行无避头尾规则，单标点可能落行首（参考实现同行为，暂未修）。

### teriteri 前端 JSON 登录修复 — perf-agent（2026-09-08T09:05:28Z，done）
- 现象与定位：8081 前端 admin/bili-saver 登录报"账号或密码不能为空"；根因＝前端 LoginRegister.vue 用 axios.post 发 JSON，而适配层 login 只认表单（FormValue），解析结果为空。
- 修复：login 在表单为空时回退 json.Decoder 解析（username/email/password），新增 firstStr 助手；表单路径行为不变。
- 验证：8081 实测 JSON 登录 200+token、表单登录兼容、错误密码提示正确；全量 build/vet/test 绿（新增 TestLoginJSON 回归用例）；服务已重启。

### 下载质量选择（清晰度白名单 + 音频偏好链）— core-agent（2026-09-08T09:35:00Z，done）
- 需求（人类交办）：默认最高画质 / 可选全部 / 可选指定几档。
- 实现：config 新增 `download.qualities`（空=全部=最高可用；指定档全缺回落更低档；兼容旧 quality 单档上限语义）与 `download.audio_qualities`（hires/dolby/192k/132k/64k，空=标准最高，集合优先、标准链兜底）；downloader 重写选流（videoQNs/selectVideo 三级回落、audioPrefChain/selectAudio、resolveQuality 按档位动态 fnval 128(4K)/256(Hi-Res/杜比)）；biliapi Dash 模型补 dolby/flac 独立音轨字段并入候选；admin handleConfig 暴露新字段；config.example.yaml 文档化；用户 config.yaml 由 quality:"360p" 改为默认最高（allow_av1/allow_4k 开）。
- 踩坑：两处测试用例自身预期错误（mkStreams 的 120 是 AV1 需 AllowAV1；64k 精确命中被误当兜底失败）。
- 验证：全量 build+vet+test 绿（新增/重写 TestSelectVideoQualitySet、TestResolveQuality、TestSelectAudio）；端到端实测重下 2 视频：360p→720p（未登录无 SESSDATA 时 720p 即最高可用，登录后自动取 1080p+）、音轨保留、无音轨源不回归。
- 注意：并行 agent 17:29（本地时间）再次重建并重启了服务，当前二进制已含本功能（经 /api/v1/config 验证）。

### B站用户等级 + UID 拉取展示（schema v6，含风控误判更正）— core-agent（2026-09-08T10:05:00Z → 12:05:00Z，done）
- 需求（人类交办）："缺失拉取任务的 b站uid、b站等级"。核实：UID 全链路本就有（member_mid/owner_mid/适配层 uid），等级从未采集。
- 实现（10:05）：biliapi reply Member 解析 level_info，新增 SpaceAccInfo（wbi+buvid+空间 Referer）；collector 入库 MemberLevel；schema v6（comments.member_level + videos.owner_level，ensureColumn 幂等，迁移历史已记）；新建 `internal/levels` 包——CommentLevelsFromSnapshots 从 raw 评论快照离线回填存量、OwnerLevels 走 space/acc/info 拉UP主等级（无 SESSDATA 自动跳过）、LevelExp 按前端契约映射；fetch_levels 任务 + `POST /api/v1/levels/backfill` + 归档钩子 EnsureOwnerLevel；teriteri 适配层 tUser.Exp=等级经验下限驱动 VLevel 徽章（前端经验边界 50/200/1500/4500/10800/28800 实证自 teriteri-client 源码）。
- 踩坑与更正（12:05）：初报"UP主等级更新=5"实为误判——B 站对登录态失效/风控场景返回 code=0 但剥离 level_info 的降级数据，旧代码把缺失字段当 Lv0 写库并计成功。修复：OwnerLevels 登录门槛从 HasCookie(SESSDATA) 改为 CachedLogin（nav 真值校验，5min 缓存）；SpaceAccInfo.LevelInfo 改指针，缺失即判软风控——拒绝写库、停止本轮并提示重新扫码；fetch_levels #810 实测正确识别软风控、未写脏数据。
- 验证：14→15 包 build+vet+test 全绿；线上 fetch_levels #809 回填评论等级 24489/24644（99.3%，Lv2-Lv6 分布合理），commentGet 实测返回真实 uid+exp（Lv6=28800/Lv4=4500 正确）。
- 遗留与观察：当前登录态对 space 接口仍被软屏蔽（评论等级不受影响，99.3% 已持久化），UP主等级待 B 站解除软屏蔽或重新扫码后重跑 backfill 补齐；另观察到 cookies.json 的 SESSDATA 曾出现又消失，疑似多个并行实例互相覆盖 cookie 文件——core-agent 自留后续项：考虑 cookie 文件写入加锁或单实例约束。

### 新视频触发器 + 跟踪列表（schema v7）— core-agent（2026-09-08T13:55:00Z，done）
- 需求（人类交办）：up主/合集/收藏夹触发器，检测到新视频自动下载一次并加入跟踪列表。
- 实现：schema v7——`sources.auto_download` 三态（1 开 / -1 关 / 0 跟随全局 `download.auto_download`，对全部源类型生效）+ `video_tracks` 表；app.go sync_source 判定新视频后按 triggerOn 命中即归档+下载一次（下载幂等天然保证"只下一次"）并 TrackAdd；手动 sync 带 download 参数视为手动路径、不入列；jobArchive 按下载成败 TrackSetState 回写（done/failed，无记录空操作）；admin 新增 PATCH /sources/{id} 接受 auto_download（校验 -1/0/1）与 GET /api/v1/tracks（联 videos 补标题/UP主/落盘态）；index.html 监控源表加"新视频触发器"列、设置对话框三态选择、新增"跟踪列表"导航页。
- 验证：15 包 build+vet+test 全绿（triggerOn 三态、Track 生命周期/幂等/联表用例；inline JS node --check 过）；线上实测：PATCH 源配置持久化✓、手动 /add 路径不入列✓、触发源手动同步后 track 行出现（queued）✓；归档任务因热门视频评论全量翻页仍在进行，完成后自动转 done（状态回写有单测覆盖）。

### 评论全文搜索（管理端「搜索」页，含 FTS 零接线缺口修复）— ui-agent（2026-09-08T16:35:00Z → 16:50:00Z，done-review）
- 交付（16:35，认领 ARCHITECTURE §12 近期项）：store 新增只读 `SearchCommentsFull`（comments_fts⋈comments⋈videos，status 过滤 / ctime 倒序 / limit≤200，返回 CommentHit=Comment+video_title+owner_name，不带 raw_json）；admin `GET /api/v1/comments/search` 响应由 `{rpids}` 升级为 `{query,total,hits}`（全仓确认无既有消费方，L2）；index.html 新增侧栏「搜索」页——关键词前缀匹配+三态状态过滤（全部/仅现存/仅已消失）、关键词高亮、消失评论删除线+消失时间、来源视频联表可点回详情，详情页返回链接按来源智能显示「← 搜索」；视频详情「统计曲线」区新增快照明细表（8 列全量指标，可展开/收起）。
- 踩坑与修复（16:50）：API 断言时发现任意关键词零命中，查库证实 comments_fts 为 0 行（3.2 万存量从未建索引）——`ReindexCommentFTS` 全仓零调用，搜索端点自诞生即空转。修复两步：UpsertComment 追加 best-effort 索引维护（从库内真值取 content、幂等、派生层失败不阻断归档、下一轮重同步自愈）；一次性 SQL 回填存量 0→32490 行（origin 数据零改动，与服务运行并发安全）。未动 core 的 ReindexCommentFTS（保留供未来显式重建），已在 CLAIMS 留言 core-agent。
- 教训（raw 原文收录）：FTS 这类"影子索引"没有接线断言——端点+表+方法齐备≠链路通，交付搜索功能必须先用真实数据断言命中。
- 验证：验收门全绿（gofmt+build+vet+20 包 test，新增 TestCommentSearchFull 覆盖接线/联表/过滤/幂等）；服务重启后实测搜索"感谢"命中 28 条、deleted 过滤、空关键词/limit 边界均通过；浏览器截图验证高亮、消失灰显、快照明细与智能返回链接。claim 置 done-review（API 形状变化，待人类晨间抽查）。

### 自适应补档冷却（新发加速 + 活跃度冷却，schema v8）— session-agent（2026-09-08T17:45:00Z，done-review）
- 需求（人类交办）：按视频新发布程度/评论/播放活跃度设计检测频率冷却。
- 设计与实现：有效间隔＝基准 × 新发系数（<24h 0.5 / <7d 1 / <30d 2 / ≥30d 4）× 活跃系数（最近两统计快照 view/时 + reply/天 分档 0.5/1/2/4），clamp 到 [基准/4, min(8×基准, 14天)]；作用于三处——dueParts 视频级伸缩（源级周期不变）、sync_stats_all 冷却跳过、Scheduler.freshSweep 新发 24h 内 45min touch_stats 加速；顺带修复 SyncVideoStats 不 bump last_stats_at 的时间戳失真；`adaptive_sync=false` 全回退；schema v8（idx_videos_pubdate，老库自升级）。
- 踩坑：全量 build 曾被并行 agent 的 teriteri 半成品阻塞（等 90s 对方修复后过门）；gofmt 引入 CRLF 需重排。
- 验证：gofmt+build+vet+20 包 test 全绿（新增 adaptive_test 6 用例）；新二进制实测 adaptive_sync 暴露、调度正常。claim done-review（L2：sync_stats_all 语义变化 + collector 写路径，待人类抽查）。

### 全平台兼容性审计 + Linux 实测验证 — perf-agent（2026-09-08T18:01:09Z，done，无代码改动）
- 审计结论：纯 Go 无 CGO（modernc/sqlite）、零平台分支代码（仅 teriteriweb npm runner 一处 GOOS 判断）、信号处理可移植、路径全 filepath/URL 用正斜杠、linkFile 三级降级链设计正确。
- 实测：五平台交叉编译全过；linux/amd64 下 16 个测试二进制在 WSL2 Debian 全绿；真实服务端到端跑通（登录态跨平台复用 → 归档 → 下载 166830B 视频流 → ffmpeg 合并 mp4 → 媒体库 movie 布局落盘）。
- 注意点：Linux 部署两处前提——ffmpeg 硬依赖、-config 文件必须存在——已写入 ARCHITECTURE §11；中途一个"失败"实为 WSL 未装 ffmpeg（错误信息按设计可读）。

### 触发器语义 UX 审查修正（采集/监控源页 + /add 初值映射）— ui-agent（2026-09-08T18:20:00Z → 18:40:00Z，done-review）
- 背景与发现（18:20，人类启示"触发器接管新视频发现，周期同步只剩评论/播放量等高频刷新"）：审查出四处语义错位，最关键是一处行为陷阱——/add 的"下载视频文件"勾选只影响首次同步参数，源级 AutoDownload 恒为 0（跟随全局、默认开），用户不勾下载、日后新视频仍会被自动下载。修复：handleAdd 中 req.Download 非空时写入 src.AutoDownload（勾=1/不勾=-1）。
- 其余修正：采集页下载勾选按类型动态命名（视频=下载视频文件；源=新视频自动下载（触发器））、"触发补档"文案改"持续监控"、动态 syncHint 把间隔重新定义为"评论/弹幕/字幕/统计的刷新频率"、仅一次+非视频警告"停止监控/不再发现新视频"；监控源页列头改"刷新间隔"、触发器列对 video 显"—"、跟随全局解析显示当前全局态、编辑表单 video 隐藏触发器字段且 saveSrc 不发 auto_download；兼容 core-agent 刚落地的热门视频 need_confirm 确认流（重读 index.html 后锚定，未触碰其逻辑）。
- 过程踩坑（18:20→18:40 更正）：全量门两度被并行 agent 的 teriteri site.go 半成品阻塞（wsDanmu 重复声明等）——按包验证 + 服务重启暂缓至对方收敛（避免用含他人 WIP 的二进制顶掉健康实例）；等待窗口内对方修复后复跑全量门 20 包全绿，重建重启上线，/add 两态映射实测通过（不勾→-1、勾→1，测试源已删）。
- 观察：期间 deleted 评论 +956（夜间存量重同步消化中，防御机制在位，已提请 core-agent 留意）。

### 热门视频补档二次确认 — core-agent（2026-09-09T02:10:00Z，done）
- 需求（人类交办）：热门视频补档前先警告反馈，需要再次确认。
- 实现：config 新增 `collector.hot_video_views`（默认 100万）/ `hot_video_replies`（默认 1万），0=禁用；/add（实时 view 接口判定）与 /videos/{bvid}/archive（库内最新 stats 判定，重同步重拉评论同样耗时）未确认时返回 200+`{need_confirm,reason,view,reply}` 不入队——特意用 200 而非 4xx（前端 api() 助手错误路径只透传 error 字符串、会丢结构体标记）；前端采集页与归档项目页"重同步"按钮确认后带 confirm_hot 重提；自动化路径（触发器/调度/触摸统计）不打扰。
- 验证：16 包全绿（hotReason 边界/禁用用例；inline JS node --check 过）；线上三路径实测——LPL 热门视频（175.9万播放/1.1万评论）重同步未确认→need_confirm✓、普通视频 confirm_hot→直接入队✓、/add 阴阳师CG（219万播放）未确认→need_confirm 不入队✓。

### 协调巡检体系与经验库 — coord-agent（2026-09-08T18:08Z → 18:24Z，生效）
- 首轮巡检（人类交办协调角色）：建 `docs/coordination/PITFALLS.md` 坑台账 8 条，含时钟漂移实证——ui/session 交接时间比文件 mtime 快约 1h、疑共同 TZ 来源（本轮 core 条目再现同症，且出现本地时间误标 UTC 的"未来时间戳"）；修 ARCHITECTURE.md 陈旧数字 ×3（schema v5→v8、17→20 包）、AGENTS.md 同步；COORDINATION §4 加时间戳实测条款、§5 加按包验证条款。
- 职责升级（人类明确分工：walkthrough 管流水记录、coord 管经验提炼与运用）：建 `docs/coordination/PLAYBOOK.md` 经验库——四层知识结构（raw 流水→PITFALLS 证据→COORDINATION 条款→PLAYBOOK 方法论），经验 E1-E10 各含原理/做法/证据/迁移注意；巡检流程加"经验提炼"步骤（模式重复出现第二次才提炼）；巡检窗口收窄为仅夜间（UTC+8 23:00–9:00）每 15min。
- 门禁现状：19/20 包绿，唯一 FAIL＝session-agent 设置页 WIP 的 TestPatchConfig（nil Client panic），已 @all 预警勿代改；两次共享文件并行写入均按 §3 重读后重做，零冲突。

### git 管理体系搭建 — walkthrough-agent（2026-09-08T18:22:46Z → 18:35:00Z，done）
- 人类交办"除 walkthrough 外增加 git 管理"。建仓（分支 main）+ 基线提交 `7a94910`（130 文件：源码/文档/walkthrough 记录；不含 data/ 6.8GB、exe、日志、本机 config.yaml、astrbot 独立仓库）；补 .gitignore（`*.exe~`——原规则漏掉的 37M 备份二进制、`*.log`、`tmp/`、根目录截图）+ 新建 .gitattributes（仓库统一 LF）。
- 协作规则（CLAIMS @all 广播）：全仓 git 操作统一走 walkthrough-agent，其他 agent 只管写文件、不碰 .git；仓库无 remote，push 属 L3 不执行。
- 踩坑：定时任务会话内不能 CronCreate 新自动化 → 快照提交经 CronUpdate 并入现有 walkthrough 自动化（两段式提示词，与汇总同轮执行）。

### 跟踪列表 UI/UX 优化 — ui-agent（自报 2026-09-08T19:15:00Z，约实写 18:42Z，done-review）
- store 新增 TrackStateCounts + ListTracksPage（state 过滤、q 对标题/UP主/bvid 的 LIKE 模糊匹配带转义、limit≤100·offset 分页、返回过滤后总数；联 sources 补 source_title；Track 结构体 additive 加 SourceTitle；旧 ListTracks 被 core 测试引用原样保留）；handleTracks 响应由裸数组升级 `{total,counts,items}`（唯一消费方为管理端，L2）。
- 前端 renderTracks 重写：状态 Tab（全部/已下载/排队中/失败，圆点+计数徽标）、搜索框、分页条（单页隐藏）、来源列显示源名替代 #id、失败原因灰字、空态文案、「清除筛选」动态显隐——首版只在整页渲染时算可见性，自检发现搜索后不出现，改为 loadTk 内按状态切换。
- 验证：20 包全绿（期间被并行设置页 WIP 短暂卡编译，等待收敛后复绿）；临时 24 行数据断言（分页无重叠/状态过滤/三种搜索/LIKE 转义防注入/counts 一致，建后即清）；DOM 断言交互 + 截图（后续截图通道故障，以 DOM 断言补位）。

### 总览页第三方媒体库在线状态 — core-agent（自报 2026-09-09T02:55:00Z，约实写 2026-09-08T18:49Z，done）
- config 增 `media.servers` 列表（name/type/url）+ MediaServer 类型；新端点 `GET /api/v1/media-servers/status` 并发探测（3s 超时）：emby/jellyfin→/System/Info/Public、plex→/identity、kodi/teriteri→根路径，收到任意 HTTP 响应（含 401/403 鉴权）即判在线，note 带状态码；teriteri 未配置但适配启用时自动显示"内置适配（随核心进程运行）"；总览页新增"第三方媒体库在线"区块。
- 踩坑（多 agent 竞争写 config.yaml）：写入的 servers 条目被并行 agent 的"设置页保存 config"整文件重写覆盖（yaml Marshal 回写丢注释与条目）——config.yaml 与 cookies.json 同属多实例互相覆盖风险，已在留言区提醒；重加条目后验证通过。teriteri 前端已由 teriteriweb 核心自托管方案接管（client_addr :8081）。
- 验证：17 包全绿（类型健康路径/401 在线判定/离线与空地址用例）；实测 teriteri 前端 HTTP 200 / 57ms 在线。

### 管理端「设置」页 + PATCH /api/v1/config — session-agent（自报 2026-09-08T18:55:00Z，done-review）
- 交付：index.html「设置」页（采集与补档/下载/媒体库与站点/只读信息 四分组，† 标注重启生效项）；`PATCH /api/v1/config`（指针字段全可选、先校验后提交、Normalize+Validate、写回 config.yaml 自动 .bak 备份 0600）；config.SaveConfigFile；GET config 补齐 full_resync_comments/stats_resync_hours/download_danmaku/min_req_ms；Scheduler 由拷贝字段改为持 *config.Config 活读取（设置改动下一 tick 生效）；Deps.ConfigPath + App.CfgPath 由 main 注入。
- 踩坑：PATCH 响应复用 GET payload 时 CachedLogin 对 nil API panic → 拆出 login 字段仅 GET 附加；期间两次被并行 agent 半成品阻塞（store 缺 import、media vet），等自愈后重跑全量门；strm 功能由协作 agent 并入设置页（link_mode strm + media_public_url 全链路）。
- 验证：全量门 20 包绿；服务实测 GET/PATCH/非法 400/.bak；UI 渲染+保存 toast 实测过。done-review（API 形状新增，待人类抽查）。

### 分区+横竖屏采集落库与官方分区表映射（schema v9 + bilicats）— session-agent（自报 2026-09-08T19:45Z → 20:15Z，done-review）
- 采集落库（人类交办"补分区/竖屏功能"）：biliapi VideoView 补解析 tid/tname_v2/dimension（rotate 90/270 宽高互换）；videos 表 v9 加 tid/tname/orientation 三列（ensureColumn 幂等）；collector 归档落库 + SyncVideoStats 顺带自愈老数据（仅新值可信时覆盖，防空值抹旧）；orientationOf 派生 horizontal/vertical（未知不猜）；media Matches 的 categories 预留位转正（按 tname 匹配）；UI 归档详情加「分区/类型」行（tname 空回退显示 tid）+ 媒体库路由表单加分区输入。
- 数据面发现：近期 120/120 快照 tname/tname_v2 均为空串——B 站已不在 view 接口返回分区名，按名路由对新增数据不命中；tid 已落库无需重爬，留言 @human/@core-agent 裁决。
- 官方分区表（人类裁决"引入"）：archive/catalog 分区表接口已下线、排行接口无 cookie 被 -352 风控——用项目 cookie jar 实测收割 ranking/v2 各主分区，16 个库内 tid 全部获得官方现名（含翻新名 76=美食制作、173=桌游棋牌、228=人文历史，验证了"不手写旧记忆表"的决定）；152（主播相关）未覆盖，宁缺毋滥留待扩展。交付 `internal/bilicats` 零依赖映射包：pickTname 三级兜底（tname→tname_v2→表）；store Matches 候选名匹配（落库 tname+表现名+一级名），存量行凭 tid 立即命中无需自愈；详情 API 显示解析。端到端：按一级名"美食"建临时库路由恰好命中 tid=76 视频（测试库已清理）。
- 踩坑：UpsertVideo 占位符少一个（25 列 24 个 ?），既有测试大面积失败实为同一根因。
- 验证：全量门 20 包绿（两轮，含 bilicats 抽查用例）；真实重同步实测 DB 落值（tid=76/orientation=horizontal）与 raw 快照核对一致 + UI 透出；服务重启后详情 API 返回"美食制作"。注：美食库现有规则是按标签"美食教程"，未代改——人类可在媒体库 UI 一键加分区路由。

### card 渲染并发崩溃修复 — session-agent（自报 2026-09-08T20:40Z，done-review）
- 事故：服务 17:11 崩溃（drawGlyphOver index out of range），任务 worker 无 recover 直接带死进程。
- 定位：card 渲染并发共享 faceCache 的 opentype Face 无内部锁——Glyph 返回的 mask 是内部复用缓冲视图，并发 DrawString 互相打空 mask；单方法加锁不够，临界区必须覆盖 Glyph→DrawMask 全程。另外 jobs.Manager.process 无 recover，panic 直接带崩进程。
- 修复：draw.go 加 glyphMu 绘制级互斥（覆盖 DrawString + measureString/runeWidth/textBaseline/textH）+ jobs.process recover（panic→任务 failed）+ app.refreshCard recover（维持只告警语义）。
- 踩坑：font.Face 接口 6 个方法，首版漏 GlyphAdvance/GlyphBounds 编译失败；"单方法 lockedFace"方案被自己的并发测试证伪后改为绘制级互斥。
- 验证：-race 并发回归精确复现生产栈、修复后通过；全量门 20 包绿；崩溃视频 BV1J6tW63EfJ 重归档 done + card.png 正常（556KB）+ 服务存活。遗留发现：favlist "ml" 前缀解析回归（core 区域，留言不代改）。

### favlist "ml" 前缀回归修复（P-009 接管）— coord-agent（2026-09-09T16:14Z，修复已落码、待重启验证）
- 背景：新版收藏夹 fid 带 `ml`（media-list）前缀导致解析回归，此前 session-agent 在 card 崩溃修复中发现并留给 core；coord-agent 接管预告（23:05）到期无人认领，按 COORDINATION §2 接管执行最小修复。
- 修复四件套：① resolver 新增 normalizeFavID 剥 ml 前缀，应用于全部 4 处 TypeFavList 构造点（space/favlist、medialist 路径、media_id、fid 兜底）+ expandFavList ParseInt 前防御性再剥；② store.migrate() 加 v9.1 幂等数据修复（UPDATE sources 剥存量 ml 前缀，NOT EXISTS 防 UNIQUE 冲突）；③ jobs.Scheduler 新增 syncSourceBlocked——sync_source 连续失败≥5 次后提交间隔按 2 的幂拉长（上限 1h，成功即清零），接入 tick 循环；④ 回归测试 ×3（resolver 剥离 / store 迁移 / jobs 退避全路径）。
- 过程快照（raw 原文）：接管预告→1h 无响应→登记接管→侦察复用 P-009 结论→一次落码。
- 状态：三包测试绿，全量门 19 包 ok（待最终确认）；按 §5 重启服务并验证 ml 同步循环停止尚待执行。

### 归档项目页 UI/UX 优化（分类/分页/排序）— ui-agent（2026-09-09T17:40:00Z，done-review）
- 人类交办"归档项目的分页、分类、排序显示（按时间/名称升降序）"。
- store 新增 VideoStatusCounts + ListVideosPage（status ok/gone/private 与 downloaded 过滤、q LIKE 匹配标题/简介/UP主/bvid 带转义、sort 白名单 title/pubdate/last_sync + order、limit≤100/offset 分页、返回过滤后总数；旧 ListVideosWithCounts 原样保留）；handleListVideos 响应裸数组→`{total,counts,items}`，同步更新 app 包 TestHTTPWiring 契约断言（自己形状变更引发的测试红，非他人区域）。
- 前端 renderVideos 重写：状态 Tab（全部/正常/已消失/私有+计数）、下载状态筛选、可点击表头排序（项目▲▼ title NOCASE / 发布 / 最后同步，新增发布列）、分页条、各组合空态、清除筛选动态显隐。
- 自检抓到竞态缺陷：快速连续操作（切 Tab 后立即搜索）时并发响应乱序覆盖 DOM——loadV/loadTk 加请求序号守卫（过期响应直接丢弃），顺带修复跟踪列表同款隐患。
- 验证：20 包全绿；服务已重启（期间被并行 agent 顶掉一次实例，最终版含全部改动）；431 条真实数据断言（默认 pubdate desc/分页/三状态过滤与 counts 一致/dl 过滤/title 升降序/组合过滤+分页/四种搜索/LIKE 转义用 SQL 真值核对——q=% 命中唯一含"99%"标题属正确语义）；DOM 断言排序切换与竞态守卫；截图验证观感。

### 媒体库状态五行恒显示 + 页面 no-cache — core-agent（自报 2026-09-09T03:20:00Z 不可信，实为本轮前新追加，done）
- 人类反馈总览页"各媒体库状态还没看到，要显示如 emby（未检测到）"。重构 mediaServerStatuses：恒定输出 teriteri/emby/jellyfin/plex/kodi 五行——未配置实例的类型输出 Detected=false + Note="未检测到"；已配置多实例聚合"在线 m/n"、单实例透传其 note；teriteri 未配置实例但适配启用时仍显示内置在线项，显式配置则不叠加。前端按行渲染 在线/离线/未检测到 三态 + 实例子行。
- 顺带修复"看不到新版 UI"的诱因之一：server.go 静态页与首页加 `Cache-Control: no-cache`（页面随二进制内嵌更新，防浏览器停留旧版）。
- 验证：server/config 包测试绿（新增五行聚合/内置不叠加/未检测到断言），inline JS 过；线上实测五行输出——teriteri 在线(HTTP 200)、其余四类"未检测到"，首页带 no-cache 头。

### 采集页链接输入 tips — session-agent（自报 2026-09-08T20:55:00Z 不可信，实为本轮前新追加，done）
- 人类交办"针对填入链接增加小 tips"：采集页输入框下加 hint——推荐粘贴完整链接（支持 b23 短链）、收藏夹用 space 收藏夹页/medialist 链接、纯数字会被当 UP 主，均为实测确认的陷阱。
- 验证：JS 语法过、重建重启、浏览器实测提示渲染正确。

### 设置页 + 归档详情页 UI 重构 — ui-agent（自报 2026-09-09T18:30:00Z，实为 2026-09-10T17:49Z 前后新追加，done-review）
- 人类交办"设置页面和归档项目的详情页 ui 太差，优化"。
- 设置页（session-agent done 区回归共享，纯布局重构、所有 input id 与 saveSettings 逻辑不动）：.fgrid 栅格（auto-fill minmax(320px,1fr)）替代 .inline 全宽混排、数字输入限宽 180px、checkbox 说明换行缩进、长文本项通栏、底部 sticky 保存条（与顶部双保存入口）、只读信息区独立标注。
- 归档详情页（openArchive 重写）：封面图头部（本地 cover.jpg 优先回退 CDN）+ .statrow 数据条（复用总览组件：播放/点赞/投币/收藏/评论+消失子标注/字幕）+ 走势图加高 36→48px + 档案 kv 瘦身（UP主/发布/状态/分区上移头部）+ 评论列表头像化（本地 faces 优先、onerror 隐藏）。
- 顺带修复两个既有缺陷：① loadCmts/loadDmAudit 无初始调用点——详情页评论区/弹幕审计永远停在"加载中…"（现渲染后主动调用）；② 两函数从 .pagehead .desc 抓 bvid 的隐性 DOM 耦合改为 window.__bvid。另绕过 /media 双前缀路由约定（URL=/media/<local_path> 且 local_path 自带 media/ 前缀），归档列表缩略图同款 CDN 403 一并改本地封面优先。
- 验证：20 包全绿；服务重启；浏览器实测设置页保存链路通、详情页封面/头像/数据条/评论区全部生效（faces 27 visible、cmtRows 100）。

### 主程序归档链路算法优化 — core-agent（2026-09-10T17:13:27Z，done）
- 人类交办全库架构评审（附实测：真实库 15 万评论，UpsertComment 逐条 FTS 存在性检查单条 116ms、平方级）后按人类指示收窄主程序。
- store：`UpsertCommentsBatch`/`UpsertDanmakuBatch`（单事务+prepared，store 层原全仓零事务）；`HealCommentsFTS()` 每采集轮一次批量补索引，移除 UpsertComment 尾部逐条补索引（采纳 ui-agent 09-09 留言预留方案，已回执）；`AddTags` 单事务；schema_meta '5'→'9'（仅信息展示，无读取方）。
- collector：评论 sink 缓冲 256 条批量落库（defer 冲刷保证消失检测前全部落库）、弹幕按段批量（total 只计实际持久化数）。
- jobs：`PruneTerminalJobs(keep)` 终态任务滚动保留（done/failed 留最近 1000 条），调度器每 24h 清理（P-009 失败循环曾刷 3000+ 条）。
- 验证：gofmt 改动文件零告警 + build + vet + 全量 19 包 test 绿（新增 4 用例）；红线零触碰（消失防御/origin-derived/旧方法签名未动）。注意：运行中服务仍为旧二进制，待下次重启重建生效。

### walkthrough/git 体系：夜窗频率提升与远端对接 — walkthrough-agent（2026-09-10T17:44:31Z → ~17:49:13Z，done）
- 夜窗频率提升（人类指令）：cron 由每小时改为**每 30 分钟**（`0,30 0-9,21,23 * * *`），夜窗 12→20 轮；白天维持 9:00/21:00（表达式附带 9:30/21:30 两个技术性空跑点）。
- 远端对接（人类指令"对接 github.com/A-BenLi06/bili-saver、每次提交都 push"）：首推前对 tracked 内容做密钥扫描（干净——ai-* 插件 api_key 全为空占位符）；直连 github.com:443 不通，探测到本机系统代理 127.0.0.1:7897 后配置仓库级 http.proxy；`push -u origin main` 一次成功（远端空仓库、认证走本机凭据管理器），全部提交已上远端；自动化规则改为"提交成功后立即 push、失败重试一次不回退本地提交、绝不 force-push"。
- 运行注意：push 依赖本机代理进程在线，离线时当轮报错下轮补推；远端若 non-fast-forward 按铁律不 merge/rebase、只报告待人类处理。

### 设置页 UX 交互机制升级（脏状态/离开拦截/结构化选择）— ui-agent（自报 2026-09-09T19:05:00Z，实为 09-10T18:3xZ 前后新追加，done）
- 人类反馈"设置页面的 UX 不够好，易于给用户造成困惑"。诊断四个结构性困惑点并逐一修复：① **静默丢改动**（最严重）——脏状态跟踪（ST_FIELDS 统一映射 + stCollect/stRefresh），改动字段标粉色 ●、顶部与保存条显示"未保存修改 N 项 · 离开页面将丢失"、导航点击 confirm 拦截 + beforeunload 兜底；② 新增"放弃更改"（顶部+保存条双入口，恢复到载入快照）；③ 保存按钮无修改时 disabled，保存成功后快照刷新回干净态；④ 画质白名单自由文本改 checkbox 组（8 画质档+5 音频档，未知存量值兼容渲染），语义提示"全不勾选=不限画质"。所有 PATCH 字段与后端契约不变。
- 验证：JS 检查过；全量门 20 包绿（期间两度被并行 WIP 卡门，等待收敛复绿未代改）；服务重启（.new→mv 缩短空窗）；浏览器 DOM 断言——初始保存禁用/改值出●+计数/放弃还原 360/脏态导航拦截/保存链路 360→362 落盘→还原 360 双向持久化；截图验证。

### UP主源扩展归档：动态/动态视频/图文专栏（schema v10）— session-agent（2026-09-10T17:05:00Z，done-review）
- 人类交办"up主归档增加所有动态与动态视频、所有专栏，设置里可勾选（默认全部）"。接口实测定型：动态 feed（polymer）免 wbi 可用且 AV/WORD/DRAW/FORWARD/ARTICLE 同源；传统专栏列表接口已死（legacy 空、opus 404）→ 专栏经 feed 的 ARTICLE/图文类归档。
- 交付：biliapi DynamicFeed（FlexInt 兼容 pub_ts 字符串）+ store v10（space_posts 只增不删 + 消失标记防御 + sources.space_scope）+ collector SyncSpaceDynamics（offset 翻页全量、拉到底才标记消失）+ ParseSpaceScope/ValidSpaceScope + jobSyncSource 接线（动态视频提取 bvid 归档去重）+ admin（/add 与 PATCH space_scope 校验、GET /sources/{id}/posts）+ UI 采集页与监控源编辑四勾选（space 类型才显示、默认全选）。
- 验证：真实 UP 主端到端 794 条动态落库 + 669 bvid 提取；PATCH 往返/非法 400/UI 勾选实测；全量门 19 包绿。已知限制：纯不发动态的老专栏暂收不到（接口层面无解，已标注）。

### 读路径优化第二轮：teriteri 评论区/卡片 + media 桥 — core-agent（2026-09-10T18:20:00Z，done）
- 人类"继续处理"评审遗留项。① teriteri commentGet 由"整视频评论含 raw_json 全量加载两遍+内存分页"改主楼 SQL 分页（ListMainCommentsPage/CountMainComments，hot 序=现存优先→赞降→ctime 稳定，与原语义逐项对齐）+ 页内楼中楼一次 IN 查询 + commentReplyMore 直查，合并分页用 segSlice 跨页；② statsOf/card 路径 LatestStats 单条替代全量历史、收藏判断改 CountFavItems 单查询（原遍历全部收藏夹×条目）、searchHot 改 TopTags 单条 GROUP BY（原 N+1）、videosByOwner 去掉重复第二次全量历史；③ media：gatherMeta 用 LatestStats、sortByID 插入排序→sort.SliceStable、tvshow.nfo 写前内容比对（消大合集平方级重写）、RebuildAll 单飞 + 下载后去抖合并；④ 顺带修复 biliapi nav money 浮点解析（Coin int→float64，TestNavAccount 由红转绿）。
- 验证：本人改动集按包验证全绿（store/adapter/media/app/jobs/biliapi/server）；全量门余两红均为 session-agent space_posts WIP 中间态，按归属纪律未代改、待其收敛复查。约束遵守：schema（site_* 索引）主动避开 session 的 v10 区域留待串行补。

### 账号页当前账号信息 — core-agent（自报 2026-09-11T03:30:00Z 未来戳，实写 09-10T18:3xZ 前后，done）
- 人类交办"账号页显示现在的帐号状态，uid，账号名，头像，等级，是否为大会员等"。biliapi 增 NavAccount（nav 解析 isLogin/mid/uname/face/level_info+exp/vipStatus/vipType/money，getJSONLenient 容忍未登录）；admin 增 `GET /api/v1/login/status`（nav 失败按未登录呈现不炸页面）与 `GET /api/v1/login/avatar`（头像服务端代理——B站图床对第三方 Referer 403 必须转发）；账号页新增"当前账号"信息卡（头像/昵称+大会员徽章/UID/等级+经验/大会员状态/硬币/刷新），扫码区下移为"重新扫码覆盖登录态"。
- 验证：全量 18 包绿（NavAccount 解析用例）；线上实测 logged_in=true BenLi06 / mid=397703470 / Lv.6 / exp 30511 / 非大会员 / 硬币 1105，头像代理 200 image/jpeg。附带确认：当前登录态有效（此前 space 软风控期的登录态问题已不存在），fetch_levels #2908 已入队待补齐全库 UP主等级（124 个 distinct）。
- 过程观察：等待期间 teriteri 适配层被并行 agent 短暂置于编译失败态（sort import 残留），约 10 分钟后对方自行修复，未代改。

### 设置页第三轮：控制化视觉（滑块/开关/分段选择器）— ui-agent（自报 2026-09-09T19:35:00Z，实为 09-10T19:0xZ 前后新追加，done）
- 人类反馈"设置页还是丑、不直观，加入滑块等元素，体验最畅快、选项最完善、不加认知负担"。① 9 个数值设置全部升级**滑块+数字联动**（range 与 number 双向同步，范围/步长按语义定制：同步间隔 0~7 天步进 15 分、统计周期 0~72h、评论上限 0~2000、热门阈值百万级步进 10 万等），标签行右侧**语义化实时值**（"6 小时/不限/禁用/100.0万/4 路"）；② 全部布尔项换 iOS 式开关（.switch）；③ 媒体库布局/链接方式从 select 换分段选择器（.seg，长解释下沉 title 悬浮）；④ 画质/音频 chips 加选中描边态。ST_FIELDS 增 seg 类型（collect/apply/dirty 全兼容），slider 联动不改 stCollect 读取路径（number 仍为真值源，yaml 超范围值兼容）；脏跟踪/放弃更改/离开拦截全部继续生效。
- 验证：JS 检查过、构建绿；服务重启；浏览器断言——滑块拖动→数字+语义标签联动+脏计数、分段/开关切换、放弃更改三类控件全还原（720→360 落盘还原实证、配置零污染）；截图确认观感。

### UP主源追加追番/追剧列表归档（v10 追加）— session-agent（2026-09-10T18:25:00Z，done-review）
- 人类交办"追番列表等也加入"。实测 /x/space/bangumi/follow/list（type=1 追番/2 追剧，免 wbi，用户关闭公开时返回空）→ biliapi SpaceFollowList（raw 逐条保留）+ collector SyncSpaceBangumi（分页到底、dtype=follow_bangumi/follow_cinema、正文=evaluate+集数）+ scope 第 5 token bangumi（Parse/Valid/全选阈值 5）+ MarkSpacePostsGone 加 kinds 作用域（动态轮与追番轮互不误标）+ UI 第 5 勾选（scopeChecked 改动态长度）+ app 接线。
- 踩坑：追加方法时 Python 锚点被注释里的弯引号破坏——改用更小锚点分步断言；错误提示漏列 bangumi 补正。
- 验证：live 实测 mid=2 落库 156 条（122 追番+34 追剧，标题/简介/raw 完整）；服务内 PATCH 五 token 往返+非法 400+UI 双勾选组全过。

### B站登出+切换账号 — core-agent（自报 2026-09-11T04:00:00Z 未来戳，实写 09-10T19:0xZ 前后，done）
- 人类交办"增加登出功能，切换账号功能"。biliapi 增 Logout（带 bili_jct csrf 通知 /x/log/web/logout，不可达兜底 /login/exit/v2，均尽力而为）+ ClearCookies（cookiejar 无删除接口，用"过期条目写回"清空，避免替换 jar 实例的并发竞态）+ 登出后 ResetLoginCache；admin 增 `POST /api/v1/login/logout`（登出后立刻 SaveCookies 覆盖本机 cookie 文件，重启不再带回旧登录态）；账号页信息卡增"退出登录/切换账号"按钮（均二次确认，切换=登出+账号页自动重出二维码）。
- 验证：全量 19 包绿（Logout 端点路径/csrf 携带/cookie 清空/登录态失效断言）；线上验证刻意不真实执行登出以免毁掉用户当前会话（GET 打 POST 路由 405 确认注册即止），登录态 BenLi06 Lv.6 保持完好，页面含"切换账号"按钮。

### 任务队列可读性 + 日志页（分级/实时/导出）— session-agent（2026-09-10T19:15:00Z，done-review）
- 人类交办两件事。① **任务队列**：/jobs 读时富化"任务来源"（sync_source→源标题带类型前缀；archive/download/touch_stats→视频标题+来自源；统计/图片/等级→系统任务名），零 schema 变更（请求内缓存防重复查）；任务页重构：类型/状态中文化（源同步/归档/排队中/执行中…）、状态筛选 tabs、running 任务显示 updated_at（可见活性）。② **日志页**：logging.Setup 改造为多路 writer（stdout + 用户文件 + 按日 data/logs/service-YYYYMMDD.log 按日切换 + 内存环形 4000 行捕获器，writer 层实现零 slog 侵入），`/api/v1/logs`（after seq 增量+level 过滤）与 `/api/v1/logs/export`（按日文件合并+时间范围+级别过滤→附件下载），UI 日志页（级别 tabs/实时刷新 2s/增量渲染上限 1200 行/日期范围导出）。
- 踩坑：admin.go 与 index.html 均被并行改动多次——python 锚点断言失败后必须重读再打；大段 JS 补丁 heredoc 与模板串打架，改走 Write 补丁文件+Python 拼接。
- 验证：全量门（并行 TestNavAccount WIP 红除外）绿 + logging 单测过；服务实测任务来源三类标签正确、日志 API/导出/按日文件全过；浏览器任务页+日志页实测。

### 媒体库管理细化：分区/类型路由 + 路由预览 — session-agent（自报 2026-09-10T19:35:00Z 略超前真实时钟，done-review）
- 人类交办"媒体库管理可详细指定分区/类型到哪个库（仅限 emby 等，teriteri 全量显示）"。MatchRules 加 orientation 数组（Matches：分区名/类型任一命中即匹配；默认库语义不变）；`GET /libraries/route-test` 路由预览（bvid 优先读真实分区/类型/标签，手动 tid+orientation 兜底，返回每个启用库命中态）；媒体库页：编辑表单加类型（横屏/竖屏）勾选、规则列显示类型、路由测试工具（输入 BV 号→显示解析属性与命中库）。pagehead 注明"仅作用于 Emby/Plex/Jellyfin/Kodi 输出；teriteri 全量显示不受影响"。
- 踩坑：UpsertMediaLibrary 系函数名记忆偏差（ListMediaLibraries 非 ListLibraries）；admin.go/index.html 持续并行漂移，锚点逐个断言。
- 验证：全量门绿（store/server/media 包）；服务实测 route-test（BV1CRt16gEyL → 美食制作/横屏/标签 → 命中默认库+美食库）。

### 设置页第四轮：撤滑块改"预设片+数字/步进器"（控件匹配决策）— ui-agent（自报 2026-09-09T19:00:00Z，实为 09-11T19:00Z 前新追加，done）
- 人类反馈"滥用滑块了，以顶级设计师决定最好的设置显示效果"。自省：滑块适合模糊调量，不适合精确取值与巨大非线性范围——热门阈值 0~500 万线性滑块无法表达"150 万"、同步间隔 673 个刻度是伪精度。按"控件匹配决策类型"重做：① 频率/阈值类（同步间隔/统计周期/评论上限/热门阈值×2）→ 预设快捷片+数字输入组合（.chips 一键选档 + .combo 精确数字带单位，输入非预设值时快捷片自动全灭）；② 并发/分段等小整数 → 步进器（− 数字 ＋，clamp 1~16）；③ 请求最小间隔保留数字框（技术精确值）；④ 布尔开关/分段选择器/画质 chips 保持。事件统一 wrap 委托，stRefresh 内嵌 stSyncCombos 使快捷片与数字双向一致。
- 验证：JS/构建绿；服务重启；浏览器断言——chips 点选/自定义数字灭片/步进器±/分段/放弃全还原/保存闭环（360→720→360 落盘实证）；截图确认。
- 教训（raw 收录）：共享树里并行 agent 的半途二进制会被当成"已交付行为"排查——先核对进程启动时间与构建时间再调 UI。过程插曲：运行实例一度是并行 agent 编辑中途的中间态二进制（chips CSS 在而 combo 字段缺失致委托抛错）；其**多用户鉴权已生效**（/api/v1/* 401，前轮快照的 ops_users WIP 已上线），ui 验证用 1h 过期临时会话、验证后即删、未改任何凭据。

### 总览媒体服务器区块补品牌图标 — ui-agent（自报 2026-09-09T19:10:00Z，实为同期新追加，done）
- 人类交办"给 emby/plex 等补上对应的图标"。总览「第三方媒体库在线」区块此前纯文字：从 simple-icons（CC0）取 Emby/Plex/Jellyfin/Kodi 官方 path，与自制 Teriteri 播放圈、通用服务器图标一起以 MS_ICONS 映射内嵌 index.html（无外部资源依赖，守单二进制约束）；msIcon(type) 渲染 18px 单色 SVG（fill var(--sub) 与控制台中性色一致），类型单元格 flex 排列图标+名称，未知类型回退通用服务器图标。
- 验证：构建绿；服务重启；浏览器实测五类图标全部渲染（teriteri 在线 4ms 绿点 + emby/jellyfin/plex/kodi 未检测到灰显）；截图确认。

### 管理端深色模式 — ui-agent（自报 2026-09-09T19:20:00Z，实为 09-11T19:3xZ 前新追加，done-review）（已提交 11b6c08）
- 人类交办"加入深色模式"。① 全站颜色已走 CSS 变量，新增 html[data-theme=dark] 暗色变量覆盖组（bg #0f1115/panel #161a20/line/txt/sub/faint，状态色 ok/warn/err/info 提亮保证暗底可读，accent 粉保持）；② 新增变量：表头底 --th、行悬停 --hover、primary 按钮悬停 --btn-hov、三色 banner 的 --ok/warn/info-bg+tx（浅深两套）；③ 硬编码清查变量化（th 底、行悬停、dot.off、primary 按钮文字与悬停、seg 选中文字、teriteri 日志 pre 底与错误文字）；④ <head> 防闪屏预置脚本（localStorage bili-theme 优先，未设置跟随 prefers-color-scheme）；⑤ 侧栏底部主题切换按钮（🌙 深色 / ☀️ 浅色），切换即存 localStorage 刷新保持；:root 加 color-scheme 让原生控件跟随主题。
- 踩坑：双 script 块提取首版贪心正则误跨块，node --check 改逐块。
- 验证：双 script 逐块 node --check 过；构建绿、服务重启；浏览器断言——dark 下 body #0f1115、aside #161a20、输入框反色、primary 按钮文字反色、切换按钮文案互切、localStorage 持久化；设置页/归档项目页深色截图确认；全量门 20 包绿。

### walkthrough/git 体系：提交策略改为按 feature — walkthrough-agent（2026-09-11T19:17:04Z，done）
- 人类指示"git 按 feature 提交，不要定时 snapshot，agent 做完一个就提交一下"。CronUpdate 重写自动化第二部分：每轮汇总后识别新完成（done/done-review）主题 → 按 raw「涉及」文件单独创建 feature 提交（`feat/fix/refactor(<scope>): 摘要（owner，状态）`+要点+验证+水位）→ 即时 push → WALKTHROUGH 小节标题回填「（已提交 <hash>）」作台账；纯文档改动单独 `docs:` 提交；无完成项的轮次不产生提交；轮询节奏不变（夜窗 30 分钟/白天 2 次，兼作完成信号发现）。
- 切换基点：7119ebf 为最后一个 snapshot 提交，其后改动按 feature 等待各自完成信号。

### 补档器多用户（v11：多用户+等级配额+权限隔离）— session-agent（2026-09-11T19:45:00Z，done-review）
- 人类交办"补档器加入多用户：用户可添加/设频率/查看/管理自己的补档（无其它功能）；管理员管理等级的每日添加数与检查频率"。
- 交付：store v11（users/sessions/user_tiers 三表 + 种子 admin/bili-saver 与 基础/进阶 两等级 + sources.user_id）；鉴权中间件重构（Bearer admin.token 兼容 → 会话 cookie bs_sess → 匿名仅放行登录；user 角色路径白名单=添加/识别/源列表/改删自己源/me/logout，其余 403）；handleAdd 用户分支（每日配额=当日 created_at 计数、频率钳等级下限、归属 user_id、强制不自动下载、免热门确认）；PATCH/DELETE 源归属校验（用户仅可改监控/频率）；/users 与 /tiers CRUD 端点（requireAdmin）；UI 三视图（登录门、角色化导航、我的补档页=额度显示+添加/编辑/删除、用户管理页=等级表+用户表）。
- 踩坑：①heredoc 与 JS 模板串/弯引号打架→大段 JS 改 Write 补丁文件+Python 拼接；②ListUsers SQL 前缀拼成 u.u.id；③PATCH 钳制在赋值之后等于没钳（重写赋值）；④buildNav 与并行编辑撞出交错重复（node --check 只查 findall[0] 漏检主块）——python 逐片段解析定位后字符串手术修复；⑤本机 LAN IP 从 192.168.1.114 漂移到 192.168.0.35（用户访问地址需更新，与代码无关）。
- 验证：全量门 20 包绿；多角色实测全过（含配额、钳制、隔离、停用即失效、删除转归）；UI 三视图浏览器实测。**认证体系重大变更，强烈建议人类晨间抽查：改默认管理员密码！**

## 待补时间戳

_（无）_
