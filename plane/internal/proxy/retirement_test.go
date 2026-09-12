package proxy_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

func TestRetiredInboundRequestKindIsRejected(t *testing.T) {
	f := newFakeStore()
	f.addToken("kmh_x", store.Credential{Name: "hook"})
	mux, token := adminMux(t, f)
	w := adminDo(mux, "POST", "/admin/requests", token,
		`{"credential":"hook","kind":"inbound","subject":"demo"}`)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	w = adminDo(mux, "GET", "/admin/approvals", token, "")
	require.JSONEq(t, `{"pending":[]}`, w.Body.String(), "a retired kind must not file a request")
}

func TestRetiredInboundAuditIsNotServed(t *testing.T) {
	mux, token := adminMux(t, newFakeStore())
	w := adminDo(mux, "GET", "/admin/inbound-audit", token, "")
	require.Equal(t, http.StatusNotFound, w.Code)
}
