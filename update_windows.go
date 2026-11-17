package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
)

func updateAssetName() string { return "AWGProxy-Control-windows-amd64.exe" }

// cleanupOldBinary удаляет копию прежней версии, оставшуюся после обновления.
func cleanupOldBinary() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

// installUpdate: запущенный exe нельзя перезаписать, но можно переименовать —
// старый становится .old, новый ложится на его место.
func installUpdate(data []byte) (func() error, error) {
	if len(data) < 2 || !bytes.Equal(data[:2], []byte("MZ")) {
		return nil, fmt.Errorf("скачанный файл — не программа Windows")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return nil, err
	}
	if err := os.WriteFile(exe, data, 0o755); err != nil {
		_ = os.Rename(old, exe)
		return nil, err
	}
	return func() error { return exec.Command(exe).Start() }, nil
}
