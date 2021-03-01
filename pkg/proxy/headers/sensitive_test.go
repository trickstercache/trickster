package headers

import "testing"

func TestHideAuthorizationCredentials(t *testing.T) {
	hdrs := map[string]string{NameAuthorization: "Basic SomeHash"}
	HideAuthorizationCredentials(hdrs)
	if hdrs[NameAuthorization] != "*****" {
		t.Errorf("expected '*****' got '%s'", hdrs[NameAuthorization])
	}
}
