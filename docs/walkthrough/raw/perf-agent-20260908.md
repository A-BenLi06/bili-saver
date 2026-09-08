## 2026-09-08T08:18:14Z perf-agent — 下载提速：修 merge 无音轨 bug + 60s 超时 + 分段并行下载
- 做了什么：诊断"下载慢"——主因队列积压+单连接 ~1.5MB/s+60s 总超时掐断大流+无音轨 merge 必失败（job #276/#667）。修复：downloadPage 空音轨路径；biliapi.DownloadClient()（无总超时、jar 共享、30s 空闲 watchdog）；分段并行 Range 下载（.segs 断点+稀疏预分配+镜像逐段重试）；音视频流并行；新增 download.segments（默认 4）全链路；分段流量接入 metrics。config.yaml concurrency 2→4。
- 涉及：internal/downloader/downloader.go（主体）、internal/biliapi/client.go、internal/config/config.go、internal/server/admin.go（handleConfig 暴露 segments）、config.example.yaml、config.yaml、internal/downloader/segment_test.go（新，5 用例）
- 结果/状态：实测 107MB 3.3s（≈32MB/s，约 20 倍），md5 与旧产物一致，无音轨视频重下成功；go build ./... + vet + test 12 包全绿；服务已重启。done
## 2026-09-08T09:05:28Z perf-agent — teriteri 前端 JSON 登录修复
- 做了什么：8081 前端 admin/bili-saver 登录报"账号或密码不能为空"。定位：前端 LoginRegister.vue axios.post 发 JSON，适配层 login 只认表单（FormValue）→解析为空。修复 login 在表单为空时回退 json.Decoder（username/email/password），新增 firstStr；表单路径保持不变。
- 涉及：internal/adapter/teriteri/adapter.go、adapter_test.go（TestLoginJSON 回归）
- 结果/状态：8081 实测 JSON 登录 200+token、表单兼容、错误密码提示正确；全量 build/vet/test 绿；服务已重启。done
## 2026-09-08T18:01:09Z perf-agent — 全平台兼容性审计 + Linux 实测验证
- 做了什么：按"全平台兼容、尤其 Linux"要求审计：纯 Go 无 CGO（modernc/sqlite）、零平台分支代码（仅 teriteriweb npm runner 一处 GOOS 判断）、信号处理可移植、路径全 filepath/URL 用正斜杠、linkFile 三级降级链设计正确。实测：五平台交叉编译全过；linux/amd64 下 16 个测试二进制在 WSL2 Debian 全绿；真实服务端到端跑通（登录态跨平台复用→归档→下载 166830B 视频流→ffmpeg 合并 mp4→媒体库 movie 布局落盘）。发现两处 Linux 部署注意点（ffmpeg 硬依赖、-config 文件必须存在）已写入 ARCHITECTURE §11。
- 涉及：无代码改动（审计+验证）；ARCHITECTURE.md §11 追加实测记录
- 结果/状态：五平台构建 OK + Linux 运行时实测通过；已验证中途一个"失败"实为 WSL 未装 ffmpeg（错误信息按设计可读）。done
