package cli

import (
	"context"
	"testing"

	"water/internal/connectors/google/gapi"
	"water/internal/vault"
)

// `water connect google --revoke` must always remove the local secret, even
// when what is stored no longer parses (so there is nothing to revoke).
func TestRevokeGoogleDeletesUnparsableCredential(t *testing.T) {
	v := vault.NewMemory()
	if err := v.Set(gapi.Service, gapi.DefaultAccount, vault.NewSecret(`{"client_id":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if err := revokeGoogle(context.Background(), v, gapi.DefaultAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(gapi.Service, gapi.DefaultAccount); err == nil {
		t.Fatal("credential still in the vault after --revoke")
	}
}
