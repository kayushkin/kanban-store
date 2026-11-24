package grantstore

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A grant-store the bridge serves gets the bridge's token and never the
// standalone store's own, which would be a second secret sent for nothing.
func TestTheBridgeTokenReplacesGrantStoresOwn(t *testing.T) {
	var bridgeToken, grantToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bridgeToken, grantToken = r.Header.Get(BridgeServiceTokenHeader), r.Header.Get(ServiceTokenHeader)
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := New(server.URL, "grant-store-token")
	if _, err := client.EffectiveBoardGrants("principal_000001"); err != nil {
		t.Fatal(err)
	}
	if bridgeToken != "" || grantToken != "grant-store-token" {
		t.Errorf("standalone: bridge %q, grant-store %q", bridgeToken, grantToken)
	}
	client.BridgeServiceToken = "bridge-token"
	if _, err := client.EffectiveBoardGrants("principal_000001"); err != nil {
		t.Fatal(err)
	}
	if bridgeToken != "bridge-token" || grantToken != "" {
		t.Errorf("in the bridge: bridge %q, grant-store %q", bridgeToken, grantToken)
	}
}
