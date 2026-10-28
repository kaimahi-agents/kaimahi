//go:build unix

package localtools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const stdioFrameLimit = 1 << 20

type stdioProcess struct {
	cmd           *exec.Cmd
	input, output *os.File
	ctx           context.Context
	stop          func() bool
	mu            sync.Mutex
	closing       bool
	failed        bool
	once          sync.Once
	closeErr      error
	// Kernel-only teardown evidence stays private behind fixed public errors.
	observeErr error
	killErr    error
	scanner    *bufio.Scanner
	pending    []byte
	check      func(...string) error
}

func startStdioProcess(ctx context.Context, cfg StdioConfig, check func(...string) error) (*stdioProcess, error) {
	input, childInput, err := stdioPipes()
	if err != nil {
		return nil, ErrStdio
	}
	output, childOutput, err := os.Pipe()
	if err != nil {
		_ = input.Close()
		_ = childInput.Close()
		return nil, ErrStdio
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = []string{} // A nil slice would inherit the complete ambient environment.
	keys := make([]string, 0, len(cfg.Env))
	for key := range cfg.Env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		cmd.Env = append(cmd.Env, key+"="+cfg.Env[key])
	}
	cmd.Stdin, cmd.Stdout = childInput, childOutput
	// Nil stderr goes to the null device without an inherited-pipe drain goroutine.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	err = cmd.Start()
	_ = childInput.Close()
	_ = childOutput.Close()
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, ErrStdio
	}
	p := &stdioProcess{cmd: cmd, input: input, output: output, ctx: ctx, check: check}
	p.scanner = bufio.NewScanner(output)
	p.scanner.Buffer(make([]byte, 4096), stdioFrameLimit+1)
	p.scanner.Split(func(data []byte, eof bool) (int, []byte, error) {
		if at := bytes.IndexByte(data, '\n'); at >= 0 {
			return at + 1, data[:at], nil
		}
		if eof && len(data) > 0 {
			return 0, nil, ErrStdio
		}
		return 0, nil, nil
	})
	p.mu.Lock()
	p.stop = context.AfterFunc(ctx, func() { p.fail(); _ = p.Close() })
	p.mu.Unlock()
	return p, nil
}

// Keep parent/child ends explicit: Cmd.Wait must not own or close the SDK pipes.
func stdioPipes() (parent, child *os.File, err error) {
	child, parent, err = os.Pipe()
	return
}

func (p *stdioProcess) transport() mcp.Transport {
	return &mcp.IOTransport{Reader: p, Writer: stdioWriter{p}, MaxLineLength: stdioFrameLimit}
}

func (p *stdioProcess) healthy() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed || p.closing || p.ctx.Err() != nil {
		return ErrStdio
	}
	return nil
}

func (p *stdioProcess) fail() {
	p.mu.Lock()
	if !p.closing {
		p.failed = true
	}
	p.mu.Unlock()
}

// Read validates complete bounded newline frames before the SDK decoder sees
// them. In particular, decoding in the SDK must not erase duplicate keys or
// JSON-escaped credential echoes. Peer data never enters SDK logging unchecked.
func (p *stdioProcess) Read(dst []byte) (int, error) {
	if len(p.pending) == 0 {
		if !p.scanner.Scan() {
			p.fail()
			return 0, ErrStdio
		}
		raw := p.scanner.Bytes()
		var value map[string]any
		if len(raw) > stdioFrameLimit || !runtime.JSONKeysUnique(raw) || !runtime.JSONNumbersExact(raw) || json.Unmarshal(raw, &value) != nil || value == nil || value["method"] != nil || CheckJSONPayloads(value, p.check, []string{"result", "tools", "*", "inputSchema"}, []string{"result", "structuredContent"}) != nil {
			p.fail()
			return 0, ErrStdio
		}
		if result, ok := value["result"].(map[string]any); ok {
			if kind, present := result["resultType"]; present && kind != "" && kind != "complete" {
				p.fail()
				return 0, ErrStdio
			}
		}
		p.pending = append(append([]byte(nil), raw...), '\n')
	}
	n := copy(dst, p.pending)
	p.pending = p.pending[n:]
	return n, nil
}

type stdioWriter struct{ p *stdioProcess }

func (w stdioWriter) Write(data []byte) (int, error) {
	if w.p.healthy() != nil {
		return 0, ErrStdio
	}
	return w.p.input.Write(data)
}
func (w stdioWriter) Close() error { return w.p.Close() }

func (p *stdioProcess) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		// Check before closing stdin or sending our own kill. A dead leader may
		// have a descendant holding stdout open, so EOF alone is not exit proof.
		exited, err := stdioLeaderExited(p.cmd.Process.Pid)
		p.observeErr = err
		if exited {
			p.failed = true
		}
		p.closing = true
		stop := p.stop
		p.mu.Unlock()
		if stop != nil {
			stop()
		}
		_ = p.input.Close()
		// Kill before reaping: an exited leader remains waitable, pinning its
		// identity until its owned group has been signaled. Escaped setsid daemons
		// are outside process-group cleanup; this is not OS containment.
		p.killErr = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		_ = p.output.Close() // Unblock SDK reads even if a descendant held stdout.
		waited := make(chan error, 1)
		go func() { waited <- p.cmd.Wait() }()
		select {
		case err := <-waited:
			var exit *exec.ExitError
			expectedKill := errors.As(err, &exit) && exit.Sys().(syscall.WaitStatus).Signal() == syscall.SIGKILL
			if err != nil && !expectedKill {
				p.closeErr = ErrStdio
				if exit == nil {
					p.closeErr = ErrStdioCleanup
				}
			}
		case <-time.After(time.Second):
			p.closeErr = ErrStdioCleanup
		}
		p.mu.Lock()
		if (p.failed || p.ctx.Err() != nil) && p.closeErr == nil {
			p.closeErr = ErrStdio
		}
		if p.observeErr != nil || p.killErr != nil && !errors.Is(p.killErr, syscall.ESRCH) {
			p.closeErr = ErrStdioCleanup
		}
		p.mu.Unlock()
	})
	return p.closeErr
}

var _ io.ReadCloser = (*stdioProcess)(nil)
var _ io.WriteCloser = stdioWriter{}
