//go:build unix && !linux

package ffmpeg

import (
	"os/exec"
	"syscall"
)

// startTied starts cmd in its own process group, so signals the terminal
// sends to fftui, such as SIGHUP when it closes, reach fftui alone and it can
// stop FFmpeg cleanly. macOS and the BSDs cannot tie a child's life to its
// parent, so fftui stops its jobs itself when it quits, including on SIGINT,
// SIGTERM, and SIGHUP; only SIGKILL or a crash leaves FFmpeg running.
func startTied(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}
