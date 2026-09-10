
## 2026-09-10T17:05:00Z session-agent — UP主源扩展归档：动态/动态视频/图文专栏（v10）
- 做了什么：人类交办"up主归档增加所有动态与动态视频、所有专栏，设置里可勾选（默认全部）"。接口实测定型：动态 feed（polymer）免 wbi 可用且 AV/WORD/DRAW/FORWARD/ARTICLE 同源；传统专栏列表接口已死（legacy 空、opus 404）→ 专栏经 feed 的 ARTICLE/图文类归档。交付：biliapi DynamicFeed（FlexInt 兼容 pub_ts 字符串）+ store v10（space_posts 只增不删+消失标记防御+sources.space_scope）+ collector SyncSpaceDynamics（offset 翻页全量、拉到底才标记消失）+ ParseSpaceScope/ValidSpaceScope（''/'all'/'none'/子集）+ jobSyncSource 接线（动态视频提取 bvid 归档去重）+ admin（/add 与 PATCH space_scope 校验、GET /sources/{id}/posts）+ UI（采集页与监控源编辑四勾选，space 类型才显示、默认全选）。
- 涉及：internal/biliapi/{models.go,endpoints.go}；internal/store/{models.go,ops_misc.go,ops_space.go(新),store.go,space_posts_test.go(新)}；internal/collector/{space_dynamics.go(新),space_dynamics_test.go(新),space_live_test.go(新)}；internal/app/app.go；internal/server/admin.go；internal/server/static/index.html；ARCHITECTURE §4/§5。
- 结果/状态：真实 UP 主端到端 794 条动态落库 + 669 bvid 提取；PATCH 往返/非法 400/UI 勾选实测；全量门 19 包绿（并行 core TestNavAccount WIP 红未收敛，与本改动无关已注明）；claim done-review。已知限制：纯不发动态的老专栏暂收不到（接口层面无解，已标注）。

## 2026-09-10T18:25:00Z session-agent — UP主源追加追番/追剧列表归档（v10 追加）
- 做了什么：人类交办"追番列表等也加入"。实测 /x/space/bangumi/follow/list（type=1 追番/2 追剧，免 wbi，用户关闭公开时返回空）→ biliapi SpaceFollowList（raw 逐条保留）+ collector SyncSpaceBangumi（分页到底、kind=bangumi、dtype=follow_bangumi/follow_cinema、正文=evaluate+集数）+ scope 第 5 token bangumi（Parse/Valid/全选阈值 5）+ MarkSpacePostsGone 加 kinds 作用域（动态轮与追番轮互不误标）+ UI 第 5 勾选（scopeChecked 改动态长度）+ app 接线。踩坑：①追加方法时 Python 锚点被注释里的弯引号破坏——改用更小锚点分步断言；②错误提示漏列 bangumi 补正。
- 涉及：internal/biliapi/{models.go,endpoints.go}；internal/collector/{space_dynamics.go,space_live_test.go}；internal/store/{ops_space.go,space_posts_test.go}；internal/app/app.go；internal/server/admin.go；index.html；ARCHITECTURE/CLAIMS。
- 结果/状态：live 实测 mid=2 落库 156 条（122 追番+34 追剧，标题/简介/raw 完整）；服务内 PATCH 五 token 往返+非法 400+UI 双勾选组全过；v10 claim 追加标注 done-review。
