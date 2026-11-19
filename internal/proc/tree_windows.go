package proc

import (
	"errors"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// group is a job the command is put in, and every process it starts with
// it; closing the job ends them all, as it does when magpie exits. A
// command that can't be put in one is killed alone. proc holds the root
// process's own handle until sweep, so SetEfficiencyMode reaches only that
// process and never a recycled pid.
type group struct {
	job  windows.Handle
	proc windows.Handle
}

func ownGroup(*exec.Cmd) {}

func (g *group) join(cmd *exec.Cmd) {
	if p, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(cmd.Process.Pid)); err == nil {
		g.proc = p
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
		LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	g.job = job
}

func (g *group) setEfficiency(on bool) {
	if g.proc != 0 {
		_ = setHandleEfficiencyMode(g.proc, on)
	}
}

// kill ends the job's processes. Unlike a Unix group, a job takes no new
// process once it is being ended, so nothing can be missed and nothing is
// left to wait on: the channel is closed already.
func (g *group) kill(*exec.Cmd) <-chan struct{} {
	if g.job != 0 {
		_ = windows.TerminateJobObject(g.job, 1)
	}
	return closed
}

var closed = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// term is never sent: Windows has no signal a console-less process can be
// asked to end by, so Stop kills the job at once.
func (g *group) term(*exec.Cmd) error { return errors.ErrUnsupported }

func (g *group) sweep(*exec.Cmd) {
	if g.proc != 0 {
		_ = windows.CloseHandle(g.proc)
		g.proc = 0
	}
	if g.job != 0 {
		_ = windows.CloseHandle(g.job)
		g.job = 0
	}
}
