package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

func TestProviderSectionMissing(t *testing.T) {
	if _, _, err := Provider(context.Background(), &bootstrapv1.Workflow{}); err == nil {
		t.Fatal("expected error when argo section is missing")
	}
}

func TestProviderBuildsClient(t *testing.T) {
	out, cleanup, err := Provider(context.Background(), &bootstrapv1.Workflow{
		Argo: &bootstrapv1.Workflow_Argo{
			ServerUrl:          "http://127.0.0.1:2746",
			Namespace:          "prod",
			Token:              "tk",
			InsecureSkipVerify: true,
		},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	defer cleanup()
	if out == nil {
		t.Fatal("client should not be nil")
	}
	if _, ok := out.(interface{ Close() error }); !ok {
		t.Fatal("client should expose Close()")
	}
}
