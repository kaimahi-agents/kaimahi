//go:build windows

package app

import (
	"context"
	"errors"
)

type localAgentIPCRequest struct {
	Label, SessionID, Input, InvocationID string
	Fresh                                 bool
}
type localAgentIPCResponse struct {
	Observation LocalAgentObservation
	Session     LocalAgentSession
	Answer      string
}

func validLocalAgentSocketName(string) bool { return false }
func validLocalAgentLockName(string) bool   { return false }
func startLocalAgentWorker(context.Context, string, LocalAgentInstance) error {
	return errors.New("persistent local agents are unavailable on native Windows; use WSL")
}
func probeLocalAgent(context.Context, LocalAgentInstance, string, localAgentIPCRequest) (localAgentIPCResponse, error) {
	return localAgentIPCResponse{}, errors.New("persistent local agents are unavailable on native Windows; use WSL")
}

func invokeLocalAgentIPC(context.Context, LocalAgentInstance, string, string) (localAgentIPCResponse, error) {
	return localAgentIPCResponse{}, errors.New("persistent local agents are unavailable on native Windows; use WSL")
}
func waitLocalAgentStopped(context.Context, LocalAgentInstance) error {
	return errors.New("persistent local agents are unavailable on native Windows; use WSL")
}
func removeLocalAgentSocket(LocalAgentInstance) error {
	return errors.New("persistent local agents are unavailable on native Windows; use WSL")
}
func localAgentWorkerLeaseHeld(context.Context, LocalAgentInstance) (bool, error) {
	return false, errors.New("persistent local agents are unavailable on native Windows; use WSL")
}
