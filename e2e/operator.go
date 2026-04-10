package e2e

import (
	"os/exec"
	"syscall"
	"time"
)

// operatorStopTimeout — сколько ждать корректного завершения после SIGTERM, затем SIGKILL.
const operatorStopTimeout = 30 * time.Second

// stopOperatorProcess завершает процесс тестового оператора: SIGTERM, ожидание до
// operatorStopTimeout; если процесс не вышел — SIGKILL (зависания в библиотеках).
func stopOperatorProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		return
	case <-time.After(operatorStopTimeout):
		_ = cmd.Process.Kill()
		<-waitDone
	}
}
