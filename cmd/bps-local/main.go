package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"bpslocal/internal/gateway"
	"bpslocal/internal/identity"
	"bpslocal/internal/localconfig"
	"bpslocal/internal/relayconfig"
)

type Phase string

// BPS 原版协议只验证这一模型；界面和本地路由不允许切换到其他模型。
const fixedModelID = "gpt-6-astra"

const (
	phaseIdle    Phase = "idle"
	phaseTesting Phase = "testing"
	phaseEnabled Phase = "enabled"
	phaseStopped Phase = "stopped"
	phaseError   Phase = "error"
)

type Event struct {
	BaseURL  string          `json:"base_url,omitempty"`
	APIKey   string          `json:"api_key,omitempty"`
	Type     string          `json:"type"`
	Phase    Phase           `json:"phase,omitempty"`
	Message  string          `json:"message,omitempty"`
	Account  string          `json:"account,omitempty"`
	Model    string          `json:"model,omitempty"`
	Port     int             `json:"port,omitempty"`
	Requests int64           `json:"requests"`
	Backup   string          `json:"backup,omitempty"`
	Result   *gateway.Result `json:"result,omitempty"`
}
type output struct{ mu sync.Mutex }

func (o *output) send(e Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_ = json.NewEncoder(os.Stdout).Encode(e)
}

type controller struct {
	settings *relayconfig.Settings
	baseURL  string
	ctx      context.Context
	manager  *localconfig.Manager
	authPath string
	model    string
	out      *output
	server   *http.Server
	count    atomic.Int64
}

func (c *controller) status(p Phase, msg string) {
	event := Event{Type: "state", Phase: p, Message: msg, Model: c.model, Requests: c.count.Load(), Backup: c.manager.BackupPath()}
	if c.settings != nil {
		event.BaseURL = c.baseURL
		event.APIKey = c.settings.APIKey
	}
	c.out.send(event)
}

