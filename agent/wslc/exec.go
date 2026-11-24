package wslc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

const DefaultTimeout = 60 * time.Second

// Runner executes wslc. Run bounds the command by Executor.Timeout.
// RunStreaming does not add a timeout; cancelling ctx kills the process.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout, stderr string, code int, err error)
	RunStreaming(ctx context.Context, args ...string) (stdout io.ReadCloser, wait func() (stderr string, code int, err error), err error)
}

// Executor runs wslc.exe. Session, when set, is passed as a global --session
// flag in front of the subcommand.
type Executor struct {
	Path    string
	Session string
	Timeout time.Duration
}

func FindBinary() (string, error) {
	if p := os.Getenv("WSLC_PATH"); p != "" {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			return "", errors.New("WSLC_PATH does not point at wslc.exe: " + p)
		}
		return p, nil
	}

	const installed = `C:\Program Files\WSL\wslc.exe`
	if st, err := os.Stat(installed); err == nil && !st.IsDir() {
		return installed, nil
	}
	if p, err := exec.LookPath("wslc.exe"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("wslc"); err == nil {
		return p, nil
	}
	return "", errors.New("wslc.exe not found")
}

func NewExecutor() (*Executor, error) {
	path, err := FindBinary()
	if err != nil {
		return nil, err
	}
	return &Executor{
		Path:    path,
		Session: os.Getenv("WSLC_SESSION"),
		Timeout: DefaultTimeout,
	}, nil
}

func (e *Executor) commandArgs(args []string) []string {
	if e.Session == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--session", e.Session)
	out = append(out, args...)
	return out
}

func (e *Executor) Run(ctx context.Context, args ...string) (string, string, int, error) {
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, e.Path, e.commandArgs(args)...)
	hideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := exitCode(err)
	if err != nil && !isExitError(err) {
		return stdout.String(), stderr.String(), code, err
	}
	return stdout.String(), stderr.String(), code, nil
}

func (e *Executor) RunStreaming(ctx context.Context, args ...string) (io.ReadCloser, func() (string, int, error), error) {
	cmd := exec.CommandContext(ctx, e.Path, e.commandArgs(args)...)
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	wait := func() (string, int, error) {
		err := cmd.Wait()
		code := exitCode(err)
		if err != nil && !isExitError(err) {
			return stderr.String(), code, err
		}
		return stderr.String(), code, nil
	}
	return stdout, wait, nil
}

func isExitError(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
