# AGENTS.md — 面向 AI agent 的协作指南

> 本项目有多个 agent 并行开发。改动前先读 [ARCHITECTURE.md](ARCHITECTURE.md)（架构与规划）和本文件。**修改别人区域前先确认现状**——文件可能在你读取后被并行 agent 改动。

## 环境与命令

```bash
# Go 工具链（用户级安装，未动系统）：
export PATH="/c/Users/Ben/.local/go/bin:$PATH"
export GOPROXY="https://goproxy.cn,direct"   # proxy.golang.org 不可达，必须走镜像

go build ./...          # 编译
go vet ./...            # 静态检查
go test -count=1 ./internal/...   # 全量测试（改动后必须全绿；被并行半成品阻塞时按 COORDINATION §5 按包验证）
gofmt -w internal cmd   # 提交前格式化

# 本地运行（Windows/Git Bash，服务占 8080）
./bili-saver.exe -config config.yaml serve      # 后台运行时用 run_in_background
# 停止旧实例再起新的（端口占用会 bind 失败）：
powershell -Command "Get-Process bili-saver -ErrorAction SilentlyContinue | Stop-Process -Force"
```

- 运行数据在 `data/`（SQLite + raw/ + media/ + derived/ + library/）。**不要提交或删除**。
- 测试插件 `data/plugins/test-echo` 会写 derived_summaries，验证插件链路用。
- ffmpeg 已在 PATH。Docker 本机不可用（compose 文件就绪但未实测）。

## 红线（违反即破坏产品核心价值）

1. **origin / derived 分离不可破**：插件 op 与任何生成数据只能进 derived 层（origin=plugin:*），不得写入 B 站原始数据表行。
2. **消失检测防御勿绕过**（ARCHITECTURE §6.2）：未登录跳过评论对账、两轮确认、弹幕降级检测。背景：B 站未登录评论接口会伪报 is_end，曾造成 407 条评论被误标消失（已恢复+加防）。
3. **只增不删**：comments/danmaku 永不物理删除。
4. **cookie jar 必须挂载**（`http.Client{Jar: jar}`）——曾因漏挂导致扫码登录态全丢。
5. **schema 迁移必须幂等**（`ensureColumn`/`IF NOT EXISTS`），老库启动自动升级，不能要求手工迁移。

## 并行协作协议（必读）

多 agent 并行开发的完整规则见 **[COORDINATION.md](COORDINATION.md)**，动态登记与留言板见 **[CLAIMS.md](CLAIMS.md)**。要点：

- **动手前**：读 CLAIMS.md——目标文件有别人的活跃 claim 就不要碰；处理 @你/@all 留言。
- **开工时**：登记 claim（区域/owner/状态）；完成前过验收门（build+vet+test 全绿）才能置 done。
- **昼夜模式**：白天（9:00–23:00）人类在线可裁决；夜间（23:00–9:00）自主执行人类交办任务——**"需要人工测试确认"不是挂起理由**，走自动化验证阶梯（测试→起服务脚本验证→真实接口冒烟→浏览器截图）替代，通过即 done；L3 挂起仅限红线/产品方向/外部发布。会话结束写晨间交接（CLAIMS.md 顶部）。
- **独占区现状**：`internal/card/**` 与卡片端点/UI 接线归 card-agent，**已于 2026-09-08 收尾 done**（见 CLAIMS.md）；后续改动走常规 claim 登记即可。
- **共享热点**（app.go / admin.go / server.go / index.html / ARCHITECTURE.md）：改前重读目标代码块，精确锚点+断言，断言失败即停检查并行改动；结构性改动去 CLAIMS.md 留言区广播。

## 已知陷阱（实战踩过）

- **Git Bash 下 curl 发中文会变 `???`**（编码链路问题）。测试带中文的 API 用 Python（UTF-8 可控）发请求体。
- **python 补丁脚本断言失败会阻止写盘**——多段替换时前面成功的内容也会丢。先全部验证锚点再一次性写。
- 任务队列**崩溃恢复会重放** running 任务：杀进程后重启会看到旧任务重跑，属预期。
- 下载器幂等：本P mp4 存在则跳过（含不请求 playurl）；重复下载任务是验证幂等的快捷方式。
- 短生命周期 CLI（add）退出前必须 `a.Bus.Wait()`，否则异步插件事件来不及执行。
- Windows 符号链接：`linkFile` 的 src 已强制绝对路径；残留坏链接由 Lstat 分支清理。
- **踩坑先查台账**：全量坑记录与规避方式见 [docs/coordination/PITFALLS.md](docs/coordination/PITFALLS.md)（coord-agent 维护）；踩到新坑追加台账或留言 @coord-agent。

## 约定

- UI 遵循现有 SaaS 风格（侧栏/中性色/细线表格/圆点状态）；**少用胶囊、禁止卡片套卡片**（用户明确要求）。
- 管理端是控制台（状态/完整度/审计），不内嵌播放；观看走 teriteri/媒体库。
- 新增配置：config.go 加字段 + Defaults() + config.example.yaml + `/api/v1/config` 暴露（脱敏）。
- 新增表/列：schema.sql（基线）+ migrate() 补列 + 更新 ARCHITECTURE §5 迁移历史。
- 错误信息面向用户可读（如 space 源未登录时给出扫码指引），不做裸 code。
