# coord-agent walkthrough（UTC 日期分文件，append-only）

## 2026-09-08T18:08Z coord-agent — 首轮协调巡检：坑台账建立 + 共享门红预警 + 时钟漂移取证
- 做了什么：人类交办协调 agent 角色，首轮巡检（02:00–02:08 本地）。① 建 `docs/coordination/PITFALLS.md` 坑台账（8 条，含时钟漂移+1h 实证：交接条目"02:45"写于 mtime 01:45 的文件；ui/session 两家同偏移，疑共同 TZ 来源）；② 修 ARCHITECTURE.md 陈旧数字 ×3（schema v5→v8、17→20 包）+ AGENTS.md「11 包」→20；③ COORDINATION §4 加时间戳实测条款、§5 加按包验证条款（ui-agent 当夜实践固化）；④ CLAIMS 登记 coord-agent claim + @all/@ui-agent/@core-agent 三条留言（共享门红预警、未处理留言摘要）。
- 涉及：docs/coordination/PITFALLS.md（新）、ARCHITECTURE.md、AGENTS.md、COORDINATION.md、CLAIMS.md、docs/walkthrough/raw/。
- 结果/状态：门禁实测 19/20 包绿，唯一 FAIL=internal/server TestPatchConfig（session-agent 设置页 WIP 半成品，nil Client panic，勿代改已 @all 广播）；服务健康（:8080 200，队列 running 4/queued 3）。共享热点编辑两次遭遇并行写入（ARCHITECTURE 02:01 被 core 改、CLAIMS 02:00 被 ui-agent 追加新 claim），均按 §3 重读后重做，零冲突。

## 2026-09-08T18:24Z coord-agent — 职责升级：经验总结职能，建 PLAYBOOK.md 经验库
- 做了什么：人类明确分工——walkthrough 只负责流水记录，coord 负责把坑提炼成经验并运用于协调（agent 上下文范围管理/职能配置/交流协议/坑模式化），目标是内化吸收+下个项目可复用。交付：① `docs/coordination/PLAYBOOK.md`——四层知识结构（raw 流水→PITFALLS 证据→COORDINATION 条款→PLAYBOOK 方法论）、10 条经验（E1-E10：时间戳机器生成/全局事实防腐化/共享文件并发编辑/门绿时点快照+按包验证/派生索引双证据/不代改/上下文最小阅读集/角色区域×时效配置/留言板三要素/验证阶梯），每条含原理/做法/证据（指 PITFALLS 编号）/迁移注意，外加 §0 一页总览、Day 0 移植清单、经验反哺协调的运用方式；② 巡检 cron 新增"经验提炼"步骤（水位比对、模式 vs 项目特有判断、重复出现第二次才提炼）；③ CLAIMS 登记 row 与 @all 留言同步更新。
- 涉及：docs/coordination/PLAYBOOK.md（新）、CLAIMS.md（登记行+@all）、巡检自动化 prompt。
- 结果/状态：生效。巡检调度同轮应人类要求收窄为仅夜间窗口（UTC+8 23:00–9:00）每 15min。

