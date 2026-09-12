package config_test

import (
	"testing"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/stretchr/testify/require"
)

// A hosted model endpoint must be marked HTTPS/443 with no userinfo;
// an unmarked endpoint cannot escape onto the plain in-cluster dialer.
func TestParseHostedUpstreamShape(t *testing.T) {
	for _, tc := range []struct {
		name, url, extra string
		ok               bool
	}{
		{"hosted https", "https://api.githubcopilot.com", `,"internet":true`, true},
		{"hosted explicit 443", "https://api.githubcopilot.com:443", `,"internet":true`, true},
		{"hosted with ca_file", "https://model.kaimahi-ci.test", `,"internet":true,"ca_file":"/etc/model.crt"`, true},
		{"hosted public IP literal", "https://203.0.113.10", `,"internet":true`, true},
		{"in-cluster service", "http://model:8080", "", true},
		{"in-cluster service.ns", "http://model.kagent:8080", "", true},
		{"in-cluster full suffix", "http://model.kagent.svc.cluster.local:8080", "", true},
		{"in-cluster private IP", "http://10.96.0.5:8080", "", true},
		{"in-cluster loopback https", "https://127.0.0.1:8443", "", true},
		{"hosted over http", "http://api.githubcopilot.com", `,"internet":true`, false},
		{"hosted on another port", "https://api.githubcopilot.com:8443", `,"internet":true`, false},
		{"hosted with userinfo", "https://user:pw@api.githubcopilot.com", `,"internet":true`, false},
		{"hosted private IP", "https://10.0.0.1", `,"internet":true`, false},
		{"hosted metadata IP", "https://169.254.169.254", `,"internet":true`, false},
		{"public host without marker", "https://api.githubcopilot.com", "", false},
		{"public IP without marker", "https://203.0.113.10", "", false},
		{"ca_file without marker", "http://model.kagent:8080", `,"ca_file":"/etc/x"`, false},
		{"three-label name without mark", "http://model.kaimahi-ci.test", "", false},
		{"two-label https without marker", "https://github.com", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Parse([]byte(`{"upstreams":{"m":{"base_url":"` + tc.url + `","path":"v1/chat/completions","classification":"metered"` + tc.extra + `}}}`))
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestInternetHostsCoverModelUpstreams(t *testing.T) {
	c, err := config.Parse([]byte(`{"upstreams":{
  "ollama":{"base_url":"http://ollama.ollama.svc.cluster.local:11434","path":"v1/chat/completions","classification":"free"},
  "copilot":{"base_url":"https://api.githubcopilot.com","path":"chat/completions","classification":"metered","internet":true},
  "copilot-responses":{"base_url":"https://api.githubcopilot.com","path":"responses","classification":"metered","internet":true},
  "echo":{"base_url":"https://Model-Echo.kaimahi-ci.test","path":"v1/responses","classification":"metered","internet":true,"ca_file":"/etc/model.crt"}
 }}`))
	require.NoError(t, err)
	hosts := c.InternetHosts()
	got := map[string]string{}
	for _, h := range hosts {
		got[h.Name] = h.CAFile
	}
	require.Equal(t, map[string]string{"api.githubcopilot.com": "", "model-echo.kaimahi-ci.test": "/etc/model.crt"}, got)
	require.Len(t, hosts, 2)
	require.Equal(t, []string{"ollama.ollama.svc.cluster.local"}, c.InClusterHosts())
}
