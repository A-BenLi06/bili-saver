# bili-saver —— Bilibili 补档机

把指定的 B 站视频（链接 / BV 号 / UP 主 / 收藏夹 / 合集）**忠实归档**：视频、弹幕、字幕、评论、标签、简介、封面、点赞收藏等全部数据；**已消失的评论与弹幕不删除，与现存内容共存并显式标记**。支持 Docker 一键部署与 Linux/macOS/Windows 二进制运行。

架构设计详见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 核心特性

- **数据忠诚**：所有 B 站接口响应先原样落快照（`data/raw/`），再解析入库；AI/插件生成的数据只能写入 `derived` 层（数据库 origin 字段 + `data/derived/` 目录），与原生数据**机制上强制分离**。
- **补档语义**：评论/弹幕消失检测（多次快照对比），消失记录保留正文并标记 `deleted_at`；点赞/收藏等按时间序列留痕。
- **播放前端**：内置 [teriteri-client](https://github.com/xunlu129/teriteri-client)（Vue3 仿 B 站）适配层，可选部署；自带简版管理页（视频库/任务/扫码登录/插件管理）。
- **媒体库桥**：按 Kodi NFO 规范生成 Emby/Jellyfin/Kodi 可刮削目录（Plex 靠命名约定），软链接→硬链接→复制跨平台降级链。NFO 最大化写入 B 站原生数据：标题/简介/官方AI小结/统计快照（播放弹幕点赞投币收藏分享）/标签作 genre+tag/UP主作 studio+actor/点赞率作评分/来源收藏夹作合集（set）；输出 poster/fanart/banner 图片组、B站CC字幕自动转 srt 外挂、剧集布局含聚合 tvshow.nfo 与分集缩略图。
- **插件系统**：事件总线 + 外部进程插件（任意语言），官方示例：
  - `whisper-subtitle`：无 CC 字幕视频自动转录（faster-whisper / whisper.cpp / MacWhisper）
  - `ai-summary`：本地 Ollama 或 OpenAI 兼容接口生成总结
  - `ai-tagger`：AI 补充标签（与 B 站原生标签分区展示）
- **快速预览卡片**：每个已归档视频自动生成一张 B 站风格分享卡片（封面/UP主/统计/归档完整度/二维码，参考 astrbot_plugin_bilibili_analysis 的卡片视觉），输出到 `data/derived/{bvid}/card.png`，供日后分享功能直接取用。
- **风控友好**：全局限流（默认 350ms 间隔）、WBI 签名、扫码登录 + cookie 管理、备用 CDN 切换。

## 快速开始

### 二进制

```bash
# 构建（或从 Release 下载）
go build -o bili-saver ./cmd/bili-saver

# 1. 扫码登录（可选，提高清晰度上限与接口可用性）
./bili-saver -config config.yaml login

# 2. 归档：单视频 / UP主 / 收藏夹 / 合集，都行
./bili-saver add "https://www.bilibili.com/video/BV1GJ411x7h7"
./bili-saver add "https://space.bilibili.com/946974"
./bili-saver add "https://space.bilibili.com/946974/favlist?fid=123456"
./bili-saver add "https://space.bilibili.com/546195/channel/collectiondetail?sid=1234"

# 3. 常驻服务：管理端 http://127.0.0.1:8080/ ，定时重同步与消失检测
./bili-saver serve
```

首次运行把 `config.example.yaml` 复制为 `config.yaml` 按需修改；所有字段可用 `BILI_SAVER_*` 环境变量覆盖。

### Docker

```bash
cd deploy
docker compose up -d          # core :8080（管理+API+播放）  web :8081（teriteri 前端）
# 不需要 teriteri 前端就删掉 web service
```

### 对接 Emby / Plex / Jellyfin / Kodi

```bash
./bili-saver export-nfo       # 或在管理页点“重建媒体库软链接”
```

然后把 `data/library/` 作为媒体库目录添加即可（`movie` 布局=每视频一部电影；`show` 布局=UP主当作剧集；同 UP主 ≥3 个视频自动按剧集输出）。

**让 B 站数据完整显示的关键设置**（否则会被在线元数据覆盖）：

- **Emby**：库 → 元数据下载器 → 只勾选“NFO”；图片获取器 → 只勾选“本地”；关闭“TheMovieDb”等在线源。
- **Jellyfin**：库 → 元数据下载器 → 只留“NFO”；图片获取器 → 只留“本地图片”。
- **Kodi**：设置 → 媒体 → 关闭“从互联网获取信息”。
- 标题/简介/官方AI小结/统计（播放弹幕点赞投币收藏）/标签(genre+tag)/UP主(studio+actor)/点赞率评分/合集(收藏夹来源)/封面海报横幅全自动就位；B站CC字幕自动转 srt 外挂挂载。
- **STRM 模式**（`media.link_mode: strm` + `media.public_url`）：不落视频实体，写 `.strm` 指向核心 HTTP 流——Emby/Jellyfin 可部署在另一台机器上直接播放，主程序作为纯数据源。
- 多P投稿：已下载的 P2+ 自动进入条目的“附加内容(extras)”区。

### 对接 teriteri 多端（推荐：管理页一键安装）

- 管理页总览 → **TeriTeri 多端** 卡片 → 一键安装：自动克隆 [teriteri-client](https://github.com/xunlu129/teriteri-client)（用户端）与 [teriteri-admin](https://github.com/xunlu129/teriteri-admin)（管理员端），npm 构建，完成后由核心直接托管在 `:8081`（播放站）与 `:8082`（审核台），API/媒体/弹幕 WS 进程内直通，无需 Docker 或常驻 dev server。需系统已装 git 与 Node.js。
- 也可手动 Docker 部署：`web` service 开箱即用（nginx 已反代 `/api`、`/media`、`/ws`）。
- 站点地址可用 `teriteri.client_addr` / `teriteri.admin_addr` / `teriteri.web_root` 配置。
- 已消失的评论在评论区以 `【已消失·仅存档】` 前缀显式呈现。

## 命令速查

| 命令 | 说明 |
| --- | --- |
| `serve` | 常驻服务（管理端 + teriteri 适配 + 定时同步 + 插件） |
| `add <输入...>` | 立即归档（链接/BV/av/UP主/收藏夹/合集/系列） |
| `sync` | 手动同步全部监控源 |
| `login` | 终端扫码登录 |
| `export-nfo` | 重建媒体库软链接与 NFO |
| `cards [bvid...]` | 为已归档视频生成/刷新快速预览卡片（默认全部） |
| `version` | 版本 |

## 数据目录

```
data/
├─ bili-saver.db        # SQLite（WAL）
├─ cookies.json         # B 站登录态（注意备份与权限）
├─ raw/                 # 【origin】全部 API 原始快照（gzip）
├─ media/               # 【origin】视频/封面/弹幕原件/CC字幕
├─ derived/             # 【derived】插件产物（AI 字幕等）+ {bvid}/card.png 快速预览卡片
├─ library/             # 媒体库桥输出（软链接 + NFO）
└─ plugins/             # 插件安装目录
```

## 插件开发

每个插件一个目录 + `manifest.yaml`：

```yaml
id: my-plugin
version: 0.1.0
entry: [python3, main.py]     # 任意语言的可执行入口
events: [video.downloaded]    # 事件：video.archived/discovered/downloaded/gone,
                              #       subtitle.missing, comments.resynced, daily.tick
timeout_sec: 600
config: {key: value}          # 以事件 JSON 里的 config 传入
```

核心把事件 JSON（含 `context.api_base/token`）写一行到插件 stdin；插件 stdout 每行回传一个 op：

```json
{"op": "save_summary",   "params": {"bvid": "BV...", "content": "...", "model": "..."}}
{"op": "save_subtitle",  "params": {"bvid": "BV...", "cid": "1", "lang": "ai-zh", "content": "srt文本"}}
{"op": "save_tag",       "params": {"bvid": "BV...", "tags": ["a", "b"]}}
{"op": "save_relation",  "params": {"src_type": "video", "src_id": "BV...", "dst_type": "video", "dst_id": "BV...", "relation": "similar"}}
{"op": "save_embedding", "params": {"entity_type": "video", "entity_id": "BV...", "model": "m", "vec": [0.1, ...]}}
{"op": "log",            "params": {"msg": "..."}}
```

**插件只能写 derived 层**——核心提供的 op 全部落到带 origin 的派生表/目录，无法触碰 B 站原始数据。

## 已知边界（诚实声明）

- B 站接口只返回当前可见内容：**消失检测只能覆盖曾经抓到过的评论/弹幕**，从未见过的无法凭空恢复。
- **评论消失检测需要登录态**：未登录时 B 站评论接口只返回部分数据且伪报翻页结束，系统会自动跳过评论消失对账（抓到的数据仍正常入库）；要启用消失检测请先扫码登录。
- 假消失防御：单轮消失占比异常（评论 >30% 且 >50 条）时需连续两轮确认才标记；弹幕接口降级（seg→list 上限 1200 条）时自动跳过对账，防止误标。
- 已 404 的视频无法再抓取；补档的本质是"趁还在先存"。
- **UP 主投稿列表接口（space）被 B 站风控强限制**：未登录时基本必返回 -352/412，添加 UP 主源前请先 `bili-saver login`；收藏夹/合集/系列/单视频**无需登录**即可归档。
- 弹幕优先走分段接口（seg.so），被风控时自动回退旧版整段 XML（list.so，上限约 1200 条最近弹幕）；已消失弹幕同样做对账标记。
- 未登录时清晰度上限一般为 1080P；1080P 高码率/4K 需要登录（4K 需大会员）。
- Plex 不读 NFO，元数据丰富度依赖命名约定；Jellyfin/Emby/Kodi 体验完整。
- MacWhisper 为 macOS GUI 应用，插件通过其 CLI 桥集成，不可用时自动回落 faster-whisper/whisper.cpp。
- teriteri-client 是冻结中的第三方项目：适配层覆盖首页/播放/弹幕/评论/搜索/单用户登录/UP主空间（空间页用归档库真实数据渲染：投稿列表、获赞、播放数）；消息聊天等页面为空数据占位。

## 实现状态（已验证）

以下能力已通过单元测试、mock B 站端到端测试与**真实 B 站接口冒烟测试**（归档 BV1GJ411x7h7：视频 213 秒合并成功、1200 条真实弹幕、34 条真实评论、7 个真实标签、1.05 亿播放量入统计序列、13 份原始快照、NFO+封面媒体库输出、teriteri 适配层全端点）：

- [x] 归档：元数据/分P/标签/简介/封面/官方AI小结/统计时间序列 + 全量原始快照
- [x] 下载：DASH 选流（质量上限、编码策略）、断点续传、备用 CDN、ffmpeg 原子合并
- [x] 弹幕：seg.so→list.so 回退链、protobuf/XML 双格式解析、原始件落盘、full.xml 导出
- [x] 评论：主楼+楼中楼全量翻页、`first_seen/last_seen/deleted_at` 消失检测（只增不删）
- [x] 源监控：收藏夹/合集/系列/UP主（space 需登录）、cron 调度、崩溃恢复任务队列
- [x] 管理端：REST API + 内嵌 Web UI（概览/采集/视频库/源/任务/扫码登录/插件）
- [x] 快速预览卡片：归档/下载后自动渲染 + `GET /api/v1/videos/{bvid}/card` 端点 + `cards` 命令批量补生成（内嵌 HarmonyOS Sans 字体与 B 站风格图标，零运行时依赖）
- [x] teriteri 适配层：getone/danmu-list/comment/search/login/mock-WS + 热搜/搜索计数/收藏夹/评论赞踩/UP主空间/消息占位/IM-WS 保活，已消失评论显式标记；评论对象严格对齐客户端契约（avatar_url、数字型 vip/auth、parentId/toUser），缺失会导致前端渲染冻结
- [x] 媒体库桥：movie/show 布局、Kodi NFO（只写 origin 数据）、symlink→hardlink→copy
- [x] 插件系统：事件总线、进程插件（任意语言）、derived-only 写入、审计；示例插件 whisper-subtitle / ai-summary / ai-tagger / ai-search
- [x] 部署：goreleaser 式交叉编译（linux amd64/arm64、macOS 两架构、Windows）、Docker/compose

规划中（接口已预留）：向量相似检索已有 embeddings 表与 `/api/v1/videos/{bvid}/similar` 端点（由 ai-search 插件供数）；关系网可视化与自动整理依赖 `derived_relations` 表，待前端呈现。

## 开发

```bash
go build ./...          # 编译
go test ./internal/...  # 全部测试（含 mock B 站的端到端测试）
go vet ./...
```

模块结构见 ARCHITECTURE.md §4；teriteri 适配层契约依据源码核实，详见 §7。

## License

MIT
