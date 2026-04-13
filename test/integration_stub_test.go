//go:build !integration

package integration

import "testing"

func TestIntegrationRequiresTag(t *testing.T) {
	t.Skip("run with: go test -tags=integration -timeout=10m ./test/... (requires docker compose services)")
}
