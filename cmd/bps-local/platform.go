package main

import (
	"os"
	"path/filepath"
	"runtime"
)

func defaultStateDir(home string) string {
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(local, "AstraBridge")
	case "darwin":
		// 保留已有 macOS 地址、Key 与旧版迁移备份。
		return filepath.Join(home, "Library", "Application Support", "BPS Local")
	default:
		config, err := os.UserConfigDir()
		if err != nil {
			config = filepath.Join(home, ".config")
		}
		return filepath.Join(config, "AstraBridge")
	}
}
