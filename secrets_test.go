package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/1password/onepassword-sdk-go"
)

func TestLoadSecrets_MissingAccountID(t *testing.T) {
	t.Setenv("OP_ACCOUNT_ID", "")
	t.Setenv("OP_ENVIRONMENT_ID", "env-id")
	_, err := loadSecrets(context.Background())
	if err == nil || !strings.Contains(err.Error(), "OP_ACCOUNT_ID") {
		t.Errorf("loadSecrets() error = %v, want it to mention OP_ACCOUNT_ID", err)
	}
}

func TestLoadSecrets_MissingEnvironmentID(t *testing.T) {
	t.Setenv("OP_ACCOUNT_ID", "acct-id")
	t.Setenv("OP_ENVIRONMENT_ID", "")
	_, err := loadSecrets(context.Background())
	if err == nil || !strings.Contains(err.Error(), "OP_ENVIRONMENT_ID") {
		t.Errorf("loadSecrets() error = %v, want it to mention OP_ENVIRONMENT_ID", err)
	}
}

func TestLoadSecrets_ClientConnectionErrorIsWrapped(t *testing.T) {
	t.Setenv("OP_ACCOUNT_ID", "acct-id")
	t.Setenv("OP_ENVIRONMENT_ID", "env-id")

	orig := newOPClient
	t.Cleanup(func() { newOPClient = orig })
	wantErr := errors.New("no desktop app running")
	var gotAccountID string
	newOPClient = func(ctx context.Context, accountID string) (*onepassword.Client, error) {
		gotAccountID = accountID
		return nil, wantErr
	}

	_, err := loadSecrets(context.Background())
	if err == nil || !strings.Contains(err.Error(), "connecting to 1Password desktop app") {
		t.Errorf("loadSecrets() error = %v, want it to wrap the connection error", err)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("loadSecrets() error = %v, want it to wrap %v", err, wantErr)
	}
	if gotAccountID != "acct-id" {
		t.Errorf("newOPClient called with accountID = %q, want %q", gotAccountID, "acct-id")
	}
}

func TestSecretsLoaderDefaultsToLoadSecrets(t *testing.T) {
	// secretsLoader is a package var seam; confirm it starts out wired to the
	// real loadSecrets rather than something tests silently overrode earlier.
	t.Setenv("OP_ACCOUNT_ID", "")
	_, err := secretsLoader(context.Background())
	if err == nil || !strings.Contains(err.Error(), "OP_ACCOUNT_ID") {
		t.Errorf("secretsLoader() error = %v, want the same validation as loadSecrets()", err)
	}
}
