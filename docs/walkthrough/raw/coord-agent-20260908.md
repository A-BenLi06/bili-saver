# coord-agent walkthrough（UTC 日期分文件，append-only）

## 2026-09-08T18:08Z coord-agent — 首轮协调巡检：坑台账建立 + 共享门红预警 + 时钟漂移取证
- 做了什么：人类交办协调 agent 角色，首轮巡检（02:00–02:08 本地）。① 建 `docs/coordination/PITFALLS.md` 坑台账（8 条，含时钟漂移+1h 实证：交接条目"02:45"写于 mtime 01:45 的文件；ui/session 两家同偏移，疑共同 TZ 来源）；② 修 ARCHITECTURE.md 陈旧数字 ×3（schema v5→v8、17→20 包）+ AGENTS.md「11 包」→20；③ COORDINATION §4 加时间戳实测条款、§5 加按包验证条款（ui-agent 当夜实践固化）；④ CLAIMS 登记 coord-agent claim + @all/@ui-agent/@core-agent 三条留言（共享门红预警、未处理留言摘要）。
- 涉及：docs/coordination/PITFALLS.md（新）、ARCHITECTURE.md、AGENTS.md、COORDINATION.md、CLAIMS.md、docs/walkthrough/raw/。
- 结果/状态：门禁实测 19/20 包绿，唯一 FAIL=internal/server TestPatchConfig（session-agent 设置页 WIP 半成品，nil Client panic，勿代改已 @all 广播）；服务健康（:8080 200，队列 running 4/queued 3）。共享热点编辑两次遭遇并行写入（ARCHITECTURE 02:01 被 core 改、CLAIMS 02:00 被 ui-agent 追加新 claim），均按 §3 重读后重做，零冲突。
