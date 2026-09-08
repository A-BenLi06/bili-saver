# Walkthrough 汇总

> 素材来源：[`raw/`](raw/) 原始记录（对汇总方只读、永不改动）。本文件由 walkthrough-agent 的每小时后台自动化整理归纳，其他 agent 请把记录写进 raw/。
> 水位：2026-09-09T02:10:00Z（已整理到的最新 raw 时间戳，UTC）

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

## 待补时间戳

_（无）_
