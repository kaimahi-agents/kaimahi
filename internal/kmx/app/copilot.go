package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
)

const (
	copilotClientID   = "01ab8ac9400c4e429b23" // GitHub's Copilot-entitled VS Code OAuth app.
	copilotSecretName = "kaimahi-copilot-token"
)

type copilotEnvironment struct {
	deviceURL, accessURL, exchangeURL string
	tokenFile                         string
	client                            *http.Client
	sleep                             func(time.Duration)
}

func defaultCopilotEnvironment() (copilotEnvironment, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return copilotEnvironment{}, fmt.Errorf("cannot locate the Copilot login cache: %w", err)
	}
	return copilotEnvironment{
		deviceURL:   "https://github.com/login/device/code",
		accessURL:   "https://github.com/login/oauth/access_token",
		exchangeURL: "https://api.github.com/copilot_internal/v2/token",
		tokenFile:   filepath.Join(home, ".config", "kaimahi", "copilot-oauth-token"),
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return fmt.Errorf("redirect refused for credential-bearing request")
			},
		},
		sleep: time.Sleep,
	}, nil
}

func (a *App) copilotEnvironment() (copilotEnvironment, error) {
	if a.copilotEnv != nil {
		return *a.copilotEnv, nil
	}
	return defaultCopilotEnvironment()
}

// CaptureCopilotCredential performs GitHub's device flow, exchanges the
// cached OAuth token for a short-lived Copilot token, and puts only that
// short-lived token in plane custody. Stdin is deliberately never read.
func (a *App) CaptureCopilotCredential() error {
	if err := a.Guard("store the Copilot model credential as Secret "+admin.Namespace+"/"+copilotSecretName,
		a.operationCommand("models", "credential", "copilot")); err != nil {
		return err
	}
	env, err := a.copilotEnvironment()
	if err != nil {
		return err
	}
	oauth, err := readPrivateToken(env.tokenFile)
	if os.IsNotExist(err) || err == nil && len(oauth) == 0 {
		oauth, err = a.copilotDeviceLogin(env)
	}
	if err != nil {
		return err
	}
	defer zeroBytes(oauth)

	token, err := copilotExchange(env, oauth)
	if err != nil {
		return fmt.Errorf("Copilot token exchange failed. If the login is stale or lacks a Copilot subscription, remove %s and re-run to log in again: %w", env.tokenFile, err)
	}
	defer zeroBytes(token)
	if err := a.apply("plane/namespace.yaml"); err != nil {
		return err
	}
	if err := a.storeCredentialValue(copilotSecretName, admin.Namespace, "api-key", token); err != nil {
		return err
	}
	zeroBytes(token)
	if err := a.applyCopilotEgress(); err != nil {
		return err
	}
	a.notef("Copilot credential refreshed. It is short-lived; re-run `kmx models credential copilot` when authentication fails.")
	return a.restartPlaneIfPresent()
}

// storeCredentialValue keeps credential material off argv and disk.
func (a *App) storeCredentialValue(name, namespace, key string, token []byte) error {
	body := credentialSecretManifest(name, namespace, key, token)
	defer zeroBytes(body)
	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s -n %s apply -f - # (Secret %s, key %s, credential on stdin)\n", a.Cfg.KubeContext, namespace, name, key)
	if err := quiet.RunStdin(body, "kubectl", a.kubectl("-n", namespace, "apply", "-f", "-")...); err != nil {
		return err
	}
	a.notef("Secret %s/%s stored.", namespace, name)
	return nil
}

func credentialSecretManifest(name, namespace, key string, value []byte) []byte {
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(value)))
	base64.StdEncoding.Encode(encoded, value)
	defer zeroBytes(encoded)
	var body []byte
	body = append(body, "apiVersion: v1\nkind: Secret\nmetadata:\n"...)
	body = append(body, "  name: "+name+"\n  namespace: "+namespace+"\ntype: Opaque\ndata:\n  "+key+": "...)
	body = append(body, encoded...)
	return append(body, '\n')
}

