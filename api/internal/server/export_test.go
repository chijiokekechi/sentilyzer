package server

import (
	"context"

	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
)

// CredsFromGRPCForTest exposes the unexported gRPC metadata extraction to the
// server_test package.
func CredsFromGRPCForTest(ctx context.Context) connectors.Credentials {
	return credsFromGRPC(ctx)
}
