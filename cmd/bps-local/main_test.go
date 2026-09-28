package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"bpslocal/internal/localconfig"
	"bpslocal/internal/relayconfig"
)

func TestBPSModelIsFixed(t *testing.T) {
	if fixedModelID != "gpt-6-astra" {
		t.Fatalf("BPS model must be fixed to gpt-6-astra, got %q", fixedModelID)
	}
}

func TestProbeCooldownBlocksRepeatedUpstreamRequest(t *testing.T) {
	c := &controller{lastProbe: time.Now()}
	if _, err := c.test(); err == nil || !strings.Contains(err.Error(), "BPS 风控") {
		t.Fatalf("repeated probe was not blocked: %v", err)
	}
}

func TestInterruptDuringProbeExitsAfterCleanupWithoutError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows exits through stdin EOF; covered separately")
	}
	// 保持模拟连接未完成，避免“端口拒绝”先于 SIGINT 返回导致调度相关误报。
	release := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer proxy.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	state := filepath.Join(root, "state")
	if e := os.MkdirAll(home, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte("model='test-model'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":4102444800,"https://api.openai.com/auth":{"chatgpt_account_id":"fake"}}`)) + ".signature"
	auth := `{"auth_mode":"chatgpt","tokens":{"access_token":"` + token + `","account_id":"fake"}}`
	if e := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestEngineHelper", "--", "--codex-home", home, "--state-dir", state)
	// 假账号仅供进程测试；所有连接强制指向本机模拟代理，绝不访问真实上游。
	cmd.Env = append(os.Environ(), "BPS_ENGINE_TEST_HELPER=1", "HTTPS_PROXY="+proxy.URL, "https_proxy="+proxy.URL, "NO_PROXY=", "no_proxy=")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { stdin.Close(); cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("no initial state")
	}
	if _, e := io.WriteString(stdin, "{\"action\":\"probe\"}\n"); e != nil {
		t.Fatal(e)
	}
	if !scanner.Scan() || !strings.Contains(scanner.Text(), `"phase":"testing"`) {
		t.Fatal("no testing state")
	}
	if e := cmd.Process.Signal(syscall.SIGINT); e != nil {
		t.Fatal(e)
	}
	var remaining string
	for scanner.Scan() {
		remaining += scanner.Text() + "\n"
	}
	if strings.Contains(remaining, `"phase":"error"`) {
		t.Fatal("user cancellation incorrectly reported as error:", remaining)
	}
	if !strings.Contains(remaining, `"phase":"stopped"`) {
		t.Fatal("missing cleanup confirmation")
	}
}

func TestStdinEOFDuringProbeCancelsAndExits(t *testing.T) {
	// 代理只挂起本机 CONNECT，确保取消不依赖真实网络或远端响应。
	release := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer proxy.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	state := filepath.Join(root, "state")
	if e := os.MkdirAll(home, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte("model='test-model'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":4102444800,"https://api.openai.com/auth":{"chatgpt_account_id":"fake"}}`)) + ".signature"
	auth := `{"auth_mode":"chatgpt","tokens":{"access_token":"` + token + `","account_id":"fake"}}`
	if e := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestEngineHelper", "--", "--codex-home", home, "--state-dir", state)
	// 假账号仅供进程测试；所有连接强制指向不可用的环回代理，绝不访问真实上游。
	cmd.Env = append(os.Environ(), "BPS_ENGINE_TEST_HELPER=1", "HTTPS_PROXY="+proxy.URL, "https_proxy="+proxy.URL, "NO_PROXY=", "no_proxy=")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { stdin.Close(); cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("no initial state")
	}
	if _, e := io.WriteString(stdin, "{\"action\":\"probe\"}\n"); e != nil {
		t.Fatal(e)
	}
	if !scanner.Scan() || !strings.Contains(scanner.Text(), `"phase":"testing"`) {
		t.Fatal("no testing state")
	}
	if e := stdin.Close(); e != nil {
		t.Fatal(e)
	}
	var remaining string
	for scanner.Scan() {
		remaining += scanner.Text() + "\n"
	}
	if strings.Contains(remaining, `"phase":"error"`) {
		t.Fatal("user cancellation incorrectly reported as error:", remaining)
	}
	if !strings.Contains(remaining, `"phase":"stopped"`) {
		t.Fatal("missing cleanup confirmation")
	}
}

// 通过真实子进程检查启动恢复，避免窗口显示空闲但配置仍指向上次死亡端口。
func TestLaunchRecoversBackupBeforeIdle(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	state := filepath.Join(root, "state")
	if e := os.MkdirAll(home, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(home, "config.toml")
	original := []byte("model='gpt-6-astra'\n")
	if e := os.WriteFile(path, original, 0600); e != nil {
		t.Fatal(e)
	}
	m := &localconfig.Manager{ConfigPath: path, StateDir: state}
	if e := m.Enable(17861, "old-key"); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestEngineHelper", "--", "--codex-home", home, "--state-dir", state)
	cmd.Env = append(os.Environ(), "BPS_ENGINE_TEST_HELPER=1")
	stdin, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { stdin.Close(); cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("engine emitted no initial state")
	}
	var event Event
	if e = json.Unmarshal(scanner.Bytes(), &event); e != nil {
		t.Fatal(e)
	}
	if event.Phase != phaseIdle {
		t.Fatalf("not ready: %s", scanner.Text())
	}
	restored, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if string(restored) != string(original) {
		t.Fatal("idle engine left old provider active")
	}
}
func TestEngineHelper(t *testing.T) {
	if os.Getenv("BPS_ENGINE_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	main()
	os.Exit(0)
}
func TestStartProvidesRelayWithoutChangingCodexConfig(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	root := t.TempDir()
	config := filepath.Join(root, "config.toml")
	auth := filepath.Join(root, "auth.json")
	original := []byte("model='test-model'\n")
	if err := os.WriteFile(config, original, 0600); err != nil {
		t.Fatal(err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":4102444800,"https://api.openai.com/auth":{"chatgpt_account_id":"fake"}}`)) + ".sig"
	if err := os.WriteFile(auth, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+token+`","account_id":"fake"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c := &controller{settings: &relayconfig.Settings{Port: 17863, APIKey: "test-key"}, ctx: ctx, manager: &localconfig.Manager{ConfigPath: config, StateDir: filepath.Join(root, "state")}, authPath: auth, model: "test-model", out: &output{}}
	defer c.stop()
	if err := c.start(); err != nil {
		t.Fatalf("local start must not probe remote: %v", err)
	}
	actual, err := os.ReadFile(config)
	if err != nil || string(actual) != string(original) {
		t.Fatal("relay start changed Codex provider")
	}
	if c.server == nil {
		t.Fatal("relay not listening")
	}
	if _, err := os.Stat(c.manager.BackupPath()); !os.IsNotExist(err) {
		t.Fatal("new relay must not create provider backup")
	}
}

func TestStartupFailureKeepsErrorAsLastState(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "relay.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestEngineHelper", "--", "--codex-home", root, "--state-dir", state)
	cmd.Env = append(os.Environ(), "BPS_ENGINE_TEST_HELPER=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var final Event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &final); err != nil {
		t.Fatal(err)
	}
	if final.Phase != phaseError {
		t.Fatalf("startup error overwritten by %s", final.Phase)
	}
}
