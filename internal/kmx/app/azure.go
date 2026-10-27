package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The Azure CLI is a prerequisite and is deliberately NOT fetched.
//
// kmx downloads kind and kubectl when a machine lacks them, pinned and
// checksum-verified, because each is a single binary and the alternative is a
// first agent that is several downloads away. The Azure CLI is neither of those
// things. It is a Python distribution rather than one file, it has a real
// package story on every platform it supports, and it must be signed in
// interactively before it is useful — so fetching it would save an operator
// no step at all. The decisive reason is the last one: `az login` puts cloud
// credentials on the machine, and a tool that quietly installs the thing that
// then holds your subscription access is a different kind of surprise from
// one that drops a kubectl in a cache.
var depAz = dependency{"az", "to reach Azure: the cluster, the registry and the monitoring workspaces",
	"https://learn.microsoft.com/cli/azure/install-azure-cli", []string{"version"}, false}

const azureServiceConnectorDeadlock = "ERROR: Error loading command module 'serviceconnector': deadlock detected by _ModuleLock('azure.core.pipeline') at "
const azureManagedClusterAPIVersion = "2025-08-01"

var azureExtensionVariables = []string{
	"AZURE_EXTENSION_DIR",
	"AZURE_EXTENSION_SYS_DIR",
	"AZURE_EXTENSION_DEV_SOURCES",
	"AZURE_EXTENSION_USE_DYNAMIC_INSTALL",
}

func isAzureServiceConnectorDeadlock(line []byte) bool {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if !bytes.HasPrefix(line, []byte(azureServiceConnectorDeadlock)) {
		return false
	}
	address := line[len(azureServiceConnectorDeadlock):]
	if len(address) == 0 {
		return false
	}
	for _, b := range address {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}

type azureStderrFilter struct {
	out         io.Writer
	pending     []byte
	passthrough bool
}

func (w *azureStderrFilter) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		if w.passthrough {
			newline := bytes.IndexByte(p, '\n')
			if newline < 0 {
				if _, err := w.out.Write(p); err != nil {
					return 0, err
				}
				return written, nil
			}
			if _, err := w.out.Write(p[:newline+1]); err != nil {
				return 0, err
			}
			w.passthrough = false
			p = p[newline+1:]
			continue
		}

		newline := bytes.IndexByte(p, '\n')
		if newline < 0 {
			w.pending = append(w.pending, p...)
			// The only filtered line is a fixed prefix plus a short decimal
			// address. Once this bound is exceeded it cannot match, so stream it.
			if len(w.pending) > len(azureServiceConnectorDeadlock)+32 {
				if _, err := w.out.Write(w.pending); err != nil {
					return 0, err
				}
				w.pending = nil
				w.passthrough = true
			}
			return written, nil
		}
		if len(w.pending)+newline > len(azureServiceConnectorDeadlock)+32 {
			if _, err := w.out.Write(w.pending); err != nil {
				return 0, err
			}
			if _, err := w.out.Write(p[:newline+1]); err != nil {
				return 0, err
			}
		} else {
			w.pending = append(w.pending, p[:newline]...)
			if !isAzureServiceConnectorDeadlock(w.pending) {
				if _, err := w.out.Write(append(w.pending, '\n')); err != nil {
					return 0, err
				}
			}
		}
		w.pending = nil
		p = p[newline+1:]
	}
	return written, nil
}

func (w *azureStderrFilter) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	defer func() { w.pending = nil }()
	if isAzureServiceConnectorDeadlock(w.pending) {
		return nil
	}
	_, err := w.out.Write(w.pending)
	return err
}

func (a *App) isolatedAzureRunner() (*run.Runner, func(), error) {
	if a.Run == nil {
		return nil, func() {}, errors.New("Azure CLI runner is unavailable")
	}
	extensions, err := os.MkdirTemp("", "kmx-az-extensions-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create private Azure CLI extension workspace: %w", err)
	}
	runner := *a.Run
	runner.Unset = append([]string(nil), runner.Unset...)
	names := map[string]bool{}
	for _, name := range azureExtensionVariables {
		names[name] = true
		runner.Unset = append(runner.Unset, name)
	}
	clean := make([]string, 0, len(runner.Env))
	for _, entry := range runner.Env {
		name, _, _ := strings.Cut(entry, "=")
		if !names[name] {
			clean = append(clean, entry)
		}
	}
	runner.Env = clean
	runner.Env = append(runner.Env,
		"AZURE_EXTENSION_DIR="+extensions,
		"AZURE_EXTENSION_SYS_DIR="+extensions,
		"AZURE_EXTENSION_DEV_SOURCES=",
		"AZURE_EXTENSION_USE_DYNAMIC_INSTALL=no",
	)
	return &runner, func() { _ = os.RemoveAll(extensions) }, nil
}

// runAzure isolates extension discovery without moving the caller's Azure
// account configuration. Azure CLI 2.86.0 can still emit one non-fatal loader
// error for its built-in Service Connector module; suppress only that exact
// shape while preserving every other diagnostic and the command's exit status.
func (a *App) runAzure(args ...string) error {
	runner, cleanup, err := a.isolatedAzureRunner()
	if err != nil {
		return err
	}
	defer cleanup()
	out := runner.Stderr
	if out == nil {
		out = io.Discard
	}
	filtered := &azureStderrFilter{out: out}
	runner.Stderr = filtered
	runErr := runner.Run("az", args...)
	return errors.Join(runErr, filtered.flush())
}

