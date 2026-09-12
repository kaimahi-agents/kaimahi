package proxy_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/db"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRetiredToolAdminRoutesAndFilingsAreRejected(t *testing.T) {
	f := newFakeStore()
	f.addToken("kmh_x", store.Credential{Name: "model"})
	mux, token := adminMux(t, f)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/admin/tool-allowlist?credential=model", ""},
		{"PUT", "/admin/tool-allowlist", `{"credential":"model","tools":[]}`},
		{"GET", "/admin/tool-audit", ""},
	} {
		require.Equal(t, http.StatusNotFound, adminDo(mux, tc.method, tc.path, token, tc.body).Code, tc.path)
	}
	for _, body := range []string{
		`{"credential":"model","kind":"tool","subject":"tokens"}`,
		`{"credential":"model","kind":"tool","subject":"tokens","arguments":{}}`,
		`{"credential":"model","kind":"budget","subject":"tokens","arguments":null}`,
	} {
		require.Equal(t, http.StatusBadRequest, adminDo(mux, "POST", "/admin/requests", token, body).Code, body)
	}
	require.JSONEq(t, `{"pending":[]}`, adminDo(mux, "GET", "/admin/approvals", token, "").Body.String())
}

// Exercise the API over real historical rows, not a fake's kind validation.
func TestAdminCannotApproveHistoricalToolRequests(t *testing.T) {
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
	name := fmt.Sprintf("admin-retired-%d", time.Now().UnixNano())
	hash := sha256.Sum256([]byte(name))
	require.NoError(t, s.CreateCredential(ctx, name, hash[:], time.Now().Add(time.Hour)))
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("admin"), 0600))
	mux := proxy.NewAdminMux(proxy.Deps{Store: s}, tokenFile)
	for _, digest := range []string{"", strings.Repeat("a", 64)} {
		var id string
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO approval_request (credential_name,kind,subject,arg_digest,arg_summary) VALUES ($1,'tool','tokens',$2,'historical call') RETURNING id`, name, digest).Scan(&id))
		w := adminDo(mux, "POST", "/admin/approvals/"+id+"/approve", "admin", `{"max_uses":1}`)
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		w = adminDo(mux, "GET", "/admin/approvals", "admin", "")
		require.Contains(t, w.Body.String(), id)
		require.Contains(t, w.Body.String(), "historical call")
		require.Equal(t, http.StatusNoContent, adminDo(mux, "POST", "/admin/approvals/"+id+"/deny", "admin", "").Code)
	}
	grants, _, err := s.Grants(ctx, name, 10)
	require.NoError(t, err)
	require.Empty(t, grants)
}