// 探测仅发送固定文本；真实聊天内容与上游错误正文均不进入控制台日志。
type probeWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *probeWriter) Header() http.Header    { return w.header }
func (w *probeWriter) WriteHeader(status int) { w.status = status }
func (w *probeWriter) Write(p []byte) (int, error) {
	if w.body.Len()+len(p) > 1<<20 {
		return 0, errors.New("probe response too large")
	}
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(p)
}
func probe(ctx context.Context, authPath, model string) (identity.Account, error) {
	a, e := identity.Read(authPath)
	if e != nil {
		return a, e
	}
	body, _ := json.Marshal(map[string]interface{}{"model": model, "input": "Reply with exactly BPS_LOCAL_OK. Do not use tools.", "stream": false, "reasoning": map[string]string{"effort": "low"}})
	r, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:17861/v1/responses", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer probe")

	w := &probeWriter{header: make(http.Header)}
	gateway.New("probe", model, func() (identity.Account, error) { return identity.Read(authPath) }, nil).ServeHTTP(w, r)
	if w.status != 200 {
		var v struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.body.Bytes(), &v)
		if v.Error.Message != "" {
			return a, errors.New(v.Error.Message)
		}
		return a, fmt.Errorf("BPS 测试未完成（HTTP %d）", w.status)
	}
	var response struct {
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(w.body.Bytes(), &response) != nil || response.Status != "completed" {
		return a, errors.New("上游响应没有成功完成")
	}
	for _, item := range response.Output {
		for _, part := range item.Content {
			if part.Type == "output_text" && strings.Contains(part.Text, "BPS_LOCAL_OK") {
				return a, nil
			}
		}
	}
	return a, errors.New("上游已返回，但未通过固定文本检查")
}
func (c *controller) test() (identity.Account, error) {
	c.model = fixedModelID
	c.status(phaseTesting, "正在验证当前账号的 BPS 连接…")
	ctx, cancel := context.WithTimeout(c.ctx, 60*time.Second)
	defer cancel()
	return probe(ctx, c.authPath, c.model)
}
func (c *controller) start() error {
	c.model = fixedModelID
	if c.server != nil {
		c.status(phaseEnabled, "本地中转已启动，请将地址和 API Key 填入 AiMaMi")
		return nil
	}
	if c.settings == nil {
		settings, err := relayconfig.Load(c.manager.StateDir)
		if err != nil {
			return err
		}
		c.settings = settings
	}
	account, err := identity.Read(c.authPath)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", c.settings.Port))
	if err != nil {
		return fmt.Errorf("本地端口 %d 无法监听；请退出旧版工具，或在 relay.json 修改端口", c.settings.Port)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	c.baseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", port)
	// 每个请求重新读取账号，AiMaMi 切换/刷新账号后无需把令牌交给客户端配置。
	g := gateway.New(c.settings.APIKey, c.model, func() (identity.Account, error) { return identity.Read(c.authPath) }, func(r gateway.Result) {
		if r.Success {
			c.count.Add(1)
		}
		c.out.send(Event{Type: "request", Result: &r, Requests: c.count.Load()})
	})
	c.server = &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	server := c.server
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.status(phaseError, "本地监听意外停止，请退出后重新启动")
		}
	}()
	c.out.send(Event{Type: "state", Phase: phaseEnabled, Account: account.MaskedEmail, Model: c.model, Port: port, BaseURL: c.baseURL, APIKey: c.settings.APIKey, Requests: c.count.Load(), Message: "本地中转已启动；尚未验证上游。将地址和 API Key 填入 AiMaMi，并选择固定模型 gpt-6-astra。"})
	return nil
}
func (c *controller) stop() error {
	if c.server != nil {
		if err := c.server.Close(); err != nil {
			return err
		}
		c.server = nil
	}
	c.status(phaseStopped, "本地中转已停止；AiMaMi 中保存的地址和 API Key 可在下次启动后继续使用。")
	return nil
}
func main() {
	home, _ := os.UserHomeDir()
	defaultCodex := os.Getenv("CODEX_HOME")
	if defaultCodex == "" {
		defaultCodex = filepath.Join(home, ".codex")
	}
	codexHome := flag.String("codex-home", defaultCodex, "Codex configuration directory")
	stateDir := flag.String("state-dir", defaultStateDir(home), "private state directory")
	probeOnly := flag.Bool("probe", false, "probe without changing configuration")
	restoreOnly := flag.Bool("restore", false, "restore configuration and exit")
	flag.Parse()
	out := &output{}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	config := filepath.Join(*codexHome, "config.toml")
	c := &controller{ctx: ctx, manager: &localconfig.Manager{ConfigPath: config, StateDir: *stateDir}, authPath: filepath.Join(*codexHome, "auth.json"), model: fixedModelID, out: out}
	if *probeOnly {
		a, e := c.test()
		if e != nil {
			c.status(phaseError, e.Error())
			os.Exit(1)
		}
		out.send(Event{Type: "probe", Account: a.MaskedEmail, Model: c.model, Message: "BPS 连通性验证通过；未修改 Codex 配置"})
		return
	}
	if e := os.MkdirAll(*stateDir, 0700); e != nil {
		c.status(phaseError, "无法创建应用数据目录")
		return
	}
	lock, e := acquireProcessLock(filepath.Join(*stateDir, "app.lock"))
	if e != nil {
		c.status(phaseError, "无法获取进程锁；星桥或旧版 BPS Local 可能已在运行，也请检查数据目录权限")
		return
	}
	defer lock.Close()
	if *restoreOnly {
		if e = c.manager.Restore(); e != nil {
			c.status(phaseError, e.Error())
			os.Exit(1)
		}
		return
	}
	if e := c.manager.Restore(); e != nil {
		c.status(phaseError, e.Error())
		return
	}
	settings, e := relayconfig.Load(*stateDir)
	if e != nil {
		c.status(phaseError, e.Error())
		return
	}
	c.settings = settings
	c.baseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", settings.Port)
	defer func() {
		if e := c.stop(); e != nil {
			c.status(phaseError, e.Error())
		}
	}()
	c.status(phaseIdle, "本地中转准备就绪；启动服务不会访问上游或改写 Codex 配置")
	type command struct {
		Action string `json:"action"`
		Model  string `json:"model"`
	}
	commands := make(chan command)
	go func() {
		defer close(commands)
		// 桌面窗口关闭管道时立即取消远端请求，Windows 无需依赖 Unix 信号。
		defer cancel()
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			var cmd command
			if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
				continue
			}
			select {
			case commands <- cmd:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-commands:
			if !ok {
				return
			}
			// 客户端命令中的 model 字段仅为旧版本兼容，BPS 始终使用固定模型。
			c.model = fixedModelID
			switch cmd.Action {
			case "start":
				e = c.start()
			case "stop":
				e = c.stop()
			case "probe":
				var a identity.Account
				a, e = c.test()
				if e == nil {
					p := phaseIdle
					if c.server != nil {
						p = phaseEnabled
					}
					out.send(Event{Type: "state", Phase: p, Account: a.MaskedEmail, Model: c.model, Message: "BPS 连通性验证通过；这不代表模型质量评测", Requests: c.count.Load()})
				}
			case "quit":
				if e = c.stop(); e == nil {
					return
				}
			default:
				e = errors.New("不支持的操作")
			}
			if e != nil {
				// 用户关闭窗口导致的主动取消直接进入清理；仅恢复失败才阻止退出。
				if ctx.Err() != nil {
					return
				}
				c.status(phaseError, e.Error())
				e = nil
			}
		}
	}
}
