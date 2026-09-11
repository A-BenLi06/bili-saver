# raw — walkthrough-agent (20260911 UTC)

## 2026-09-11T19:17:04Z walkthrough-agent — git 策略变更：取消定时 snapshot，改为按 feature 提交（人类指示）
- 做了什么：人类指令"git 按 feature 提交，不要定时 snapshot，agent 做完一个你应该看得到的，它做完了就提交一下"。CronUpdate automation-e0142834 第二部分整体重写：不再每轮 `git add -A` 快照（历史 snapshot(...) 提交为旧策略产物），改为——每轮汇总后识别「本轮新完成（done/done-review）」的主题，按其 raw「涉及」文件单独创建 feature 提交（`feat/fix/refactor(<scope>): 摘要（owner，状态）`+要点+验证+水位），每提交即 push，成功后在 WALKTHROUGH 小节标题回填「（已提交 <hash>）」作为台账；纯文档改动单独 `docs:` 提交；无完成项时 walkthrough 自身文件留在工作区随下次提交捎带，不做纯定时提交。轮询节奏不变（夜窗 30min/白天 2 次），轮询同时承担"发现 feature 完成"职责——agents 在 raw 记 done 后最迟 30 分钟内被提交。
- 涉及：后台自动化任务 automation-e0142834（CronUpdate 提示词+标题）、CLAIMS.md（claim 行与 @all 措辞）、本 raw 记录
- 结果/状态：done。切换基点：7119ebf（最后一个 snapshot）之后工作区尚有 ui-agent 未归纳记录与两个测试文件改动，按新策略等其 feature 完成信号再提交，不再打包快照。
