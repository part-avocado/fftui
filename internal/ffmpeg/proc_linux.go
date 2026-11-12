package ffmpeg

import (
	"os/exec"
	"runtime"
	"syscall"
)

// starts runs process starts on one OS thread that never exits. Linux sends
// Pdeathsig when the thread that started the child exits, not the whole
// process, so starting from a thread the Go runtime might retire could kill
// FFmpeg early.
var starts = make(chan func())

func init() {
	go func() {
		runtime.LockOSThread()
		for f := range starts {
			f()
		}
	}()
}

// startTied starts cmd so the kernel kills it when fftui exits, even if
// fftui crashes or is killed. It runs in its own process group, so signals
// the terminal sends to fftui, such as SIGHUP when it closes, reach fftui
// alone and it can stop FFmpeg cleanly instead of FFmpeg dying mid-write.
func startTied(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
	errc := make(chan error, 1)
	starts <- func() { errc <- cmd.Start() }
	return <-errc
}
