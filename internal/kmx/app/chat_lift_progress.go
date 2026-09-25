package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// Fetches run between pickers, with the terminal in canonical mode. SIGINT is
// handled by the chat context; no second input reader consumes the next choice.
func liftFetchProgress(ctx context.Context, out io.Writer, label string, fetch func(context.Context) ([]byte, error)) ([]byte, error) {
	label = strings.Join(strings.Fields(safeTerminal(label)), " ")
	if !isInteractiveTerminal(out) {
		fmt.Fprintln(out, label)
		return fetch(ctx)
	}
	width := cliui.New(out).Width()
	if width <= 0 {
		width = 80
	}
	paint := func(frame string) {
		fmt.Fprint(out, "\r\x1b[2K", ansi.Truncate(frame+" "+label, max(1, width-1), "…"))
	}
	paint("⠋")
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		frames := []string{"⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏", "⠋"}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				paint(frames[i%len(frames)])
			}
		}
	}()
	defer func() { close(stop); <-joined; fmt.Fprint(out, "\r\x1b[2K") }()
	return fetch(ctx)
}

func azureFetchLabel(args []string) string {
	value := func(flag string) string {
		for i, arg := range args {
			if arg == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	prefix := strings.Join(args, " ")
	label := "Fetching Azure data"
	switch {
	case strings.HasPrefix(prefix, "account show"):
		label = "Fetching active Azure tenant"
	case strings.HasPrefix(prefix, "account list"):
		label = "Fetching subscriptions"
	case strings.HasPrefix(prefix, "group list"):
		label = "Fetching resource groups"
	case strings.HasPrefix(prefix, "aks list"):
		label = "Fetching AKS clusters"
	case strings.HasPrefix(prefix, "rest"):
		label = "Fetching Azure REST response"
	case strings.HasPrefix(prefix, "cognitiveservices usage list"):
		label = "Checking available Foundry quota"
	case strings.HasPrefix(prefix, "cognitiveservices model list"):
		label = "Fetching regional Foundry models"
	case strings.HasPrefix(prefix, "aks get-credentials"):
		label = "Fetching AKS credentials"
	case strings.HasPrefix(prefix, "cognitiveservices account deployment list"):
		label = "Fetching Foundry deployments"
	case strings.HasPrefix(prefix, "cognitiveservices account list-models"):
		label = "Fetching available Foundry models"
	case strings.HasPrefix(prefix, "cognitiveservices account keys list"):
		label = "Fetching Foundry credential"
	case strings.HasPrefix(prefix, "cognitiveservices account list"):
		label = "Fetching Foundry accounts"
	case strings.HasPrefix(prefix, "cognitiveservices account deployment create"):
		label = "Creating Foundry model deployment"
	case strings.HasPrefix(prefix, "cognitiveservices account create"):
		label = "Creating Foundry account"
	}
	for _, field := range []struct{ flag, label string }{{"--subscription", "subscription"}, {"--resource-group", "rg"}, {"--name", "resource"}, {"--location", "region"}} {
		if v := value(field.flag); v != "" {
			label += " · " + field.label + ":" + v
		}
	}
	return label
}

func (b *orkaChatBackend) liftAzureFetch(ctx context.Context, args ...string) ([]byte, error) {
	return b.liftLoading(ctx, azureFetchLabel(args), func(ctx context.Context) ([]byte, error) { return b.app.liftDiscovery(ctx, "az", args...) })
}

func (b *orkaChatBackend) liftAzureCreate(ctx context.Context, args ...string) ([]byte, error) {
	return b.liftLoading(ctx, azureFetchLabel(args), func(ctx context.Context) ([]byte, error) { return b.app.liftAzureWrite(ctx, args...) })
}

func (b *orkaChatBackend) liftSubscriptions(ctx context.Context) ([]byte, error) {
	raw, err := b.liftAzureFetch(ctx, "account", "show", "--query", "tenantId", "-o", "json", "--only-show-errors")
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var tenant string
	if json.Unmarshal(raw, &tenant) != nil || strings.TrimSpace(tenant) == "" {
		return nil, fmt.Errorf("Azure account lookup returned no tenant; check az login")
	}
	label := "Fetching subscriptions for tenant:" + tenant + " (active; includes all accessible tenants)"
	return b.liftLoading(ctx, label, func(ctx context.Context) ([]byte, error) {
		return b.app.liftDiscovery(ctx, "az", "account", "list", "--query", "[?state=='Enabled'].{name:name,id:id,tenantId:tenantId}", "-o", "json", "--only-show-errors")
	})
}
