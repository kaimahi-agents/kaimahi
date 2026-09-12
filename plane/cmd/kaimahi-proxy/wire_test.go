package main

import (
	"context"
	"testing"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/stretchr/testify/require"
)

func TestModelUpstreamsUseAHardenedClient(t *testing.T) {
	cfg, err := config.Parse([]byte(`{"upstreams":{
  "ollama":{"base_url":"http://ollama.ollama.svc.cluster.local:11434","path":"v1/chat/completions","classification":"free"},
  "copilot":{"base_url":"https://api.githubcopilot.com","path":"chat/completions","classification":"metered","internet":true}
 }}`))
	require.NoError(t, err)
	// An offline boot warns rather than refusing; per-call vetting remains mandatory.
	client, err := hardenedClient(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, client)
	_, err = client.Get("http://api.githubcopilot.com/")
	require.Error(t, err, "the hardened client refuses plain HTTP")
}

func TestHardenedClientRefusesAPrivateHostAtLoad(t *testing.T) {
	cfg, err := config.Parse([]byte(`{"upstreams":{"inside":{"base_url":"https://localhost","path":"v1/chat/completions","classification":"free","internet":true}}}`))
	require.NoError(t, err, "the shape check cannot know what a name resolves to; the boot-time vet can")
	_, err = hardenedClient(context.Background(), cfg)
	require.ErrorContains(t, err, "refused at config load")
}
