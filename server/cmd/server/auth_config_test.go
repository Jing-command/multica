package main

import (
	"os"
	"testing"
)

func TestValidateAuthConfigRequiresJWTSecret(t *testing.T) {
	prev, had := os.LookupEnv("JWT_SECRET")
	if err := os.Unsetenv("JWT_SECRET"); err != nil {
		t.Fatalf("unset JWT_SECRET: %v", err)
	}
	defer func() {
		if had {
			_ = os.Setenv("JWT_SECRET", prev)
		} else {
			_ = os.Unsetenv("JWT_SECRET")
		}
	}()

	if err := validateAuthConfig(); err == nil {
		t.Fatal("validateAuthConfig() = nil, want error when JWT_SECRET is unset")
	}
}

func TestValidateAuthConfigAllowsConfiguredJWTSecret(t *testing.T) {
	prev, had := os.LookupEnv("JWT_SECRET")
	if err := os.Setenv("JWT_SECRET", "configured-secret"); err != nil {
		t.Fatalf("set JWT_SECRET: %v", err)
	}
	defer func() {
		if had {
			_ = os.Setenv("JWT_SECRET", prev)
		} else {
			_ = os.Unsetenv("JWT_SECRET")
		}
	}()

	if err := validateAuthConfig(); err != nil {
		t.Fatalf("validateAuthConfig() error = %v, want nil", err)
	}
}
