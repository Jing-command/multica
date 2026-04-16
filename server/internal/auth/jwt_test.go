package auth

import (
	"os"
	"testing"
)

func TestJWTSecretReturnsEmptyWhenUnset(t *testing.T) {
	prev, had := os.LookupEnv("JWT_SECRET")
	if err := os.Unsetenv("JWT_SECRET"); err != nil {
		t.Fatalf("unset JWT_SECRET: %v", err)
	}
	resetJWTSecretForTest()
	defer func() {
		if had {
			_ = os.Setenv("JWT_SECRET", prev)
		} else {
			_ = os.Unsetenv("JWT_SECRET")
		}
		resetJWTSecretForTest()
	}()

	if got := string(JWTSecret()); got != "" {
		t.Fatalf("JWTSecret() = %q, want empty when JWT_SECRET is unset", got)
	}
}

func TestJWTSecretReturnsConfiguredValue(t *testing.T) {
	prev, had := os.LookupEnv("JWT_SECRET")
	if err := os.Setenv("JWT_SECRET", "configured-secret"); err != nil {
		t.Fatalf("set JWT_SECRET: %v", err)
	}
	resetJWTSecretForTest()
	defer func() {
		if had {
			_ = os.Setenv("JWT_SECRET", prev)
		} else {
			_ = os.Unsetenv("JWT_SECRET")
		}
		resetJWTSecretForTest()
	}()

	if got := string(JWTSecret()); got != "configured-secret" {
		t.Fatalf("JWTSecret() = %q, want configured-secret", got)
	}
}
