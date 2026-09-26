package localconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

const Provider = "bps_local"
const providerLine = "model_provider = \"bps_local\"\n"

type Manager struct {
	ConfigPath string
	StateDir   string
}
type backup struct {
	Committed     bool   `json:"committed"`
	ConfigPath    string `json:"config_path"`
	Original      []byte `json:"original"`
	Applied       []byte `json:"applied"`
	Block         string `json:"block"`
	HadFile       bool   `json:"had_file"`
	HadProvider   bool   `json:"had_provider"`
	ProviderValue string `json:"provider_value"`
}

// 提交前再次比较磁盘内容；发现编辑器已经保存新版本则保留备份并停止。
func commitConfig(path string, expected []byte, existed bool, updated []byte, remove bool) error {
	latest, exists, e := readConfig(path)
	if e != nil {
		return e
	}
	if exists != existed || !bytes.Equal(latest, expected) {
		return errors.New("配置已被其他程序修改，请关闭配置编辑器后重试；备份已保留")
	}
	if remove {
		if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
			return e
		}
		return nil
	}
	return AtomicWrite(path, updated)
}

func (m *Manager) BackupPath() string { return filepath.Join(m.StateDir, "config-backup.json") }
func validate(raw []byte) (map[string]interface{}, error) {
	var v map[string]interface{}
	if toml.Unmarshal(raw, &v) != nil {
		return nil, errors.New("config.toml 不是有效的 TOML，未作修改")
	}
	return v, nil
}

// 使用语法树定位顶层值，避免把多行字符串中的文字或嵌套设置误当作配置。
func providerRange(raw []byte) (int, int, bool, error) {
	var p unstable.Parser
	p.Reset(raw)
	for p.NextExpression() {
		n := p.Expression()
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			break
		}
		if n.Kind != unstable.KeyValue {
			continue
		}
		it := n.Key()
		if !it.Next() {
			continue
		}
		key := string(it.Node().Data)
		if key != "model_provider" || it.Next() {
			continue
		}
		v := n.Value()
		if v.Kind != unstable.String {
			return 0, 0, false, errors.New("model_provider 必须是字符串")
		}
		return int(v.Raw.Offset), int(v.Raw.Offset + v.Raw.Length), true, nil
	}
	if p.Error() != nil {
		return 0, 0, false, errors.New("无法定位配置字段")
	}
	return 0, 0, false, nil
}
func readConfig(path string) ([]byte, bool, error) {
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("为保护现有配置，不自动修改符号链接或非普通配置文件")
	}
	b, e := os.ReadFile(path)
	return b, true, e
}
func AtomicWrite(path string, body []byte) error {
	dir := filepath.Dir(path)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".bps-write-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(body); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}
func (m *Manager) Enable(port int, key string) error {
	if port < 1 || port > 65535 || key == "" || strings.ContainsAny(key, "\r\n\"\\") {
		return errors.New("无效的本地服务参数")
	}
	// 上次异常退出的备份先恢复，绝不把已经修改过的配置当成原始配置。
	if _, e := os.Stat(m.BackupPath()); e == nil {
		if e = m.Restore(); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	raw, had, e := readConfig(m.ConfigPath)
	if e != nil {
		return e
	}
	data, e := validate(raw)
	if e != nil {
		return e
	}
	if providers, ok := data["model_providers"].(map[string]interface{}); ok {
		if _, exists := providers[Provider]; exists {
			return errors.New("配置中已经存在 bps_local 服务，请先重命名该配置")
		}
	}
	a, b, has, e := providerRange(raw)
	if e != nil {
		return e
	}
	s := backup{ConfigPath: m.ConfigPath, Original: raw, HadFile: had, HadProvider: has}
	var updated []byte
	if has {
		s.ProviderValue = string(raw[a:b])
		updated = append(append(append([]byte{}, raw[:a]...), []byte(`"bps_local"`)...), raw[b:]...)
	} else {
		updated = append([]byte(providerLine), raw...)
	}
	s.Block = fmt.Sprintf("\n\n# BPS Local managed provider — restore using BPS Local\n[model_providers.bps_local]\nname = \"BPS Local\"\nbase_url = \"http://127.0.0.1:%d/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = true\nsupports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0\nstream_idle_timeout_ms = 1200000\n[model_providers.bps_local.http_headers]\nX-BPS-Local-Key = \"%s\"\n# BPS Local managed provider end\n", port, key)
	updated = append(updated, []byte(s.Block)...)
	if _, e = validate(updated); e != nil {
		return errors.New("现有配置无法安全添加 BPS 服务，未作修改")
	}
	s.Applied = updated
	saved, _ := json.MarshalIndent(s, "", "  ")
	if e = AtomicWrite(m.BackupPath(), saved); e != nil {
		return e
	}
	if e = commitConfig(m.ConfigPath, raw, had, updated, false); e != nil {
		return e
	}
	s.Committed = true
	saved, _ = json.MarshalIndent(s, "", "  ")
	return AtomicWrite(m.BackupPath(), saved)
}
func (m *Manager) Restore() error {
	raw, e := os.ReadFile(m.BackupPath())
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var s backup
	if json.Unmarshal(raw, &s) != nil || s.ConfigPath != m.ConfigPath {
		return errors.New("备份与当前配置不匹配，请保留备份并手动检查")
	}
	current, exists, e := readConfig(m.ConfigPath)
	if e != nil {
		return e
	}
	if exists == s.HadFile && bytes.Equal(current, s.Original) {
		return os.Remove(m.BackupPath())
	}
	// 备份先落盘、配置后提交。若提交前发生冲突或崩溃，只清理未应用事务。
	if !s.Committed && !bytes.Equal(current, s.Applied) {
		data, parseErr := validate(current)
		if parseErr == nil && data["model_provider"] != Provider {
			providers, _ := data["model_providers"].(map[string]interface{})
			if _, exists := providers[Provider]; !exists {
				return os.Remove(m.BackupPath())
			}
		}
	}
	var restored []byte
	if bytes.Equal(current, s.Applied) {
		restored = s.Original
	} else {
		if _, e = validate(current); e != nil {
			return errors.New("当前配置已被修改且格式无效；备份已保留")
		}
		if s.Block == "" || bytes.Count(current, []byte(s.Block)) != 1 {
			return errors.New("BPS 配置段已被手动修改，无法安全恢复；备份已保留")
		}
		a, b, has, e := providerRange(current)
		if e != nil || !has || string(current[a:b]) != `"bps_local"` {
			return errors.New("当前模型服务已被其他程序修改，未覆盖；备份已保留")
		}
		restored = bytes.Replace(current, []byte(s.Block), nil, 1)
		if s.HadProvider {
			restored = append(append(append([]byte{}, restored[:a]...), []byte(s.ProviderValue)...), restored[b:]...)
		} else {
			if !bytes.HasPrefix(restored, []byte(providerLine)) {
				return errors.New("顶层配置已改变，无法安全恢复；备份已保留")
			}
			restored = restored[len(providerLine):]
		}
	}
	if _, e = validate(restored); e != nil {
		return errors.New("恢复结果校验失败，未修改文件；备份已保留")
	}
	// 删除仅由本工具创建且仍为空的配置；用户新增设置则保留。
	if e = commitConfig(m.ConfigPath, current, exists, restored, !s.HadFile && len(restored) == 0); e != nil {
		return e
	}
	return os.Remove(m.BackupPath())
}
