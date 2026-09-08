# walkthrough — 执行记录与自动汇总

各 agent 执行任务时**随手**在此追加 raw 记录（UTC 时间戳）；`walkthrough-agent` 每小时后台把新增内容整理归纳进 [WALKTHROUGH.md](WALKTHROUGH.md)。

## raw 记录（`raw/`，只增不删）

- **每人一个文件**：`raw/<owner>-<UTC日期 yyyymmdd>.md`（如 `raw/core-agent-20260907.md`），只写自己的文件，不存在则创建（文件头一行 `# raw — <owner> (<UTC日期>)` 即可）。
- **append-only**：任何已有内容（包括自己写错的）不得修改或删除；写错就追加一条更正。
- **时机**：完成一个节点（做完一步 / 踩坑 / 做出关键决策）就随手写，不要攒到会话结束凭回忆补。
- 进行中的长任务也在节点处记，不必等收尾。

条目格式：

```markdown
## <UTC时间戳> <owner> — <一句话描述>
- 做了什么：步骤 / 关键决策 / 踩坑
- 涉及：文件 / 端点 / 表
- 结果/状态：验证方式、当前状态（done / wip / blocked）
```

- 时间戳一律 **UTC ISO 8601**（`Z` 结尾），取自 `date -u +%Y-%m-%dT%H:%M:%SZ`。
- 简洁具体即可，这是给汇总和复盘用的素材，不求完整。

## 汇总（WALKTHROUGH.md）

- 由 `walkthrough-agent` 的每小时后台自动化**独家**维护，其他 agent 不要改它。
- 汇总只**读** raw、不写回；文件顶部"水位"行记录已整理到的最新 UTC 时间戳，自动化只处理水位之后的新条目，无新增则当轮跳过。
