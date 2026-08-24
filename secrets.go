package main

import (
	"context"
	"fmt"

	"github.com/1password/onepassword-sdk-go"
)

const (
	opAccountID   = "C5FUHLGUOBGJRA2PNDVTXY2MOU"
	opEnvironment = "7dmvokrkozogpo7laeuly6vdse" // tynet-runpod
)

func loadSecrets(ctx context.Context) (map[string]string, error) {
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
