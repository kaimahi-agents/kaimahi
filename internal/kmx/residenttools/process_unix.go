//go:build !windows

package residenttools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"golang.org/x/sys/unix"
)

const BrokerProcessEnv = "KMX_LOCAL_TOOL_BROKER"

// IsBrokerProcess reports the private child mode consumed by the installed kmx
// entrypoint. It is not a user-facing daemon mode.
func IsBrokerProcess() bool { return os.Getenv(BrokerProcessEnv) == "1" }

// Start launches a sibling broker with no inherited environment. The returned
// descriptor is intended for the resident model worker; closing its last copy
// cancels broker work and ends the broker.
func Start(ctx context.Context, executable string, plan Plan) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	budget, err := startupBudget(plan)
	if err != nil {
		return nil, err
	}
	var frame bytes.Buffer
	if err := writeFrame(&frame, plan); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, errors.New("resident tool broker executable is unavailable")
		}
	}
	// Darwin has no SOCK_CLOEXEC socketpair flag. Hold Go's fork lock across
	// creation and descriptor marking so no concurrent exec can inherit either
	// private endpoint in the gap.
	syscall.ForkLock.RLock()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(pair[0])
		unix.CloseOnExec(pair[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, errors.New("resident tool broker channel is unavailable")
	}
	broker := os.NewFile(uintptr(pair[0]), "resident-tool-broker")
	worker := os.NewFile(uintptr(pair[1]), "resident-tool-worker")
	fail := func() (*os.File, error) {
		_ = broker.Close()
		_ = worker.Close()
		return nil, errors.New("resident tool broker could not start")
	}
	configRead, configWrite, err := os.Pipe()
	if err != nil {
		return fail()
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		_ = configRead.Close()
		_ = configWrite.Close()
		return fail()
	}
	cmd := exec.Command(executable)
	cmd.Env = []string{BrokerProcessEnv + "=1"}
	cmd.ExtraFiles = []*os.File{broker, configRead, readyWrite}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		_ = configRead.Close()
		_ = configWrite.Close()
		_ = readyRead.Close()
		_ = readyWrite.Close()
		return fail()
	}
	_ = broker.Close()
	_ = configRead.Close()
	_ = readyWrite.Close()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	stop := func() {
		_ = configWrite.Close()
		_ = readyRead.Close()
		_ = worker.Close()
		// EOF cancels probes and their owned process groups before broker exit.
		select {
		case <-waited:
			return
		case <-time.After(5 * time.Second):
		}
		_ = cmd.Process.Kill()
		select {
		case <-waited:
		case <-time.After(time.Second):
		}
	}
	type startup struct {
		ready response
		err   error
	}
	done := make(chan startup, 1)
	go func() {
		_, err := io.Copy(configWrite, &frame)
		err = errors.Join(err, configWrite.Close())
		var ready response
		if err == nil {
			err = readFrame(bufio.NewReaderSize(readyRead, brokerLimit+1), &ready)
		}
		_ = readyRead.Close()
		done <- startup{ready, err}
	}()
	select {
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	case outcome := <-done:
		if outcome.err != nil || !outcome.ready.OK {
			stop()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if outcome.ready.Error == "resident tool checks failed" {
				return nil, errors.New(outcome.ready.Error)
			}
			return nil, errors.New("resident tool broker did not become ready")
		}
	}
	if err := ctx.Err(); err != nil {
		stop()
		return nil, err
	}
	return worker, nil
}

func startupBudget(plan Plan) (time.Duration, error) {
	if err := plan.validateRuntime(); err != nil {
		return 0, err
	}
	budget := 10 * time.Second
	for _, command := range plan.Commands.Commands {
		for _, check := range command.Checks {
			budget += time.Duration(check.TimeoutMilliseconds)*time.Millisecond + 2*time.Second
		}
	}
	return budget, nil
}

