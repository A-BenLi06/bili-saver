package media

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// writeFileIfChanged 内容不变则跳过写盘。
func writeFileIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	return replaceFile(path, func(out *os.File) error {
		_, err := out.Write(data)
		return err
	})
}

// replaceFile 在同目录准备完整文件再替换，写失败不会截断已有输出。
func replaceFile(path string, write func(*os.File) error) error {
	out, err := os.CreateTemp(filepath.Dir(path), ".media-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	if err := out.Chmod(0644); err != nil {
		out.Close()
		return err
	}
	writeErr := write(out)
	if writeErr == nil {
		writeErr = out.Sync()
	}
	closeErr := out.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(out.Name(), path)
}

// linkFile 按 auto → symlink → hardlink → copy 的降级链建立文件链接。
// symlink 的目标强制转绝对路径：相对路径会相对于链接所在目录解析而失效。
func linkFile(src, dst, mode string) error {
	if abs, err := filepath.Abs(src); err == nil {
		src = abs
	}
	// 用 Lstat 检查（不跟随符号链接）：坏链接也会被识别并重建
	if existing, err := os.Lstat(dst); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(dst); err == nil {
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(dst), target)
				}
				if filepath.Clean(target) == filepath.Clean(src) {
					if _, err := os.Stat(src); err == nil {
						return nil
					}
				}
			}
			_ = os.Remove(dst) // 旧链接：直接重建（指向可能已变化）
		} else if srcInfo, err2 := os.Stat(src); err2 == nil {
			if os.SameFile(existing, srcInfo) {
				return nil // 已是同一文件（硬链接）
			}
			if existing.Size() == srcInfo.Size() && existing.ModTime().Equal(srcInfo.ModTime()) {
				return nil // 拷贝内容一致
			}
			_ = os.Remove(dst)
		} else {
			_ = os.Remove(dst)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	try := func() []string {
		if mode == "copy" {
			return []string{"copy"}
		}
		if mode == "hardlink" {
			return []string{"hardlink", "copy"}
		}
		if mode == "symlink" {
			return []string{"symlink", "copy"}
		}
		// auto
		return []string{"symlink", "hardlink", "copy"}
	}
	for _, m := range try() {
		var err error
		switch m {
		case "symlink":
			err = os.Symlink(src, dst)
		case "hardlink":
			err = os.Link(src, dst)
		case "copy":
			err = copyFile(src, dst)
		}
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("链接失败（所有策略）: %s -> %s", src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	return replaceFile(dst, func(out *os.File) error {
		if _, err := io.Copy(out, in); err != nil {
			return err
		}
		return os.Chtimes(out.Name(), fi.ModTime(), fi.ModTime())
	})
}

func copyIfChanged(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if of, err := os.Stat(dst); err == nil && of.Size() == fi.Size() && of.ModTime().Equal(fi.ModTime()) {
		return nil
	}
	return copyFile(src, dst)
}
