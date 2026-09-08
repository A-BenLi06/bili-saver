// install.go 一键安装任务：克隆 teriteri-client / teriteri-admin 源码，
// 写入本地化 env（WS 指向核心托管端口），npm install + npm run build。
// 任务在后台 goroutine 执行，状态经 Snapshot 供管理页轮询。
package teriteriweb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	clientRepo = "https://github.com/xunlu129/teriteri-client.git"
	adminRepo  = "https://github.com/xunlu129/teriteri-admin.git"
	// 直连失败时的镜像前缀（国内网络常见 GitHub 不可达）。
	mirrorPrefix = "https://gh-proxy.com/"
	logKeep      = 60
)

// Status 安装任务状态（管理页轮询展示）。
type Status struct {
	Running     bool     `json:"running"`
	Done        bool     `json:"done"`
	Step        string   `json:"step"`
	Progress    float64  `json:"progress"`
	Error       string   `json:"error,omitempty"`
	Logs        []string `json:"logs"`
	ClientReady bool     `json:"client_ready"`
	AdminReady  bool     `json:"admin_ready"`
}

// Installer 单例安装器。
// CarouselItem 首页轮播项（由归档库生成）。
type CarouselItem struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Color  string `json:"color"`
	Target string `json:"target"`
}

type Installer struct {
	mu       sync.Mutex
	st       Status
	webRoot  string
	log      *slog.Logger
	onDone   func()                // 构建成功后回调（启动站点监听）
	Carousel func() []CarouselItem // 归档库封面轮播（client 构建前写入）
	cancel   context.CancelFunc
}

func NewInstaller(webRoot string, log *slog.Logger, onDone func()) *Installer {
	return &Installer{webRoot: webRoot, log: log, onDone: onDone}
}

// clientPatches 让冻结的 teriteri-client 兼容 BV 号作为 vid：
// 原版多处对 vid 做 Number() 转换（原站是数字主键），BV 号会变成 NaN。
var clientPatches = [][2]string{
	{
		`formData.append("vid", Number(this.video.vid));`,
		`formData.append("vid", this.video.vid);`,
	},
	{
		`params: { vid: Number(this.video.vid) },`,
		`params: { vid: this.video.vid },`,
	},
	{
		`formData.append("vid", Number(this.vid));`,
		`formData.append("vid", this.vid);`,
	},
}

// patchClientSources 递归应用 BV 兼容补丁（幂等：找不到目标即跳过）。
func (i *Installer) patchClientSources(srcDir string) (applied int) {
	filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".vue") && !strings.HasSuffix(path, ".js") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		src := string(b)
		changed := false
		for _, p := range clientPatches {
			if strings.Contains(src, p[0]) {
				src = strings.ReplaceAll(src, p[0], p[1])
				changed = true
				applied++
			}
		}
		if changed {
			_ = os.WriteFile(path, []byte(src), 0o644)
		}
		return nil
	})
	return applied
}

// writeCarousel 用归档库封面生成首页轮播（构建期固化进 client）。
func (i *Installer) writeCarousel(srcDir string) {
	if i.Carousel == nil {
		return
	}
	items := i.Carousel()
	if len(items) == 0 {
		i.logf("归档库暂无可用封面，轮播保持原样")
		return
	}
	b, err := json.MarshalIndent(items, "", "    ")
	if err != nil {
		return
	}
	target := filepath.Join(srcDir, "src", "assets", "json", "carousel.json")
	if err := os.WriteFile(target, b, 0o644); err != nil {
		i.logf("写入轮播失败: %v", err)
		return
	}
	i.logf("首页轮播已生成（%d 条归档封面）", len(items))
}

func (i *Installer) ClientDist() string { return filepath.Join(i.webRoot, "client", "dist") }
func (i *Installer) AdminDist() string  { return filepath.Join(i.webRoot, "admin", "dist") }

// Start 启动安装（已在跑则返回错误）。
func (i *Installer) Start(clientAddr string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.st.Running {
		return fmt.Errorf("安装任务进行中")
	}
	// 整体超时：克隆+两套前端依赖安装+构建，网络差时上限 30 分钟
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	i.cancel = cancel
	i.st = Status{Running: true, Step: "准备启动", Logs: []string{}}
	go func() {
		err := i.run(ctx, clientAddr)
		i.mu.Lock()
		i.st.Running = false
		if err != nil {
			i.st.Error = err.Error()
		} else {
			i.st.Done = true
			i.st.Progress = 1
			i.st.Step = "完成"
		}
		i.st.ClientReady = DistExists(i.ClientDist())
		i.st.AdminReady = DistExists(i.AdminDist())
		i.mu.Unlock()
		// 日志与回调必须在 i.mu 之外：logf 会再次加同一把非重入锁，
		// 锁内调用会让 goroutine 持锁冻结，状态接口永久挂起
		if err != nil {
			i.logf("安装失败: %v", err)
			return
		}
		i.logf("TeriTeri 多端构建完成")
		if i.onDone != nil {
			i.onDone()
		}
	}()
	return nil
}

// Snapshot 当前状态快照。
func (i *Installer) Snapshot() Status {
	i.mu.Lock()
	defer i.mu.Unlock()
	s := i.st
	s.Logs = append([]string(nil), i.st.Logs...)
	return s
}