// Run serves the private broker descriptors inherited at fd 3-5.
func Run(channel, config, readiness *os.File) error {
	if channel == nil || config == nil || readiness == nil {
		return errors.New("resident tool broker descriptors are unavailable")
	}
	defer channel.Close()
	defer config.Close()
	defer readiness.Close()
	// ExtraFiles arrive without CLOEXEC. Probes and commands must never inherit
	// the control, config, or readiness descriptors and forge broker messages.
	syscall.ForkLock.RLock()
	var descriptorErr error
	for _, file := range []*os.File{channel, config, readiness} {
		if _, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
			descriptorErr = err
			break
		}
	}
	syscall.ForkLock.RUnlock()
	if descriptorErr != nil {
		return errors.New("resident tool broker descriptor protection failed")
	}
	var plan Plan
	if err := readFrame(bufio.NewReaderSize(config, brokerLimit+1), &plan); err != nil || plan.validateRuntime() != nil {
		_ = writeFrame(readiness, response{Error: "resident tool broker configuration is unavailable"})
		return errors.New("resident tool broker configuration is unavailable")
	}
	_ = config.Close()
	conn, err := net.FileConn(channel)
	if err != nil {
		_ = writeFrame(readiness, response{Error: "resident tool broker channel is unavailable"})
		return err
	}
	_ = channel.Close()
	defer conn.Close()
	ctx, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()
	reader := bufio.NewReaderSize(conn, brokerLimit+1)
	type incoming struct {
		req request
		err error
	}
	requests := make(chan incoming, 1)
	go func() {
		for {
			var req request
			err := readFrame(reader, &req)
			if err != nil {
				cancelAll()
			}
			select {
			case requests <- incoming{req, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	host, err := localtools.OpenCommandPlan(plan.Commands)
	if err != nil {
		_ = writeFrame(readiness, response{Error: "resident tool broker configuration is unavailable"})
		return err
	}
	if err := localtools.RunCommandChecks(ctx, plan.Commands); err != nil {
		_ = writeFrame(readiness, response{Error: "resident tool checks failed"})
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeFrame(readiness, response{OK: true}); err != nil {
		return err
	}
	_ = readiness.Close()
	var writeMu sync.Mutex
	writeResponse := func(value response) {
		writeMu.Lock()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := writeFrame(conn, value); err != nil {
			cancelAll()
			_ = conn.Close()
		}
		writeMu.Unlock()
	}
	active := map[string]context.CancelFunc{}
	type scopedKey struct{ session, key string }
	used := map[scopedKey]bool{}
	sessionCalls := map[string]int{}
	turnCalls := map[scopedKey]int{}
	totalCalls := 0
	cleanupFailed := false
	var activeMu sync.Mutex
	var jobs sync.WaitGroup
	defer func() {
		activeMu.Lock()
		for _, cancel := range active {
			cancel()
		}
		activeMu.Unlock()
		jobs.Wait()
	}()
	for {
		var next incoming
		select {
		case next = <-requests:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := next.err; err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		req := next.req
		switch req.Op {
		case "describe":
			writeResponse(response{OK: true, Definitions: host.Definitions()})
		case "cancel":
			activeMu.Lock()
			cancel := active[req.ID]
			activeMu.Unlock()
			if cancel != nil {
				cancel()
			}
		case "shutdown":
			activeMu.Lock()
			for _, cancel := range active {
				cancel()
			}
			activeMu.Unlock()
			jobs.Wait()
			if cleanupFailed {
				writeResponse(response{Error: "resident tool broker cleanup is incomplete"})
				return localtools.ErrCommandCleanup
			}
			writeResponse(response{OK: true})
			return nil
		case "execute":
			if !validRequestID(req.ID) || !validRequestID(req.InvocationID) || req.SessionUID == "" || len(req.SessionUID) > 256 || !utf8.ValidString(req.SessionUID) || strings.IndexFunc(req.SessionUID, unicode.IsControl) >= 0 || req.Call.Mediation != api.MediationControllerMediated || req.Call.IdempotencyKey != req.InvocationID+":"+req.Call.ID {
				writeResponse(response{ID: req.ID, Error: "resident tool request is invalid"})
				continue
			}
			activeMu.Lock()
			limits := plan.Binding.Limits
			callKey, turnKey := scopedKey{req.SessionUID, req.Call.IdempotencyKey}, scopedKey{req.SessionUID, req.InvocationID}
			if used[callKey] {
				activeMu.Unlock()
				writeResponse(response{ID: req.ID, Error: "resident tool call was already attempted"})
				continue
			}
			if cleanupFailed || active[req.ID] != nil || len(active) >= limits.MaxConcurrent || turnCalls[turnKey] >= limits.MaxCallsPerTurn || sessionCalls[req.SessionUID] >= limits.MaxCallsPerSession || totalCalls >= limits.MaxCallsPerInstance {
				activeMu.Unlock()
				writeResponse(response{ID: req.ID, Error: "resident tool call limit reached"})
				continue
			}
			turnCalls[turnKey]++
			used[callKey] = true
			sessionCalls[req.SessionUID]++
			totalCalls++
			callCtx, cancel := context.WithTimeout(ctx, time.Duration(limits.TimeoutMilliseconds)*time.Millisecond)
			active[req.ID] = cancel
			activeMu.Unlock()
			jobs.Add(1)
			go func(req request, callCtx context.Context, cancel context.CancelFunc) {
				defer jobs.Done()
				result, executeErr := host.Execute(callCtx, controller.ToolCallContext{SessionUID: req.SessionUID}, req.Call)
				cancel()
				activeMu.Lock()
				cleanupFailed = cleanupFailed || errors.Is(executeErr, localtools.ErrCommandCleanup)
				delete(active, req.ID)
				activeMu.Unlock()
				if executeErr != nil {
					writeResponse(response{ID: req.ID, Error: "resident tool execution failed"})
					return
				}
				writeResponse(response{OK: true, ID: req.ID, Result: &result})
			}(req, callCtx, cancel)
		default:
			writeResponse(response{ID: req.ID, Error: "resident tool request is invalid"})
		}
	}
}
