//go:build !windows

package residenttools

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

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
	encodeErr := writeFrame(configWrite, plan)
	encodeErr = errors.Join(encodeErr, configWrite.Close())
	if encodeErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = readyRead.Close()
		_ = worker.Close()
		return nil, errors.New("resident tool broker configuration failed")
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	_ = readyRead.SetReadDeadline(time.Now().Add(10 * time.Second))
	var ready response
	readyErr := readFrame(bufio.NewReaderSize(readyRead, brokerLimit+1), &ready)
	_ = readyRead.Close()
	if readyErr != nil || !ready.OK {
		_ = cmd.Process.Kill()
		<-waited
		_ = worker.Close()
		if ready.Error != "" {
			return nil, errors.New(ready.Error)
		}
		return nil, errors.New("resident tool broker did not become ready")
	}
	select {
	case <-ctx.Done():
		_ = worker.Close()
		_ = cmd.Process.Kill()
		<-waited
		return nil, ctx.Err()
	default:
	}
	return worker, nil
}

// Run serves the private broker descriptors inherited at fd 3-5.
func Run(channel, config, readiness *os.File) error {
	if channel == nil || config == nil || readiness == nil {
		return errors.New("resident tool broker descriptors are unavailable")
	}
	defer channel.Close()
	defer config.Close()
	defer readiness.Close()
	var plan Plan
	if err := readFrame(bufio.NewReaderSize(config, brokerLimit+1), &plan); err != nil || plan.validate() != nil || plan.Backend != BoundedProcess {
		_ = writeFrame(readiness, response{Error: "resident tool broker configuration is unavailable"})
		return errors.New("resident tool broker configuration is unavailable")
	}
	host, err := localtools.OpenCommandPlan(plan.Commands)
	if err != nil {
		_ = writeFrame(readiness, response{Error: "resident tool broker configuration is unavailable"})
		return err
	}
	if err := localtools.RunCommandChecks(context.Background(), plan.Commands); err != nil {
		_ = writeFrame(readiness, response{Error: "resident tool checks failed"})
		return err
	}
	conn, err := net.FileConn(channel)
	if err != nil {
		_ = writeFrame(readiness, response{Error: "resident tool broker channel is unavailable"})
		return err
	}
	_ = channel.Close()
	defer conn.Close()
	if err := writeFrame(readiness, response{OK: true}); err != nil {
		return err
	}
	_ = readiness.Close()
	reader := bufio.NewReaderSize(conn, brokerLimit+1)
	var writeMu sync.Mutex
	writeResponse := func(value response) {
		writeMu.Lock()
		_ = writeFrame(conn, value)
		writeMu.Unlock()
	}
	ctx, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()
	active := map[string]context.CancelFunc{}
	sessionCalls := map[string]int{}
	turnCalls := map[string]int{}
	totalCalls := 0
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
		var req request
		if err := readFrame(reader, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
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
			writeResponse(response{OK: true})
			return nil
		case "execute":
			if !validRequestID(req.ID) || !validRequestID(req.InvocationID) || req.SessionUID == "" {
				writeResponse(response{ID: req.ID, Error: "resident tool request is invalid"})
				continue
			}
			activeMu.Lock()
			limits := plan.Binding.Limits
			if active[req.ID] != nil || len(active) >= limits.MaxConcurrent || turnCalls[req.InvocationID] >= limits.MaxCallsPerTurn || sessionCalls[req.SessionUID] >= limits.MaxCallsPerSession || totalCalls >= limits.MaxCallsPerInstance {
				activeMu.Unlock()
				writeResponse(response{ID: req.ID, Error: "resident tool call limit reached"})
				continue
			}
			turnCalls[req.InvocationID]++
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