// zeroBytes promptly clears the credential buffers this process owns.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func trimSpaceBytes(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpaceByte(b[start]) {
		start++
	}
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
}

func readPrivateToken(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("cannot secure Copilot login cache %s: %w", path, err)
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("cannot read Copilot login cache %s: %w", path, err)
	}
	return trimSpaceBytes(b), nil
}

func (a *App) copilotDeviceLogin(env copilotEnvironment) ([]byte, error) {
	a.notef("No Copilot OAuth token at %s; starting GitHub device login.", env.tokenFile)
	var device struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
	}
	if err := postFormJSON(env.client, env.deviceURL, url.Values{"client_id": {copilotClientID}, "scope": {"read:user"}}, &device); err != nil {
		return nil, fmt.Errorf("device-code request failed: %w", err)
	}
	if device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI == "" {
		return nil, fmt.Errorf("device-code request returned no user_code")
	}
	if device.Interval < 1 {
		device.Interval = 5
	}
	fmt.Fprintf(a.Err, "\n  Open:  %s\n  Code:  %s\n\nWaiting for approval...\n", device.VerificationURI, device.UserCode)
	interval := time.Duration(device.Interval) * time.Second
	for range 120 {
		env.sleep(interval)
		var access struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		err := postFormJSON(env.client, env.accessURL, url.Values{
			"client_id":   {copilotClientID},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {device.DeviceCode},
		}, &access)
		if err != nil {
			continue
		}
		if access.AccessToken != "" {
			token := []byte(access.AccessToken)
			if err := writePrivateToken(env.tokenFile, token); err != nil {
				zeroBytes(token)
				return nil, err
			}
			a.notef("Login OK; OAuth token cached at %s", env.tokenFile)
			return token, nil
		}
		switch access.Error {
		case "authorization_pending":
		case "slow_down":
			env.sleep(interval)
		default:
			return nil, fmt.Errorf("device login failed: %s", access.Error)
		}
	}
	return nil, fmt.Errorf("device login timed out")
}

func writePrivateToken(path string, token []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create Copilot login cache directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".copilot-oauth-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(token); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func copilotExchange(env copilotEnvironment, oauth []byte) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, env.exchangeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+string(oauth))
	resp, err := env.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if result.Token == "" {
		return nil, fmt.Errorf("exchange response contained no token; refusing to store a Secret")
	}
	return []byte(result.Token), nil
}

func postFormJSON(client *http.Client, endpoint string, values url.Values, out any) error {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s", strconv.Itoa(resp.StatusCode))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (a *App) applyCopilotEgress() error {
	body, err := kaimahi.Managed.ReadFile("k8s/egress-copilot.yaml")
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Err, "kubectl --context %s apply -f - # (embedded k8s/egress-copilot.yaml)\n", a.Cfg.KubeContext)
	quiet := *a.Run
	quiet.Echo = false
	return quiet.RunStdin(body, "kubectl", a.kubectl("apply", "-f", "-")...)
}

func (a *App) restartPlaneIfPresent() error {
	_, err := a.kubectlCapture("-n", admin.Namespace, "get", "deploy/kaimahi-proxy", "-o", "name")
	if err != nil {
		if strings.Contains(err.Error(), "Error from server (NotFound):") {
			a.notef("The plane is not deployed here yet; it will start with this credential.")
			return nil
		}
		return fmt.Errorf("credential is stored, but cannot tell whether the proxy is deployed; restart was not performed: %w", err)
	}
	if err := a.kubectlRun("-n", admin.Namespace, "rollout", "restart", "deploy/kaimahi-proxy"); err != nil {
		return err
	}
	return a.kubectlRun("-n", admin.Namespace, "rollout", "status", "deploy/kaimahi-proxy", "--timeout=300s")
}
