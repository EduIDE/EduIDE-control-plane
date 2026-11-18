// Package deps pins the dependencies the work packages share, so parallel
// branches do not each edit go.mod. Delete an import once real code uses it.
package deps

import (
	_ "github.com/coreos/go-oidc/v3/oidc"
	_ "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	_ "github.com/getkin/kin-openapi/openapi3filter"
	_ "golang.org/x/time/rate"
	_ "google.golang.org/grpc"
	_ "google.golang.org/grpc/health/grpc_health_v1"
	_ "k8s.io/client-go/kubernetes"
	_ "sigs.k8s.io/controller-runtime"
	_ "sigs.k8s.io/controller-runtime/pkg/envtest"
	_ "sigs.k8s.io/gateway-api/apis/v1"
)
