# ui-agent 入职指引（UI/UX 岗）

> 给新到岗的 ui-agent 会话（模型：antigravity / Gemini 3.6 Flash）。**先读完本页再动代码**，全文 5 分钟。

## 你的身份与岗位

- 身份：`ui-agent`（岗位名跨会话稳定；你是更换模型后的新一任会话）。
- 岗位：UI/UX 总设计师——管理端（:8080）与 TeriTeri 前端（:8081/:8082）的界面、交互、可用性。
- 协作环境：多 agent 并行开发（core/session/teriteri 等同时在工作），规则见 [COORDINATION.md](../../COORDINATION.md)，动态看 [CLAIMS.md](../../CLAIMS.md)。

## 开工前必读三件（顺序固定）

1. **AGENTS.md**（根目录）：环境命令、红线、UI 约定、已知陷阱。
2. **CLAIMS.md**：留言区所有 @ui-agent 的未处理消息 + 登记表里 UI 相关的活跃行。
3. 本文件剩余部分。

按需再读：ARCHITECTURE.md 相关节（改到哪读到哪）、docs/coordination/PITFALLS.md 索引、PLAYBOOK.md §0。

## 环境速查（Windows / Git Bash）

- Go 工具链：`export PATH="/c/Users/Ben/.local/go/bin:$PATH" GOPROXY="https://goproxy.cn,direct"`。
- **Git Bash 下 curl 发中文会变 `???`**——测带中文的 API 一律用 Python（UTF-8）。
- 服务：`./bili-saver.exe -config config.yaml serve` 占 :8080（管理端）；TeriTeri :8081（用户端）/ :8082（管理员端）。重启前先杀旧：`powershell -Command "Get-Process bili-saver | Stop-Process -Force"`，**重启后留言留痕**。
- JS 语法校验：`node --check`（单文件 UI 无构建链，一个引号嵌套错误曾弄挂整页——历史事故）。

## UI 约定（红线级，用户明确要求）

- SaaS 风格：侧栏导航、中性色、细线表格、圆点状态。
- **少用胶囊、禁止卡片套卡片**。
- 管理端是控制台（状态/完整度/审计），**不内嵌播放**；观看走 TeriTeri 前端或媒体库。
- 前端是**单文件内嵌 UI**（`internal/server/static/index.html`，go:embed，无构建链）：vanilla JS 内联 + CSS 同文件；`admin.go` 是后端配套；TeriTeri 前端是独立 SPA（`data/teriteri/client`，npm 构建，经 `internal/adapter/teriteri` 适配）。

## 当前 UI 地形（已交付，勿重做；重读对应 claim 行可查实现细节）

- 搜索页（全文检索/状态过滤/高亮/消失灰显）。
- 设置页（栅格化 + sticky 保存条 + 只读区标注）。
- 跟踪列表页（状态 Tab/分页/搜索 + **请求竞态守卫**）。
- 归档项目页（状态 Tab/可点击排序▲▼/分页/发布列 + 竞态守卫）。
- 归档详情页（封面头部/数据条/kv 瘦身/评论头像本地化）。
- 采集页与监控源页（触发器语义文案/动态提示）。
- TeriTeri 端互动层（收藏/点赞回显/搜索联想/账号自助三件套等 12 项）。

## ⚠️ 重大变化：多用户鉴权已上线

schema v11 引入 users/sessions 表与会话 cookie 鉴权（Bearer 兼容），**API 已要求登录**（无凭据请求返回 401/unauthorized）。你的验证脚本需带凭据或仅用 `/` 探活；前端登录态 UI（登录页/用户菜单/按权限显隐）是**新地基上的空白区**——见"建议首个任务"。

## 流程纪律（违者会被巡检留言）

- **开工前登记 claim**（区域/owner/错区声明——index.html/admin.go 是共享热点，写明只动哪个函数/区块）。
- 完成验收：node --check → go build/vet → 全量 `go test -count=1 ./internal/...` → 服务重启（留痕）→ DOM 断言/浏览器截图 → claim 置 done-review + 留言回执。
- **时间戳用 `date '+%Y-%m-%d %H:%M'` 实测**——前任会话有时钟漂移前科（快 1h），勿继承；写前自查。
- 共享文件改前重读目标块，锚点断言失败即停（COORDINATION §3）。
- 会话结束写晨间交接（CLAIMS 顶部，四段式）。

## 建议首个任务（认领前在留言区打招呼避免撞车）

1. **登录态 UI（推荐）**：多用户 v11 的前端呈现——登录页/用户菜单/按角色显隐。新地基的空白区，价值最高。
2. 归档详情页继续打磨（竞态守卫刚就位，顺承最稳）。
3. TeriTeri 端登录态联动（:8081 前端与 v11 会话打通）。

## 遗留待办（继承自前任）

- 留言区 @ui-agent 未处理项（时间戳更正条等）——处理完原地标已处理。
