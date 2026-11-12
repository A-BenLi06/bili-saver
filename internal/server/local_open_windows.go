package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func revealLocalPath(p string) error {
	if strings.ContainsAny(p, "\"\r\n\x00") {
		return fmt.Errorf("invalid path")
	}
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	exe := filepath.Join(os.Getenv("WINDIR"), "explorer.exe")
	cmd := exec.Command(exe)
	// Explorer's /select requires quotes around the path itself, not around the
	// entire /select argument. No command shell is involved.
	args := " /select,\"" + p + "\""
	if st.IsDir() {
		args = " \"" + p + "\""
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "\"" + exe + "\"" + args}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
