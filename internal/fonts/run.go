package fonts

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

// errOutputExceeded reports that a tool produced more than the configured
// stdout cap.
var errOutputExceeded = errors.New("fonts: output exceeded limit")

// runCommand runs name with args under ctx, collecting at most limit bytes of
// stdout. The process is killed on ctx cancellation (CommandContext) and on
// output overflow, so a misbehaving tool can never stream unbounded data nor
// hang the caller. A deadline is reported as the context error itself; stderr
// is discarded and the console window is hidden on Windows (procAttr).
func runCommand(ctx context.Context, limit int, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = procAttr()
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out := readCapped(stdout, limit)
	if out.overflow {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errOutputExceeded
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if waitErr != nil {
		return nil, waitErr
	}
	if out.err != nil {
		return nil, out.err
	}
	return out.data, nil
}

// cappedResult is the outcome of reading a stream up to its cap.
type cappedResult struct {
	data     []byte
	overflow bool
	err      error
}

// readCapped reads r until EOF or cap bytes; overflow reports that the stream
// held more than cap bytes (the read stops at the cap so the caller can kill
// the process promptly).
func readCapped(r io.Reader, cap int) cappedResult {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			if len(buf)+n > cap {
				return cappedResult{data: buf, overflow: true}
			}
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				return cappedResult{data: buf}
			}
			return cappedResult{data: buf, err: err}
		}
	}
}
