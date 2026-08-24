package main

import (
	"context"
	"fmt"
	"os"

	"github.com/1password/onepassword-sdk-go"
)

// secretsLoader is overridden in tests so command-layer tests don't need a
// live, unlocked 1Password desktop app.
var secretsLoader = loadSecrets

// newOPClient is overridden in tests to exercise loadSecrets' error-wrapping
// path without a real 1Password connection. The success path (an actual
// *onepassword.Client) can't be faked this way — the SDK type is concrete,
// not an interface — so it's only exercised by a live desktop-app session.
var newOPClient = func(ctx context.Context, accountID string) (*onepassword.Client, error) {
	return onepassword.NewClient(ctx,
		onepassword.WithDesktopAppIntegration(accountID),
		onepassword.WithIntegrationInfo("tynet-runpod", "0.1.0"),
	)
}

func loadSecrets(ctx context.Context) (map[string]string, error) {
	opAccountID := os.Getenv("OP_ACCOUNT_ID")
	if opAccountID == "" {
		return nil, fmt.Errorf("OP_ACCOUNT_ID is not set — copy .env.local.example to .env.local and fill it in")
	}
	opEnvironment := os.Getenv("OP_ENVIRONMENT_ID")
	if opEnvironment == "" {
		return nil, fmt.Errorf("OP_ENVIRONMENT_ID is not set — copy .env.local.example to .env.local and fill it in")
	}

	client, err := newOPClient(ctx, opAccountID)
	if err != nil {
		return nil, fmt.Errorf("connecting to 1Password desktop app: %w", err)
	}

	resp, err := client.Environments().GetVariables(ctx, opEnvironment)
	if err != nil {
		return nil, fmt.Errorf("reading tynet-runpod Environment: %w", err)
	}

	vars := make(map[string]string, len(resp.Variables))
	for _, v := range resp.Variables {
		vars[v.Name] = v.Value
	}
	return vars, nil
}
