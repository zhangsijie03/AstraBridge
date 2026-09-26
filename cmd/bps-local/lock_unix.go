//go:build !windows

package main

import (
	"os"
	"syscall"
)

// 锁由内核随文件描述符关闭而释放，崩溃不会留下阻止重启的标记文件。
func acquireProcessLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
