package main

import (
	"context"
	"fmt"
	"os"

	"github.com/1password/onepassword-sdk-go"
)

func loadSecrets(ctx context.Context) (map[string]string, error) {
	opAccountID := os.Getenv("OP_ACCOUNT_ID")
	if opAccountID == "" {
		return nil, fmt.Errorf("OP_ACCOUNT_ID is not set — copy .env.local.example to .env.local and fill it in")
	}
	opEnvironment := os.Getenv("OP_ENVIRONMENT_ID")
	if opEnvironment == "" {
		return nil, fmt.Errorf("OP_ENVIRONMENT_ID is not set — copy .env.local.example to .env.local and fill it in")
	}

	client, err := onepassword.NewClient(ctx,
		onepassword.WithDesktopAppIntegration(opAccountID),
		onepassword.WithIntegrationInfo("tynet-runpod", "0.1.0"),
	)
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
