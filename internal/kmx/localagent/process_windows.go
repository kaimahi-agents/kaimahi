//go:build windows

package localagent

import (
	"context"
	"errors"
)

type localAgentIPCRequest struct {
	Label, SessionID, Input, InvocationID string
	Fresh                                 bool
}
type localAgentIPCResponse struct {
	Observation Observation
	Session     Session
	Answer      string
}

func validLocalAgentSocketName(string) bool { return false }
func validLocalAgentLockName(string) bool   { return false }
func startLocalAgentWorker(context.Context, string, Instance) error {
	return errors.New("persistent local agents are unavailable on native Windows; use WSL")
}
func probeLocalAgent(context.Context, Instance, string, localAgentIPCRequest) (localAgentIPCResponse, error) {
	return localAgentIPCResponse{}, errors.New("persistent local agents are unavailable on native Windows; use WSL")
}

func invokeLocalAgentIPC(context.Context, Instance, string, string) (localAgentIPCResponse, error) {
	return localAgentIPCResponse{}, errors.New("persistent local agents are unavailable on native Windows; use WSL")
}
func waitLocalAgentStopped(context.Context, Instance) error {
	return errors.New("persistent local agents are unavailable on native Windows; use WSL")
}
func removeLocalAgentSocket(Instance) error {
	return errors.New("persistent local agents are unavailable on native Windows; use WSL")
}
func localAgentWorkerLeaseHeld(context.Context, Instance) (bool, error) {
	return false, errors.New("persistent local agents are unavailable on native Windows; use WSL")
}
