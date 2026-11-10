//go:build unix

package executor

import (
	"os/exec"
	"syscall"
)

// ownGroup põe o processo num grupo próprio, para que o kill alcance também os filhos (comandos
// de teste do builder, subprocessos que herdam o stdout).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup mata o grupo inteiro do processo.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
