package proxy_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/db"
	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
	"github.com/stretchr/testify/require"
)

func TestBudgetRetirementLeavesSQLHistoryUntouchedOnTheDataAndAdminPaths(t *testing.T) {
	dsn := os.Getenv("KAIMAHI_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KAIMAHI_TEST_PG_DSN not set")
	}
	ctx := context.Background()
	require.NoError(t, db.Migrate(ctx, dsn))
	pool, err := db.NewPool(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	s := store.New(pool)
	tokenFile := filepath.Join(t.TempDir(), "admin-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("admin"), 0600))
	admin := proxy.NewAdminMux(proxy.Deps{Store: s}, tokenFile)

	for _, subject := range []string{"cents", "tokens"} {
		for _, history := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/history=%t", subject, history), func(t *testing.T) {
				name := fmt.Sprintf("budget-retired-%d", time.Now().UnixNano())
				hash := sha256.Sum256([]byte(name))
				require.NoError(t, s.CreateCredential(ctx, name, hash[:], time.Now().Add(time.Hour)))
				msg := "monthly token budget reached\n"
				if subject == "cents" {
					require.NoError(t, s.SetBudget(ctx, name, i64(0), nil))
					msg = "monthly budget reached\n"
				} else {
					require.NoError(t, s.SetBudget(ctx, name, nil, i64(0)))
				}
				id := "00000000-0000-0000-0000-000000000001"
				if history {
					require.NoError(t, pool.QueryRow(ctx,
						`INSERT INTO approval_request (credential_name,kind,subject,status,decided_at,decided_by)
						 VALUES ($1,'budget',$2,'approved',now(),'admin') RETURNING id`, name, subject).Scan(&id))
					_, err := pool.Exec(ctx,
						`INSERT INTO permit_grant (request_id,credential_name,kind,subject,expires_at,max_uses,amount,decided_by)
						 VALUES ($1,$2,'budget',$3,now() + interval '1 hour',100,1000,'admin')`, id, name, subject)
					require.NoError(t, err)
					_, err = pool.Exec(ctx,
						`INSERT INTO approval_audit (request_id,credential_name,kind,subject,action,decided_by)
						 VALUES ($1,$2,'budget',$3,'approved','admin')`, id, name, subject)
					require.NoError(t, err)
				}
				snapshot := func() []string {
					var out []string
					for _, table := range []string{"approval_request", "permit_grant", "approval_audit"} {
						var rows string
						require.NoError(t, pool.QueryRow(ctx,
							`SELECT COALESCE(jsonb_agg(to_jsonb(h) ORDER BY id), '[]'::jsonb)::text FROM `+table+` h WHERE credential_name = $1`, name).Scan(&rows))
						out = append(out, rows)
					}
					return out
				}
				before := snapshot()
				up, got, _ := newUpstream(t)
				data := proxy.NewDataMux(proxy.Deps{Store: s, Meter: &meter.Meter{Store: s},
					Config: config.Config{Upstreams: map[string]config.Upstream{
						"ollama": {Protocol: config.ProtocolChatCompletions, BaseURL: up.URL, Path: "v1/chat/completions", Classification: config.ClassFree},
					}}})
				for range 3 {
					w := doChat(t, data, name, "/upstream/ollama/v1/chat/completions", chatBody)
					require.Equal(t, http.StatusTooManyRequests, w.Code)
					require.Equal(t, msg, w.Body.String())
				}
				require.Empty(t, got.Method, "no upstream call under an exhausted cap")
				for _, route := range []struct{ method, path, body string }{
					{"POST", "/admin/requests", fmt.Sprintf(`{"credential":%q,"kind":"budget","subject":%q}`, name, subject)},
					{"GET", "/admin/approvals", ""},
					{"POST", "/admin/approvals/" + id + "/approve", `{"max_uses":1,"amount":100}`},
					{"POST", "/admin/approvals/" + id + "/deny", ""},
					{"GET", "/admin/grants?credential=" + name, ""},
					{"GET", "/admin/approval-audit?credential=" + name, ""},
				} {
					require.Equal(t, http.StatusNotFound, adminDo(admin, route.method, route.path, "admin", route.body).Code, route.path)
				}
				require.Equal(t, before, snapshot(), "no filing, audit, consumption, or history rewrite")
				rows, err := s.Ledger(ctx, name, 10)
				require.NoError(t, err)
				require.Len(t, rows, 3, "the spend ledger still records every refusal")
				for _, row := range rows {
					require.Equal(t, http.StatusTooManyRequests, row.Status)
					require.Equal(t, "denied", row.CostSource)
					require.Zero(t, row.CostCents)
					require.Zero(t, row.InputTokens+row.OutputTokens)
				}
				open, err := s.OpenReservations(ctx, name)
				require.NoError(t, err)
				require.Zero(t, open)
			})
		}
	}
}
