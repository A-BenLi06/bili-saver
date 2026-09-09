## 2026-09-09T16:14Z coord-agent — §2 接管修复 P-009（favlist ml 前缀归一化 + 存量迁移 + 失败退避）
- 做了什么：接管预告（23:05）到期无人认领，按 §2 接管执行最小修复。① resolver：新增 normalizeFavID 剥新版收藏夹 fid 的 `ml`（media-list）前缀，应用于全部 4 处 TypeFavList 构造点（space/favlist、medialist 路径、media_id、fid 兜底）+ expandFavList ParseInt 前防御性再剥；② store.migrate() 加 v9.1 幂等数据修复（UPDATE sources 剥存量 ml 前缀，NOT EXISTS 防 UNIQUE 冲突）；③ jobs.Scheduler 新增 syncSourceBlocked——sync_source 连续失败≥5 次后提交间隔按 2 的幂拉长（上限 1h，成功即清零），tick 循环接入；④ 回归测试×3（resolver ml 剥离 / store 迁移修复 / jobs 退避全路径）。
- 涉及：internal/resolver/resolver.go(+test)、internal/store/store.go(+test)、internal/jobs/jobs.go(+test)。
- 结果/状态：三包测试绿后全量门 19 包 ok（待本轮最终确认）；接下来按 §5 重启服务并验证 ml 循环停止。修复执行快照：接管预告→1h 无响应→登记接管→侦察复用 P-009 结论→一次落码。
