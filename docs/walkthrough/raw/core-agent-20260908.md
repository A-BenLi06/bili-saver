# core-agent raw walkthrough（append-only）

## 2026-09-08T02:05:00Z core-agent — 图片本地化立项：诊断 teriteri 封面/头像不显示
- 做了什么：人类反馈 teriteri 全部封面/头像不显示。实测确认根因：适配层把 B 站图床原始 URL（videos.pic / owner_face / comments.member_face）直接给前端，浏览器带第三方 Referer 请求 i*.hdslb.com 被防盗链 403（curl 验证：带第三方 Referer 403、无 Referer/B 站 Referer 200）。另确认头像全链路从未落盘，封面仅 download 任务落盘 media/{bvid}/cover.jpg（187/189 已有）且适配层未使用。
- 涉及：internal/adapter/teriteri（adapter.go/comments.go/extras.go 返回 URL）、internal/downloader(downloader.go downloadCover)。
- 结果：方案定为图床资源本地化进 origin 层 media/；claim 已登记 CLAIMS.md；wip。

## 2026-09-08T02:20:00Z core-agent — images 包与 store 查询落地
- 做了什么：新建 internal/images（Localizer：SyncVideo 单视频封面+UP主+评论者头像、Backfill 全库补档、CoverURL/FaceURL 查询助手；原子落盘 .part→rename、Referer/UA 头、20ms 限速、单图 20MB 上限、失败只告警不中断）。store 新增 Face 类型 + ListCommentFaces(bvid)（GROUP BY mid 去重，含已消失评论）。封面沿用 media/{bvid}/cover.jpg 约定（card 生成器同路径受益），头像 media/faces/{mid}.jpg。
- 涉及：internal/images/**(新)、internal/store/models.go、ops_comments.go。
- 结果：images/store/adapter 三包 build/vet/test 单独全绿（4 个用例：落盘+幂等跳过+失败不致命+去重）；wip。

## 2026-09-08T02:25:00Z core-agent — 接线与 teriteri URL 改写
- 做了什么：app.go 增 Img 组件、archive/download 任务后挂 SyncVideo（非致命）、新任务类型 fetch_images(jobFetchImages)；admin.go 增 POST /api/v1/images/backfill 入队补档；teriteri 适配层 coverURL/faceURL 本地优先、缺失回退 CDN（getone/card/评论/UP主空间四处）。适配层契约测试 localurl_test.go 验证本地优先+回退。
- 涉及：internal/app/app.go、internal/server/admin.go、internal/adapter/teriteri/{adapter,comments,extras,localurl_test}.go。
- 结果：blocked（整体）——internal/card 半成品编译失败（render.go 引用 draw.go 未有方法、withAlpha 重复声明），已留言 @card-agent，不代改；本会话改动单独验证全绿；wip。

## 2026-09-08T03:39:00Z core-agent — 图片本地化完成：存量补档跑完 + 全链路实测通过
- 做了什么：card 包编译恢复后全量 `go vet ./...` + `go test -count=1 ./internal/...` 17 包全绿；重编译二进制并重启服务（:8080）；提交 fetch_images 任务 #409（入队），另用临时 runner 直连 data 目录跑完存量补档 10449/10449（新增 6272 文件），跑完即删。清理 scratch-backfill。
- 涉及：internal/images/**、internal/store/{models,ops_comments}.go、internal/app/app.go、internal/server/admin.go、internal/adapter/teriteri/**、ARCHITECTURE.md、CLAIMS.md。
- 结果：done（自动化阶梯 3/3）。实测：getone/推荐卡/评论的封面与头像 URL 已切本地 `/media/...` 且 HTTP 200 image/jpeg；存量覆盖：封面 389/389、UP主头像 4/4、评论者头像 10926/11128（缺口=图床 404 注销账号，回退 CDN 由前端占位）。注意：media/faces 占 1.1GB 磁盘；teriteri SPA 前端需强刷一次。

## 2026-09-08T09:35:00Z core-agent — 下载质量选择完成（清晰度白名单+音频偏好链）
- 做了什么：实现人类交办的"默认最高画质/可选全部/可选指定几档"。config 增 download.qualities（空=全部=最高可用；指定档全缺回落更低档；兼容旧 quality 单档上限语义）与 download.audio_qualities（hires/dolby/192k/132k/64k，空=标准最高，集合优先标准链兜底）；downloader 重写选流（videoQNs/selectVideo 三级回落、audioPrefChain/selectAudio、resolveQuality 动态 fnval 128(4K)/256(Hi-Res/杜比)）；biliapi Dash 模型补 dolby/flac 独立音轨字段并并入候选；admin handleConfig 暴露新字段；config.example.yaml 注释化文档；用户 config.yaml 由 quality:"360p" 改为默认最高（allow_av1/allow_4k 开）。
- 涉及：internal/config/config.go、internal/downloader/downloader.go(+test)、internal/biliapi/models.go、internal/server/admin.go、config.example.yaml、config.yaml、ARCHITECTURE §4。
- 结果：done。全量 build+vet+test 绿（新增 TestSelectVideoQualitySet/TestResolveQuality/TestSelectAudio 重写）；端到端实测：服务重启后重下 2 视频，360p→720p（未登录无 SESSDATA，720p 即最高可用，登录后自动取 1080p+）、音轨保留、无音轨源不回归。踩坑：测试用例自身预期错两处（mkStreams 120 是 AV1 需 AllowAV1、64k 精确命中误当兜底）。注意：并行 agent 17:29 再次重建并重启了服务，二进制已含本功能（/api/v1/config 验证过）。

## 2026-09-08T10:05:00Z core-agent — B站用户等级+UID 拉取与展示完成（schema v6）
- 做了什么：人类交办"缺失拉取任务的 b站uid、b站等级"。核实：UID 全链路本就有（comments.member_mid / videos.owner_mid / 适配层 uid 字段）；等级从未采集。交付：biliapi reply Member 解析 level_info + 新增 SpaceAccInfo（wbi+buvid+空间 Referer）；collector 两处入库 MemberLevel；schema v6（comments.member_level + videos.owner_level，ensureColumn 幂等，迁移历史已记）；新包 internal/levels——CommentLevelsFromSnapshots 从 raw 评论快照（x_v2_reply_reply / x_v2_reply_wbi_main，内含完整 member.level_info）离线回填存量，OwnerLevels 走 space acc/info 拉UP主等级（无 SESSDATA 自动跳过），LevelExp 按前端契约映射；fetch_levels 任务 + POST /api/v1/levels/backfill + 归档钩子 EnsureOwnerLevel；teriteri 适配层 tUser.Exp=等级经验下限（评论/getone/推荐卡/空间页）驱动 VLevel 徽章。前端契约实证自本地 teriteri-client 源码（UserCard.vue/CommentTree.vue/utils.js handleLevel 边界 50/200/1500/4500/10800/28800）。
- 涉及：internal/biliapi/{models,endpoints}.go、internal/collector/comments.go、internal/store/{models,schema.sql,store.go,ops_comments.go,ops_videos.go}、internal/levels/**(新)、internal/app/app.go、internal/server/admin.go、internal/adapter/teriteri/**、ARCHITECTURE §4/§5/§7/§8。
- 结果：done。全量 build+vet+test 14 包全绿（levels 回填/幂等/损坏跳过、LevelExp 边界、适配层 exp 契约等新用例）；线上实测：服务重启后 fetch_levels #809 跑完，评论等级回填 24489/24644（99.3%，分布 Lv2-Lv6 合理），UP主等级未登录优雅跳过（登录后重跑或新归档自动补），commentGet 实测返回真实 uid + exp（Lv6=28800 / Lv4=4500 正确）。踩坑：archive/gzip 不存在（应为 compress/gzip）、UpsertVideo 占位符 22 列须 22 个 ?、测试 mkStreams 等自身预期错误两处。

## 2026-09-08T12:05:00Z core-agent — 更正：UP主等级"更新=5"实为软风控误判，已修复
- 做了什么：上一条目"UP主等级拉取完成 更新=5"经查为误判——B 站对登录态失效/风控场景返回 code=0 但剥离 level_info 的降级数据（acc/info 快照实证：有 mid/name 无 level_info），旧代码把缺失字段当 Lv0 写库并计成功。修复：OwnerLevels 门槛从 HasCookie(SESSDATA) 改为 CachedLogin（nav 真值校验，5min 缓存）；SpaceAccInfo.LevelInfo 改指针，缺失即判软风控——拒绝写库、停止本轮并提示重新扫码。全量 15 包绿，重建重启后 fetch_levels #810 实测：正确识别软风控并优雅停止，未写脏数据。
- 涉及：internal/biliapi/models.go（LevelInfo 指针化）、internal/levels/levels.go。
- 结果：done。遗留：当前登录态对 space 接口仍被软屏蔽（评论等级不受影响，回填 99.3% 已持久化）；UP主等级待 B 站解除软屏蔽或重新扫码登录后，重跑 POST /api/v1/levels/backfill 即可补齐。另观察：cookies.json 的 SESSDATA 曾出现又消失（多个并行实例覆盖 cookie 文件所致可能性大），多实例并存时 cookie 文件存在互相覆盖风险——建议后续考虑 cookie 文件写入加锁或单实例约束（@core-agent 后续）。

## 2026-09-08T13:55:00Z core-agent — 新视频触发器+跟踪列表完成（schema v7）
- 做了什么：人类交办"up主/合集/收藏夹触发器，检测到新视频自动下载一次，加入跟踪列表"。schema v7：sources.auto_download 三态（1开/-1关/0跟随全局 download.auto_download，对全部源类型生效）+ video_tracks 表；app.go sync_source 新视频判定 triggerOn(src.AutoDownload, 全局)——命中即归档+下载一次（下载幂等天然保证"只下一次"）并 TrackAdd；手动 sync 带 download 参数视为手动路径不入列；jobArchive 下载成败 TrackSetState 回写（done/failed，无记录空操作）；admin PATCH /sources/{id} 接受 auto_download（校验 -1/0/1）+ GET /api/v1/tracks（联 videos 补标题/UP主/落盘态）；index.html 监控源表加"新视频触发器"列、设置对话框加三态选择、新增"跟踪列表"导航页。
- 涉及：internal/store/{models,schema.sql,store.go,ops_misc}.go、internal/app/app.go(+trigger_test.go)、internal/server/admin.go、internal/server/static/index.html、ARCHITECTURE §5/§6.3/§7。
- 结果：done。全量 build+vet+test 15 包全绿（triggerOn 三态、Track 生命周期/幂等/联表用例；inline JS node --check 过）。线上实测：PATCH 源5 auto_download=1 持久化✓；造两个库外热门视频场景——手动 /add 路径不入列✓、SQL 直建触发源(11) 手动同步后 track 行出现(queued)✓；归档任务因热门视频评论全量翻页仍在进行，完成后自动转 done（状态回写逻辑有单测覆盖）。

## 2026-09-09T02:10:00Z core-agent — 热门视频补档二次确认完成
- 做了什么：人类交办"热门视频补档前段先警告反馈一下，需要再次确认"。config 增 collector.hot_video_views（默认 100万）与 hot_video_replies（默认 1万），0=禁用；/add（视频类型，实时 view 接口判定）与 /videos/{bvid}/archive（库内最新 stats 判定，重同步重拉评论同样耗时）未确认时返回 200+{need_confirm,reason,view,reply} 不入队（用 200 而非 4xx：前端 api() 助手错误路径只透传 error 字符串会丢结构体标记）；前端采集页与归档项目页"重同步"按钮确认后带 confirm_hot 重提。自动化路径（触发器/调度/触摸统计）不打扰。
- 涉及：internal/config/config.go、config.example.yaml、internal/server/{admin.go,hot_test.go}、internal/server/static/index.html、ARCHITECTURE §7。
- 结果：done。全量 16 包绿（新增 hotReason 边界/禁用用例，inline JS node --check 过）；线上实测三条路径——LPL 热门视频(175.9万播放/1.1万评论)重同步未确认→need_confirm✓；普通视频 confirm_hot→直接入队✓；/add 阴阳师CG(219万播放)未确认→need_confirm 不入队✓。

## 2026-09-09T02:55:00Z core-agent — 总览页第三方媒体库在线状态完成
- 做了什么：人类交办"主panel增加第三方媒体库在线情况显示（teriteri/emby/jellyfin/plex/kodi）"。config 增 media.servers 列表（name/type/url）+ MediaServer 类型；新端点 GET /api/v1/media-servers/status 并发探测（3s 超时）——emby/jellyfin→/System/Info/Public、plex→/identity、kodi/teriteri→根路径，**收到任意 HTTP 响应（含 401/403 鉴权）即在线**，note 带状态码；teriteri 未配置但适配启用时自动显示"内置适配（随核心进程运行）"；总览页新增"第三方媒体库在线"区块（在线点/延迟/链接/note）。用户 config.yaml 已配 teriteri 前端(8081)。
- 涉及：internal/config/config.go、config.example.yaml、config.yaml、internal/server/{mediaservers.go,mediaservers_test.go,admin.go}、internal/server/static/index.html、ARCHITECTURE §7。
- 结果：done。全量 17 包绿（类型健康路径/401 在线判定/离线与空地址用例；inline JS 过）；实测 teriteri 前端 HTTP 200 / 57ms 在线。**踩坑（多 agent 竞争）**：我写入 config.yaml 的 servers 条目被并行 agent 的"设置页保存 config"功能整文件重写覆盖（yaml Marshal 回写丢失注释与我的条目）——config 文件与 cookies.json 一样存在多实例互相覆盖风险，已在留言区提醒；重加条目后验证通过。当前 8081 的 teriteri 前端已由 teriteriweb agent 的核心自托管方案接管（client_addr :8081）。

## 2026-09-09T03:20:00Z core-agent — 媒体库状态改为五行恒显示（未配置→未检测到）
- 做了什么：人类反馈总览页"各媒体库状态还没看到，要显示如 emby（未检测到）"。重构 mediaServerStatuses：恒定输出 teriteri/emby/jellyfin/plex/kodi 五行（未配置实例的类型输出 Detected=false + Note="未检测到"；已配置多实例聚合"在线 m/n"，单实例透传其 note；teriteri 未配置实例但适配启用时仍显示内置在线项，显式配置则不叠加）；前端按行渲染 在线/离线/未检测到 三态 + 实例子行；server.go 静态页与首页加 Cache-Control: no-cache（页面随二进制内嵌更新，防浏览器停留旧版 UI——"看不到新版"的另一个诱因）。
- 涉及：internal/server/{mediaservers.go,mediaservers_test.go,server.go,static/index.html}。
- 结果：done。server/config 包测试绿（新增五行聚合/内置不叠加/未检测到断言），inline JS 过；线上实测五行输出——teriteri 在线(HTTP 200)、其余四类"未检测到"，首页带 no-cache 头。

## 2026-09-11T03:30:00Z core-agent — 账号页当前账号信息完成
- 做了什么：人类交办"账号页显示现在的帐号状态，uid，账号名，头像，等级，是否为大会员等"。biliapi 增 NavAccount（nav 解析 isLogin/mid/uname/face/level_info.current_level+current_exp/vipStatus/vipType/money，getJSONLenient 容忍未登录）；admin 增 GET /api/v1/login/status（nav 失败按未登录呈现不炸页面）与 GET /api/v1/login/avatar（头像服务端代理——B站图床对第三方 Referer 403，必须转发）；账号页新增"当前账号"信息卡（头像/昵称+大会员徽章/UID/等级+经验/大会员状态/硬币/刷新按钮），扫码区下移为"重新扫码覆盖登录态"。
- 涉及：internal/biliapi/{login.go,account_test.go}、internal/server/admin.go、internal/server/static/index.html、ARCHITECTURE §7。
- 结果：done。全量 18 包绿（NavAccount 解析用例）；线上实测——logged_in=true BenLi06 / mid=397703470 / Lv.6 / exp 30511 / 非大会员 / 硬币1105，头像代理 200 image/jpeg 202KB。附带确认：当前登录态有效（此前 space 软风控期间的登录态问题已不复存在），fetch_levels #2908 已入队等待 worker 空闲后补齐全库 UP主等级（现 124 个 distinct UP主）。另：等待期间 teriteri 适配层被并行 agent 短暂置于编译失败态（sort import 残留），约 10 分钟后对方自行修复，未代改。
