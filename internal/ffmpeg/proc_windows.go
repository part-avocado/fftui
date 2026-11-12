package ffmpeg

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// killOnClose is a job object that kills its processes when its last handle
// closes. fftui never closes it, so Windows closes it when fftui exits for
// any reason, including a crash.
var killOnClose = sync.OnceValues(func() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
})

// startTied starts cmd in the kill-on-close job, so it dies with fftui. If
// the job cannot be used, the process is stopped and an error returned
// rather than leaving an encode that could outlive fftui.
func startTied(cmd *exec.Cmd) error {
	job, err := killOnClose()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	assignErr := cmd.Process.WithHandle(func(h uintptr) {
		err = windows.AssignProcessToJobObject(job, windows.Handle(h))
	})
	if assignErr != nil || err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if assignErr != nil {
			return assignErr
		}
		return err
	}
	return nil
}
