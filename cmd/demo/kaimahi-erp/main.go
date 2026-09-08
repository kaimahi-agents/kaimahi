// kaimahi-erp is the fixture-backed ERP the accounts-payable demo
// investigates (docs/ap-demo.md). It is an MCP tool server over an
// in-memory corpus: no database, no credential, no outbound call.
//
// The corpus is NOT in this binary. It is read from the file named by
// -fixtures, which is a ConfigMap projection in the cluster
// (k8s/erp-mcp.yaml, from k8s/erp-fixtures.json), so the story can be
// edited and re-applied without a rebuild. A corpus whose arithmetic does
// not add up is refused here, at boot, rather than quietly answering an
// audience's arithmetic wrongly.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/demo/erp"
)

func main() {
	addr := flag.String("addr", ":8085", "listen address")
	path := flag.String("fixtures", "/etc/kaimahi/erp/fixtures.json", "path to the fixture corpus")
	flag.Parse()

	f, err := os.Open(*path)
	if err != nil {
		slog.Error("erp: cannot read the fixture corpus", "path", *path, "err", err)
		os.Exit(1)
	}
	fx, err := erp.Load(f)
	_ = f.Close()
	if err != nil {
		slog.Error("erp: refusing to serve an inconsistent corpus", "path", *path, "err", err)
		os.Exit(1)
	}
	slog.Info("erp: corpus loaded", "path", *path,
		"vendors", len(fx.Vendors), "invoices", len(fx.Invoices), "purchase_orders", len(fx.PurchaseOrders))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           erp.New(fx).Mux(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("erp: serving MCP", "addr", *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("erp: server stopped", "err", err)
		os.Exit(1)
	}
}
