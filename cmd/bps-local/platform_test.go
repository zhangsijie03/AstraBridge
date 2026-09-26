package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProcessLockIsExclusiveAndReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.lock")
	first, err := acquireProcessLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := acquireProcessLock(path)
	if err == nil {
		second.Close()
		t.Fatal("second process lock succeeded")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("lock remained after close: %v", err)
	}
	next.Close()
}

func TestDefaultStateDirectory(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		local := filepath.Join(home, "Local")
		t.Setenv("LOCALAPPDATA", local)
		if got := defaultStateDir(home); got != filepath.Join(local, "AstraBridge") {
			t.Fatal(got)
		}
		t.Setenv("LOCALAPPDATA", "")
		if got := defaultStateDir(home); got != filepath.Join(home, "AppData", "Local", "AstraBridge") {
			t.Fatal(got)
		}
	} else if runtime.GOOS == "darwin" {
		if got := defaultStateDir(home); got != filepath.Join(home, "Library", "Application Support", "BPS Local") {
			t.Fatal(got)
		}
	}
}

func TestProcessLockDoesNotReplaceExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.lock")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireProcessLock(path)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "keep" {
		t.Fatal("lock replaced existing file")
	}
}
