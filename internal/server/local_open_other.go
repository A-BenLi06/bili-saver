//go:build !windows

package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func revealLocalPath(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		if st.IsDir() {
			cmd = exec.Command("open", p)
		} else {
			cmd = exec.Command("open", "-R", p)
		}
	} else {
		if !st.IsDir() {
			p = filepath.Dir(p)
		}
		cmd = exec.Command("xdg-open", p)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
