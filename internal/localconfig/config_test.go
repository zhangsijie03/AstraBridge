package localconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, original string) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	return &Manager{ConfigPath: path, StateDir: filepath.Join(root, "state")}, path
}

func TestPendingBackupAfterConflictDoesNotBlockRetry(t *testing.T) {
	m, p := fixture(t, "model='gpt-6-astra'\n")
	// 模拟写入待提交备份后，编辑器修改配置、进程退出的真实磁盘状态。
	s := backup{ConfigPath: p, Original: []byte("model='gpt-6-astra'\n"), Applied: []byte("unused"), Block: "\n# managed block", HadFile: true}
	raw, _ := json.Marshal(s)
	if e := AtomicWrite(m.BackupPath(), raw); e != nil {
		t.Fatal(e)
	}
	changed := "model='gpt-6-sol'\n"
	if e := os.WriteFile(p, []byte(changed), 0600); e != nil {
		t.Fatal(e)
	}
	if e := m.Enable(17861, "fresh"); e != nil {
		t.Fatal(e)
	}
	if e := m.Restore(); e != nil {
		t.Fatal(e)
	}
	if read(t, p) != changed {
		t.Fatal("lost concurrent editor change")
	}
}
func TestCommitRejectsStaleContent(t *testing.T) {
	_, p := fixture(t, "model='new'\n")
	if e := commitConfig(p, []byte("model='old'\n"), true, []byte("model='restored'\n"), false); e == nil {
		t.Fatal("overwrote a concurrent edit")
	}
	if read(t, p) != "model='new'\n" {
		t.Fatal("changed file despite conflict")
	}
}
func read(t *testing.T, path string) string {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

// 丢失旧 provider 或恢复时覆盖无关编辑，都会破坏用户已有客户端配置。
func TestEnableRestoreExact(t *testing.T) {
	original := "# my settings\nmodel = \"gpt-6-astra\"\nmodel_provider = \"old\" # keep me\n[features]\nmulti_agent = true\n"
	m, p := fixture(t, original)
	if e := m.Enable(17861, "local-secret"); e != nil {
		t.Fatal(e)
	}
	got := read(t, p)
	if !strings.Contains(got, `model_provider = "bps_local"`) || !strings.Contains(got, `requires_openai_auth = true`) || !strings.Contains(got, `stream_idle_timeout_ms = 3600000`) {
		t.Fatal(got)
	}
	if e := m.Restore(); e != nil {
		t.Fatal(e)
	}
	if got = read(t, p); got != original {
		t.Fatalf("restore changed original:\n%s", got)
	}
}
func TestRestorePreservesOtherEdits(t *testing.T) {
	m, p := fixture(t, "model = \"gpt-6-astra\"\n[features]\nmulti_agent = true\n")
	if e := m.Enable(17861, "secret"); e != nil {
		t.Fatal(e)
	}
	changed := strings.Replace(read(t, p), "multi_agent = true", "multi_agent = false", 1)
	if e := os.WriteFile(p, []byte(changed), 0600); e != nil {
		t.Fatal(e)
	}
	if e := m.Restore(); e != nil {
		t.Fatal(e)
	}
	got := read(t, p)
	if strings.Contains(got, "bps_local") || !strings.Contains(got, "multi_agent = false") {
		t.Fatal(got)
	}
}
func TestRestartDoesNotReplaceOriginalBackup(t *testing.T) {
	original := "model_provider = 'old'\n"
	m, p := fixture(t, original)
	if e := m.Enable(17861, "first"); e != nil {
		t.Fatal(e)
	}
	restarted := &Manager{ConfigPath: p, StateDir: m.StateDir}
	if e := restarted.Enable(17862, "second"); e != nil {
		t.Fatal(e)
	}
	if strings.Count(read(t, p), "[model_providers.bps_local]") != 1 {
		t.Fatal(read(t, p))
	}
	if e := restarted.Restore(); e != nil {
		t.Fatal(e)
	}
	if read(t, p) != original {
		t.Fatal(read(t, p))
	}
}
func TestRejectsProviderEditsOnRestore(t *testing.T) {
	m, p := fixture(t, "")
	if e := m.Enable(17861, "secret"); e != nil {
		t.Fatal(e)
	}
	changed := strings.Replace(read(t, p), `model_provider = "bps_local"`, `model_provider = "another"`, 1)
	if e := os.WriteFile(p, []byte(changed), 0600); e != nil {
		t.Fatal(e)
	}
	if e := m.Restore(); e == nil {
		t.Fatal("must not overwrite provider chosen while running")
	}
	if read(t, p) != changed {
		t.Fatal("conflict modified file")
	}
}
func TestRejectsExistingReservedProvider(t *testing.T) {
	original := "[model_providers.bps_local]\nname='mine'\n"
	m, p := fixture(t, original)
	if e := m.Enable(17861, "secret"); e == nil {
		t.Fatal("reserved provider overwritten")
	}
	if read(t, p) != original {
		t.Fatal("changed on error")
	}
}
func TestRejectsInvalidTOMLWithoutWriting(t *testing.T) {
	original := "model = [\n"
	m, p := fixture(t, original)
	if e := m.Enable(17861, "secret"); e == nil {
		t.Fatal("invalid configuration accepted")
	}
	if read(t, p) != original {
		t.Fatal("changed invalid file")
	}
}
func TestComplexTOMLPreservesQuotedText(t *testing.T) {
	original := "instructions = '''\n[not_a_table]\nmodel_provider = 'inside string'\n'''\nmodel_provider = 'old'\n"
	m, p := fixture(t, original)
	if e := m.Enable(17861, "secret"); e != nil {
		t.Fatal(e)
	}
	if e := m.Restore(); e != nil {
		t.Fatal(e)
	}
	if read(t, p) != original {
		t.Fatal(read(t, p))
	}
}
func TestMissingConfigRestoresAbsence(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "codex", "config.toml")
	m := &Manager{ConfigPath: p, StateDir: filepath.Join(root, "state")}
	if e := m.Enable(17861, "secret"); e != nil {
		t.Fatal(e)
	}
	if e := m.Restore(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("created config should be removed")
	}
}