func (a *App) captureAzure(args ...string) (string, error) {
	runner, cleanup, err := a.isolatedAzureRunner()
	if err != nil {
		return "", err
	}
	defer cleanup()
	cmd := runner.Command("az", args...)
	var stdout, stderr bytes.Buffer
	filtered := &azureStderrFilter{out: &stderr}
	cmd.Stdout, cmd.Stderr = &stdout, filtered
	runErr := cmd.Run()
	flushErr := filtered.flush()
	diagnostic := strings.TrimSpace(stderr.String())
	if runErr != nil {
		if runner.Context != nil && runner.Context.Err() != nil {
			runErr = runner.Context.Err()
		}
		if diagnostic != "" {
			runErr = fmt.Errorf("%s: %s", runErr, diagnostic)
		}
	} else if diagnostic != "" {
		out := a.Err
		if out == nil {
			out = runner.Stderr
		}
		if out != nil {
			_, flushErr = fmt.Fprintln(out, diagnostic)
		}
	}
	return strings.TrimSpace(stdout.String()), errors.Join(runErr, flushErr)
}

func managedClusterAzureArgs(opt lift.Options) []string {
	return []string{"resource", "show", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup,
		"--resource-type", "Microsoft.ContainerService/managedClusters", "--api-version", azureManagedClusterAPIVersion}
}

// account is who the CLI is signed in as and which subscription it will act
// on. Both go in the banner, and neither is the subscription id: the id is an
// identifier this project keeps out of terminals and transcripts, and the
// name plus the signed-in user is what a human actually checks against.
type account struct {
	Name string `json:"name"`
	ID   string `json:"id"`
	User struct {
		Name string `json:"name"`
	} `json:"user"`
}

func (a *App) azAccount() (account, error) {
	var acct account
	out, err := a.captureAzure("account", "show", "-o", "json")
	if err != nil {
		return acct, fmt.Errorf("kmx cluster aks: the Azure CLI is not signed in — run: az login")
	}
	if err := json.Unmarshal([]byte(out), &acct); err != nil {
		return acct, fmt.Errorf("kmx cluster aks: could not read the signed-in account: %w", err)
	}
	if strings.TrimSpace(acct.ID) == "" {
		return acct, fmt.Errorf("kmx cluster aks: the Azure CLI reported no subscription — refusing to act without knowing where")
	}
	return acct, nil
}

// groupExists answers in three values rather than two, for the reason every
// other check against a subscription in this repository does: `az` prints
// nothing on stdout and errors on stderr when the call itself fails, so code
// that reads "no answer" as "does not exist" will create-and-tag a group that
// may already belong to somebody else — or report a teardown that never
// happened. Only a literal true or false is an answer.
func (a *App) groupExists(group string) (lift.Existence, error) {
	out, err := a.captureAzure("group", "exists", "--name", group)
	if err != nil {
		return lift.Unusable, err
	}
	switch strings.TrimSpace(out) {
	case "true":
		return lift.Present, nil
	case "false":
		return lift.Absent, nil
	default:
		return lift.Unusable, fmt.Errorf("az group exists answered %q, which is neither true nor false", strings.TrimSpace(out))
	}
}

// resourceID resolves a resource to its id, and distinguishes "not
// there" from "could not ask". Used at creation to record the id, and never
// used at teardown to FIND something to delete — teardown starts from the
// recorded id and only confirms it.
func (a *App) resourceID(args ...string) (string, lift.Existence) {
	out, err := a.captureAzure(append(args, "--query", "id", "-o", "tsv")...)
	if err != nil {
		if isAzureNotFound(err) {
			return "", lift.Absent
		}
		return "", lift.Unusable
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", lift.Unusable
	}
	return id, lift.Present
}

// confirmRecordedResource asks Azure about a resource BY ITS RECORDED ID and
// returns what it found. This is the only question teardown asks, and it is
// asked by id rather than by name on purpose: on a subscription we do not
// own, a name can have been re-used since the lift ran, and deleting by name
// is how a demo removes a stranger's monitoring.
func (a *App) confirmRecordedResource(id string) (lift.Existence, string) {
	out, err := a.captureAzure("resource", "show", "--ids", id, "--query", "id", "-o", "tsv")
	if err != nil {
		if isAzureNotFound(err) {
			return lift.Absent, ""
		}
		return lift.Unusable, ""
	}
	resolved := strings.TrimSpace(out)
	if resolved == "" {
		return lift.Unusable, ""
	}
	return lift.Present, resolved
}

// isAzureNotFound is deliberately narrow. Every other failure — an expired
// token, a throttled subscription, a network blip — must NOT read as absence,
// because absence is the answer that authorises skipping a delete and
// reporting success.
func isAzureNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{"ResourceNotFound", "ResourceGroupNotFound", "was not found", "could not be found", "(NotFound)"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
