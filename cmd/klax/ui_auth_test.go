package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLookupTokenExactMatchOnly(t *testing.T) {
	s := &uiServer{tokens: map[string]uiAccess{
		"rw-token-aaaaaaaa": {User: "alice"},
		"ro-token-bbbbbbbb": {User: "alice", ReadOnly: true},
	}}
	if a, ok := s.lookupToken("rw-token-aaaaaaaa"); !ok || a.User != "alice" || a.ReadOnly {
		t.Errorf("rw token: %+v %v", a, ok)
	}
	if a, ok := s.lookupToken("ro-token-bbbbbbbb"); !ok || !a.ReadOnly {
		t.Errorf("ro token: %+v %v", a, ok)
	}
	for _, bad := range []string{"", "rw-token-aaaaaaa", "rw-token-aaaaaaaaa", "RW-TOKEN-AAAAAAAA", "rw-token-aaaaaaaa "} {
		if _, ok := s.lookupToken(bad); ok {
			t.Errorf("%q must not authenticate", bad)
		}
	}
}

func TestAccessRequiresBearerScheme(t *testing.T) {
	s := &uiServer{tokens: map[string]uiAccess{"tok": {User: "u"}}}
	for header, want := range map[string]bool{"Bearer tok": true, "Bearer wrong": false, "tok": false, "Basic tok": false, "": false} {
		r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		if _, ok := s.access(r); ok != want {
			t.Errorf("Authorization %q: ok = %v, want %v", header, ok, want)
		}
	}
}

func TestUIServerHasConnectionLimits(t *testing.T) {
	srv := newUIHTTPServer("127.0.0.1:0", http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 || srv.IdleTimeout <= 0 || srv.MaxHeaderBytes <= 0 {
		t.Fatalf("missing limits: %+v", srv)
	}
	// Long polls and big uploads must not be cut off by a whole-request timeout.
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Fatalf("ReadTimeout/WriteTimeout would break uploads and long polls: %v %v", srv.ReadTimeout, srv.WriteTimeout)
	}
}
