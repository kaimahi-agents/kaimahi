package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// deleteRetireObject sends a Kubernetes DeleteOptions with both UID and
// resourceVersion preconditions. kubectl delete by name does not check either
// version, so a context-pinned loopback proxy carries this one API request.
func (a *App) deleteRetireObject(ctx context.Context, namespace string, d retireDecision) error {
	proxyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prepared := a.Command("proxy", "--address=127.0.0.1", "--port=0", "--accept-hosts=^127\\.0\\.0\\.1$")
	cmd := exec.CommandContext(proxyCtx, prepared.Path, prepared.Args[1:]...)
	cmd.Env = prepared.Env
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("cannot start pinned Kubernetes proxy")
	}
	// stderr is never propagated: it can contain request or credential material.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start pinned Kubernetes proxy")
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	type started struct {
		port int
		err  error
	}
	ready := make(chan started, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(stdout, 512)).ReadString('\n')
		if err != nil {
			ready <- started{err: err}
			return
		}
		const prefix = "Starting to serve on 127.0.0.1:"
		if !strings.HasPrefix(line, prefix) {
			ready <- started{err: fmt.Errorf("unexpected Kubernetes proxy announcement")}
			return
		}
		port, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
		if err != nil || port < 1 || port > 65535 {
			ready <- started{err: fmt.Errorf("invalid Kubernetes proxy port")}
			return
		}
		ready <- started{port: port}
	}()
	var port int
	select {
	case start := <-ready:
		if start.err != nil {
			return fmt.Errorf("Kubernetes proxy did not start")
		}
		port = start.port
	case <-proxyCtx.Done():
		return fmt.Errorf("Kubernetes proxy startup timed out")
	}
	body, err := json.Marshal(map[string]any{"apiVersion": "meta.k8s.io/v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": d.uid, "resourceVersion": d.version}})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/apis/core.orka.ai/v1alpha1/namespaces/%s/%s/%s", port, namespace, strings.ToLower(d.kind)+"s", d.name)
	request, err := http.NewRequestWithContext(proxyCtx, http.MethodDelete, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot prepare conditional deletion")
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("conditional deletion failed; inspect target before retrying")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("conditional deletion refused (HTTP %d); inspect object version and permissions", response.StatusCode)
	}
	return nil
}
