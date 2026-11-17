package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func updateAssetName() string { return "AWGProxy-Control-mac-arm64.zip" }

func cleanupOldBinary() {}

// installUpdate заменяет файлы внутри .app на файлы из архива и возвращает функцию перезапуска.
func installUpdate(data []byte) (func() error, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(bundle, ".app") {
		return nil, fmt.Errorf("приложение запущено не из .app")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	n := 0
	for _, f := range zr.File {
		i := strings.Index(f.Name, ".app/")
		if i < 0 || strings.HasSuffix(f.Name, "/") {
			continue
		}
		rel := f.Name[i+5:]
		if rel == "" || strings.Contains(rel, "..") {
			continue
		}
		dst := filepath.Join(bundle, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		mode := os.FileMode(0o644)
		if strings.Contains(rel, "MacOS/") {
			if len(b) < 4 || !bytes.Equal(b[:4], []byte{0xcf, 0xfa, 0xed, 0xfe}) {
				return nil, fmt.Errorf("в архиве не программа для Mac")
			}
			mode = 0o755
		}
		tmp := dst + ".new"
		if err := os.WriteFile(tmp, b, mode); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return nil, err
		}
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("архив пустой")
	}
	return func() error { return exec.Command("open", "-n", bundle).Start() }, nil
}
