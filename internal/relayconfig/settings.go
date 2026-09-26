package relayconfig

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const DefaultPort = 17861
const keyPrefix = "bps-local-"

type Settings struct {
	Port   int    `json:"port"`
	APIKey string `json:"api_key"`
}

// 固定地址和独立密钥使客户端配置能跨重启使用；损坏时不能悄悄换密钥。
func Load(dir string) (*Settings, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "relay.json")
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("中转配置必须是普通文件，不能是符号链接")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var settings Settings
		if json.Unmarshal(raw, &settings) != nil || settings.Port < 1 || settings.Port > 65535 || !strings.HasPrefix(settings.APIKey, keyPrefix) || len(settings.APIKey) != len(keyPrefix)+64 {
			return nil, errors.New("relay.json 无效，请检查端口和 API Key；现有配置已保留")
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(settings.APIKey, keyPrefix)); err != nil {
			return nil, errors.New("中转 API Key 格式无效")
		}
		if err := os.Chmod(path, 0600); err != nil {
			return nil, err
		}
		return &settings, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	settings := &Settings{Port: DefaultPort, APIKey: keyPrefix + hex.EncodeToString(secret)}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return Load(dir)
	}
	if err != nil {
		return nil, err
	}
	_, writeErr := file.Write(append(raw, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return nil, err
	}
	return settings, nil
}
