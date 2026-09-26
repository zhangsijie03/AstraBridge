package relayconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestKeySurvivesRestartAndStaysPrivate(t *testing.T) {
	dir := t.TempDir()
	first, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.APIKey != second.APIKey || len(first.APIKey) < 64 || first.Port != second.Port {
		t.Fatal("connection settings changed across restart")
	}
	info, err := os.Stat(filepath.Join(dir, "relay.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows 使用用户目录继承 ACL；POSIX mode 不代表其权限。
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("API Key permissions not private")
	}
}
func TestCorruptSettingsAreNotSilentlyReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "relay.json")
	original := []byte(`{"port":0,"api_key":"short"}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("invalid settings accepted")
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != string(original) {
		t.Fatal("existing key overwritten")
	}
}
func TestSettingsRejectSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "relay.json")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("followed symlink")
	}
}