// Stop 中止安装。
func (i *Installer) Stop() {
	i.mu.Lock()
	cancel := i.cancel
	i.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (i *Installer) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	i.log.Info("teriteri-install: " + line)
	i.mu.Lock()
	defer i.mu.Unlock()
	i.st.Logs = append(i.st.Logs, line)
	if len(i.st.Logs) > logKeep {
		i.st.Logs = i.st.Logs[len(i.st.Logs)-logKeep:]
	}
}

func (i *Installer) setStep(p float64, step string) {
	i.mu.Lock()
	i.st.Progress = p
	i.st.Step = step
	i.mu.Unlock()
	i.logf("[%d%%] %s", int(p*100), step)
}

func (i *Installer) run(ctx context.Context, clientAddr string) error {
	if err := os.MkdirAll(i.webRoot, 0o755); err != nil {
		return fmt.Errorf("创建站点目录: %w", err)
	}
	// 子进程（git/npm）的 Dir 与目标路径必须是绝对路径：
	// 相对路径会以 webRoot 为 cwd 再次拼接，克隆进嵌套的错误位置
	abs, err := filepath.Abs(i.webRoot)
	if err != nil {
		return err
	}
	i.webRoot = abs
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("未找到 git，请先安装并加入 PATH")
	}
	npmWrap := npmRunner()
	if _, err := npmWrap.lookPath(); err != nil {
		return fmt.Errorf("未找到 npm，请先安装 Node.js 并加入 PATH")
	}

	type app struct {
		name string
		repo string
	}
	for idx, a := range []app{{"client", clientRepo}, {"admin", adminRepo}} {
		srcDir := filepath.Join(i.webRoot, a.name)
		base := 0.05
		if idx == 1 {
			base = 0.5
		}
		if _, err := os.Stat(filepath.Join(srcDir, "package.json")); err != nil {
			i.setStep(base+0.02, "克隆 "+a.name+" 源码")
			if err := i.clone(ctx, srcDir, a.repo); err != nil {
				return err
			}
		} else {
			i.logf("%s 源码已存在，跳过克隆", a.name)
		}
		if a.name == "client" {
			i.setStep(base+0.12, "写入本地化 env")
			if err := i.writeClientEnv(clientAddr); err != nil {
				return fmt.Errorf("写入 env: %w", err)
			}
			i.setStep(base+0.16, "应用 BV 兼容补丁 + 生成归档轮播")
			if n := i.patchClientSources(srcDir); n > 0 {
				i.logf("BV 兼容补丁：%d 处", n)
			}
			i.writeCarousel(srcDir)
		}
		if _, err := os.Stat(filepath.Join(srcDir, "node_modules")); err != nil {
			i.setStep(base+0.2, "npm install "+a.name+"（数分钟）")
			if err := npmWrap.run(ctx, srcDir, "install", "--no-audit", "--no-fund"); err != nil {
				return fmt.Errorf("npm install %s: %w", a.name, err)
			}
		} else {
			i.logf("%s 依赖已存在，跳过 npm install", a.name)
		}
		i.setStep(base+0.4, "npm run build "+a.name+"（数分钟）")
		if err := npmWrap.run(ctx, srcDir, "run", "build"); err != nil {
			return fmt.Errorf("npm build %s: %w", a.name, err)
		}
	}
	return nil
}

// clone 深度克隆；GitHub 直连失败自动走镜像。
func (i *Installer) clone(ctx context.Context, dst, repo string) error {
	if err := runCmd(ctx, i.webRoot, "git", "clone", "--depth", "1", repo, dst); err != nil {
		i.logf("直连克隆失败（%v），改用镜像重试", err)
		if err2 := runCmd(ctx, i.webRoot, "git", "clone", "--depth", "1", mirrorPrefix+repo, dst); err2 != nil {
			_ = os.RemoveAll(dst)
			return fmt.Errorf("克隆 %s: %w", repo, err2)
		}
	}
	return nil
}

// writeClientEnv 用户端的 WS 地址在构建期固化：指向核心托管的用户端站点，
// 由二级监听在进程内转发给主服务的 /ws、/im。
func (i *Installer) writeClientEnv(clientAddr string) error {
	host := "127.0.0.1" + clientPort(clientAddr)
	env := "VUE_APP_WS_DANMU_URL=ws://" + host + "\nVUE_APP_WS_IM_URL=ws://" + host + "\n"
	for _, name := range []string{".env.production", ".env.development"} {
		if err := os.WriteFile(filepath.Join(i.webRoot, "client", name), []byte(env), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func clientPort(addr string) string {
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		return addr[idx:]
	}
	return addr
}

// ---- 命令执行 ----

type npmCmd struct{ windows bool }

func npmRunner() npmCmd { return npmCmd{windows: runtime.GOOS == "windows"} }

func (n npmCmd) lookPath() (string, error) {
	if n.windows {
		return exec.LookPath("npm.cmd")
	}
	return exec.LookPath("npm")
}

func (n npmCmd) run(ctx context.Context, dir string, args ...string) error {
	if n.windows {
		return runCmd(ctx, dir, append([]string{"cmd", "/c", "npm"}, args...)...)
	}
	return runCmd(ctx, dir, append([]string{"npm"}, args...)...)
}

func runCmd(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > 800 {
			tail = tail[len(tail)-800:]
		}
		return fmt.Errorf("%s: %w\n%s", strings.Join(args, " "), err, tail)
	}
	return nil
}
