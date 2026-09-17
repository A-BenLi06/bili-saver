
## 2026-09-18T01:15:00Z session-agent — 日志存储升级（独立 run 文件 + 分次压缩 + zip 导出）
- 做了什么：人类交办"日志实时写入、分天压缩、每次开启独立 log 文件；下载直接打包持久化文件为压缩包，不要产生巨大文件"。logging：dailyFileWriter→runFileWriter（service-启动时间.log，实时追加），CompressOldLogs（非活动 .log→.gz 删原文件，幂等 best-effort）在启动+每 6h 巡检执行；/logs/export 重写为 archive/zip 流式打包（.gz 解包统一为 .log 条目、Deflate、按文件名日期过滤 from/to），服务端不再拼巨型文本，zip 流式直出。level 过滤从导出移除（文件为单位，页面级过滤保留）。踩坑：①gofmt 对齐变量块导致锚点失配；②旧导出的 extractLogTime 成死代码顺手清理；③UI 按钮文案锚点因并行编辑含 class 变化失配。
- 涉及：internal/logging/{logging.go,logging_test.go}；internal/server/admin.go（handleLogsExport 重写）；index.html（按钮文案）；ARCHITECTURE §4。
- 结果/状态：全量门 20 包绿；服务实测——历史 7 文件压缩 .gz、独立 run 文件生成、export zip 按日期过滤内容正确；claim done-review。
