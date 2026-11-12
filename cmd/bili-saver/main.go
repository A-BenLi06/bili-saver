// bili-saver —— Bilibili 补档机。
//
// 子命令：
//
//	bili-saver serve                 启动常驻服务（admin + teriteri 适配 + 调度）
//	bili-saver add <输入...>          立即归档（视频/BV/UP主/收藏夹/合集）
//	bili-saver sync                  手动触发全部监控源同步
//	bili-saver login                 终端扫码登录 B 站
//	bili-saver export-nfo            重建媒体库软链接与 NFO
//	bili-saver cards                 为全部已归档视频生成/刷新快速预览卡片
//	bili-saver version               版本
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"bili-saver/internal/app"
	"bili-saver/internal/archiveguard"
	"bili-saver/internal/archiveops"
	"bili-saver/internal/config"
	"bili-saver/internal/jobs"
	"bili-saver/internal/logging"
	"bili-saver/internal/metrics"
	"bili-saver/internal/resolver"
	"bili-saver/internal/store"
	"encoding/json"

	"github.com/skip2/go-qrcode"
)

var version = "0.1.0"

type commandFailure struct{ err error }

func main() {
	code := 0
	func() {
		defer func() {
			if value := recover(); value != nil {
				if failure, ok := value.(commandFailure); ok {
					fmt.Fprintln(os.Stderr, "Error:", failure.err)
					code = 1
				} else {
					panic(value)
				}
			}
		}()
		run()
	}()
	if code != 0 {
		os.Exit(code)
	}
}
func run() {
	configPath := flag.String("config", "", "配置文件路径（默认 ./config.yaml，可用 BILI_SAVER_CONFIG 覆盖）")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "serve", "add", "sync", "login", "export-nfo", "cards", "version", "check", "backup", "restore", "relocate", "catalog":
	default:
		usage()
		os.Exit(2)
	}
	if args[0] == "version" {
		fmt.Println("bili-saver", version)
		return
	}
	if args[0] == "restore" {
		if len(args) != 3 {
			fatal(fmt.Errorf("usage: restore BACKUP NEW_DIRECTORY"))
		}
		if err := archiveops.Restore(context.Background(), args[1], args[2]); err != nil {
			fatal(err)
		}
		fmt.Println("Restored and verified archive; review config.yaml in the new directory before starting")
		return
	}
	cfgFile := *configPath
	if cfgFile == "" {
		cfgFile = os.Getenv("BILI_SAVER_CONFIG")
	}
	if cfgFile == "" {
		cfgFile = "./config.yaml"
	}
	cfg, err := config.Load(cfgFile)
	if err != nil {
		fatal(err)
	}
	switch args[0] {
	case "check":
		report, err := archiveops.Check(context.Background(), cfg.DataDir)
		if err != nil {
			fatal(err)
		}
		printResult(report)
		if !report.DatabaseOK || len(report.Issues) > 0 {
			os.Exit(1)
		}
		return
	case "backup":
		if len(args) != 2 {
			fatal(fmt.Errorf("usage: backup NEW_DIRECTORY"))
		}
		manifest, err := archiveops.BackupWithConfig(context.Background(), cfg, args[1])
		if err != nil {
			fatal(err)
		}
		printResult(manifest)
		return
	case "relocate":
		if len(args) != 2 {
			fatal(fmt.Errorf("usage: relocate NEW_DIRECTORY"))
		}
		if err := archiveops.RelocateWithConfig(context.Background(), cfg, args[1]); err != nil {
			fatal(err)
		}
		fmt.Println("Verified archive copy created; source retained. Review destination config.yaml before starting")
		return
	case "catalog":
		lock, err := archiveguard.Acquire(cfg.DataDir)
		if err != nil {
			fatal(err)
		}
		defer lock.Close()
		catalog, err := archiveops.ReadCatalog(context.Background(), cfg.DataDir)
		if err != nil {
			fatal(err)
		}
		printResult(catalog)
		return
	}
	log, err := logging.Setup(cfg.Log.Level, cfg.Log.File, cfg.DataDir)
	if err != nil {
		fatal(err)
	}
	// Initialize once, before any client or worker starts using the default pool.
	http.DefaultTransport = metrics.InstrumentTransport(http.DefaultTransport.(*http.Transport))
	a, err := app.New(cfg, log)
	if err != nil {
		fatal(err)
	}
	defer a.Close()
	a.CfgPath = cfgFile // 设置页写盘用（serve 子命令）

	switch args[0] {
	case "serve":
		runServe(cfg, a)
	case "add":
		runAdd(a, args[1:])
	case "sync":
		runSync(a)
	case "login":
		runLogin(cfg, a)
	case "export-nfo":
		runExportNFO(a)
	case "cards":
		runCards(a, args[1:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`bili-saver ` + version + ` —— Bilibili 补档机

用法:
  bili-saver [-config 文件] <命令> [参数]

命令:
  serve                 启动常驻服务（管理端 / teriteri 前端适配 / 定时同步）
  add <输入...>          立即归档：视频链接、BV号、UP主主页、收藏夹、合集
  sync                  手动同步全部监控源
  login                 终端扫码登录 B 站
  export-nfo            重建 Emby/Plex/Jellyfin/Kodi 媒体库软链接
  cards [bvid...]       生成快速预览卡片（默认全部已归档视频；指定 bvid 只处理这些）
  check                 Verify archive files and database without migration
  backup NEW_DIR        Create a verified private backup (service stopped)
  restore BACKUP NEW_DIR Restore to a new directory; regenerate local paths
  relocate NEW_DIR      Create a verified portable copy; retain the source
  catalog               Export a versioned content-only asset catalog
  version               显示版本

配置:
  默认读取 ./config.yaml；环境变量 BILI_SAVER_<字段> 可覆盖（如 BILI_SAVER_LISTEN=:8080）
`)
}

func fatal(err error) {
	panic(commandFailure{err})
}

func runServe(cfg *config.Config, a *app.App) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Mgr.Start(ctx); err != nil {
		fatal(err)
	}
	go (&jobs.Scheduler{
		St: a.St, Mgr: a.Mgr, Log: a.Log,
		Cfg: cfg, // 活读取：设置页保存后下一 tick 生效
	}).Run(ctx)
	if err := a.Serve(ctx); err != nil {
		fatal(err)
	}
	a.Mgr.Stop()
}

