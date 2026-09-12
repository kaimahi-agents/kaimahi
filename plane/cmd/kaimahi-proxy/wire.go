package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/egress"
)

// hardenedClient builds the ONE hardened client over every
// model upstream marked `internet: true`, and vets each host at
// boot: a host that resolves to a private, link-local, loopback,
// carrier-NAT, multicast or metadata address refuses the config LOUDLY
// here, not at first use. A host that cannot be resolved at boot is a
// warning, not a refusal: the per-call check is the real gate (a record
// can change after boot anyway — DNS rebinding — which is why every call
// re-resolves), and a kind cluster with no route to the internet must
// still boot the keyless in-cluster path. Nothing about the Copilot
// path's hardening is implicit.
//
// One bound is operator-adjustable: how long an upstream may take to
// START answering (EGRESS_HEADER_TIMEOUT). The default stays 60 s, which
// is generous for a short completion — but a real
// workload exceeds it, and surfaces as a 502 with a stack trace
// pointing at http2 rather than at the cause: asking a reasoning
// model to draft release notes over ~9k tokens of pull-request listing
// can take longer than a minute to first token. That is a legitimate
// call, not a hung one, and a plane that cannot be told so would make
// the operator choose between the timeout and the workload. Everything
// else about the dialer stays fixed: this raises patience, never reach.
func hardenedClient(ctx context.Context, cfg config.Config) (*http.Client, error) {
	hosts := cfg.InternetHosts()
	policy := egress.Policy{}
	if raw := os.Getenv("EGRESS_HEADER_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 || d > 10*time.Minute {
			return nil, fmt.Errorf("EGRESS_HEADER_TIMEOUT %q: want a positive duration no greater than 10m", raw)
		}
		policy.ResponseHeaderTimeout = d
		slog.Info("egress: first-header timeout raised from the default", "timeout", d)
	}
	client, err := egress.NewClient(policy, hosts)
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		addrs, err := vetWithTimeout(ctx, h.Name)
		switch {
		case errors.Is(err, egress.ErrPrivateAddress):
			return nil, fmt.Errorf("hosted upstream refused at config load: %w", err)
		case err != nil:
			slog.Warn("hosted upstream host did not resolve at boot; every call re-vets it",
				"host", h.Name, "err", err)
		default:
			slog.Info("hosted upstream vetted", "host", h.Name, "addresses", addrs,
				"trust", trustOf(h))
		}
	}
	// The other direction (the second layer under config.hostedShape's
	// static rule): an UNMARKED upstream whose name resolves to a public
	// address would take the plain in-cluster dial around every check
	// here — refused, loudly, the same way. A name that does not resolve
	// at boot (AKS has no ollama, say) is left to the network.
	for _, name := range cfg.InClusterHosts() {
		if ip, err := netip.ParseAddr(name); err == nil {
			if egress.Refused(ip) == "" {
				return nil, fmt.Errorf("unmarked upstream refused at config load: %s is a public address; mark it internet: true", name)
			}
			continue
		}
		lookupCtx, cancel := context.WithTimeout(ctx, egress.DefaultConnectTimeout)
		addrs, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", name)
		cancel()
		if err != nil || len(addrs) == 0 {
			continue
		}
		for _, a := range addrs {
			if egress.Refused(a) == "" {
				return nil, fmt.Errorf("unmarked upstream refused at config load: %s resolves to public address %s; mark it internet: true", name, a)
			}
		}
	}
	return client, nil
}

// vetWithTimeout bounds one boot-time lookup so a hanging resolver cannot
// hold the whole startup.
func vetWithTimeout(ctx context.Context, host string) ([]netip.Addr, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, egress.DefaultConnectTimeout)
	defer cancel()
	return egress.Vet(lookupCtx, nil, host)
}

func trustOf(h egress.Host) string {
	if h.CAFile == "" {
		return "system roots"
	}
	return "ca_file " + h.CAFile
}