func srcOf(in *resolver.Input) *store.Source {
	return &store.Source{
		Type: string(in.Type), ExtID: in.ExtID, Extra: in.ExtraJSON(),
		Title: in.String(), Watch: true, CreatedAt: store.Now(),
	}
}

func runAdd(a *app.App, inputs []string) {
	if len(inputs) == 0 {
		fatal(fmt.Errorf("用法: bili-saver add <输入...>"))
	}
	ctx := context.Background()
	for _, raw := range inputs {
		in, err := resolver.Normalize(ctx, raw)
		if err != nil {
			fmt.Println("跳过:", err)
			continue
		}
		fmt.Println(">", in)
		if in.Type == resolver.TypeVideo {
			res, err := a.Col.ArchiveVideo(ctx, in.ExtID)
			if err != nil {
				fmt.Println("  归档失败:", err)
				continue
			}
			fmt.Printf("  归档完成 %s（评论 +%d，消失 %d）\n", res.Bvid, res.CommentsSeen, res.CommentsDeleted)
			if a.Cfg.Download.AutoDownload {
				fmt.Println("  下载视频…")
				err := a.Dl.DownloadVideo(ctx, res.Bvid, func(f float64, detail string) {
					fmt.Printf("\r  下载 %3d%% %-20s", int(f*100), detail)
				})
				if err != nil {
					fmt.Println("\n  下载失败:", err)
				} else {
					fmt.Println("\n  下载完成")
				}
			}
			continue
		}
		// 非视频源：登记并展开归档
		if _, err := a.St.UpsertSource(srcOf(in)); err != nil {
			fatal(err)
		}
		exp, err := a.ExpandSource(ctx, srcOf(in))
		if err != nil {
			fmt.Println("  展开失败:", err)
			continue
		}
		fmt.Printf("  共 %d 个视频，开始逐个归档\n", len(exp.Bvids))
		okCnt := 0
		for i, bvid := range exp.Bvids {
			if _, err := a.Col.ArchiveVideo(ctx, bvid); err != nil {
				fmt.Printf("  [%d/%d] %s 失败: %v\n", i+1, len(exp.Bvids), bvid, err)
				continue
			}
			okCnt++
			fmt.Printf("\r  [%d/%d] 完成 %d", i+1, len(exp.Bvids), okCnt)
		}
		fmt.Println()
	}
	// 短生命周期进程：等异步插件派发完成再退出
	if err := a.DrainDeliveries(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	a.Bus.Wait()
}

func runSync(a *app.App) {
	ctx := context.Background()
	srcs, err := a.St.ListSources()
	if err != nil {
		fatal(err)
	}
	for _, src := range srcs {
		if !src.Watch {
			continue
		}
		fmt.Println("同步:", src.Title)
		exp, err := a.ExpandSource(ctx, src)
		if err != nil {
			fmt.Println("  失败:", err)
			continue
		}
		for i, bvid := range exp.Bvids {
			if _, err := a.Col.ArchiveVideo(ctx, bvid); err != nil {
				fmt.Printf("  [%d/%d] %s 失败: %v\n", i+1, len(exp.Bvids), bvid, err)
			}
		}
		_ = a.St.SetSourceSynced(src.ID, time.Now().Unix())
	}
}

func runLogin(cfg *config.Config, a *app.App) {
	ctx := context.Background()
	qr, err := a.API.QRGenerate(ctx)
	if err != nil {
		fatal(err)
	}
	if q, err := qrcode.New(qr.URL, qrcode.Low); err == nil {
		fmt.Println(q.ToString(true))
	}
	fmt.Println("用 B 站 App 扫描上方二维码（或打开链接手动扫码）:")
	fmt.Println(qr.URL)
	for i := 0; i < 120; i++ {
		time.Sleep(2 * time.Second)
		res, err := a.API.QRPoll(ctx, qr.QRCodeKey)
		if err != nil {
			fatal(err)
		}
		switch res.Code {
		case 0:
			if err := a.API.SaveCookies(cfg.CookiePath()); err != nil {
				fatal(err)
			}
			fmt.Println("✅ 登录成功，cookie 已保存到", cfg.CookiePath())
			return
		case 86090:
			fmt.Println("已扫码，请在手机上确认…")
		case 86101:
			// 未扫码，继续等
		default:
			fmt.Println("状态:", res.Code, res.Message)
			return
		}
	}
	fmt.Println("二维码超时，请重试")
}

func runExportNFO(a *app.App) {
	res, err := a.Med.Rebuild()
	if err != nil {
		fatal(err)
	}
	fmt.Printf("媒体库重建完成：创建 %d，跳过 %d，错误 %d\n", res.Created, res.Skipped, len(res.Errors))
	for _, e := range res.Errors {
		fmt.Println(" -", e)
	}
}

// runCards 为已归档视频生成快速预览卡片（derived 层，供分享功能使用）。
func runCards(a *app.App, bvids []string) {
	ctx := context.Background()
	if len(bvids) > 0 {
		okCnt := 0
		for _, bvid := range bvids {
			path, err := a.Card.RenderToFile(ctx, bvid)
			if err != nil {
				fmt.Println("失败:", bvid, err)
				continue
			}
			okCnt++
			fmt.Println("✓", bvid, "->", path)
		}
		fmt.Printf("完成：%d/%d\n", okCnt, len(bvids))
		return
	}
	videos, err := a.St.ListVideos(0, "", 0, 0)
	if err != nil {
		fatal(err)
	}
	okCnt := 0
	for i, v := range videos {
		if _, err := a.Card.RenderToFile(ctx, v.Bvid); err != nil {
			fmt.Printf("[%d/%d] %s 失败: %v\n", i+1, len(videos), v.Bvid, err)
			continue
		}
		okCnt++
		fmt.Printf("\r[%d/%d] 完成 %d", i+1, len(videos), okCnt)
	}
	fmt.Printf("\n卡片生成完成：%d/%d（输出于 %s）\n", okCnt, len(videos), filepath.Join(a.Cfg.DataDir, "derived"))
}

func printResult(v any) {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		fatal(err)
	}
}
