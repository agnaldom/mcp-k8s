//go:build integration

// Package integration runs the mcp-k8s stack against a real kind
// cluster with the fixtures from spec §11. It is gated behind the
// "integration" build tag: `go test -tags integration ./...` (the CI
// job creates the kind cluster first; locally run `make kind-up`).
package integration

import (
	"bytes"
	"context"
	_ "embed"
	"os"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

//go:embed fixtures.yaml
var fixturesYAML []byte

const (
	fixtureNamespace = "mcp-k8s-fixtures"
	testCluster      = "integration" // policy label only; not resolved
)

// env carries the client bundle the tests exercise.
type env struct {
	typed    kubernetes.Interface
	dynamic  dynamic.Interface
	discover discovery.DiscoveryInterface
	mapper   meta.RESTMapper
}

func loadEnv(t *testing.T) *env {
	t.Helper()
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path := os.Getenv("INTEGRATION_KUBECONFIG"); path != "" {
		rules.ExplicitPath = path
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: os.Getenv("INTEGRATION_CONTEXT")}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		t.Fatalf("load kubeconfig (create one with 'make kind-up'): %v", err)
	}
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := restmapper.GetAPIGroupResources(disc)
	if err != nil {
		t.Fatal(err)
	}
	return &env{
		typed:    typed,
		dynamic:  dyn,
		discover: disc,
		mapper:   restmapper.NewDiscoveryRESTMapper(resources),
	}
}

// applyFixtures creates the namespace and every fixture object,
// ignoring already-existing ones so reruns are idempotent.
func (e *env) applyFixtures(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	ns := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": fixtureNamespace},
	}}
	if _, err := e.dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}

	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(fixturesYAML), 4096)
	for {
		obj := &unstructured.Unstructured{}
		if err := dec.Decode(obj); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("decode fixture: %v", err)
		}
		if obj.Object == nil {
			continue
		}
		if obj.GetKind() == "Namespace" {
			continue // created above
		}
		gvk := obj.GroupVersionKind()
		mapping, err := e.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			t.Fatalf("map %s: %v", gvk.String(), err)
		}
		obj.SetNamespace(fixtureNamespace)
		var errCreate error
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			_, errCreate = e.dynamic.Resource(mapping.Resource).Namespace(fixtureNamespace).Create(ctx, obj, metav1.CreateOptions{})
		} else {
			_, errCreate = e.dynamic.Resource(mapping.Resource).Create(ctx, obj, metav1.CreateOptions{})
		}
		if errCreate != nil && !apierrors.IsAlreadyExists(errCreate) {
			t.Fatalf("create %s %s: %v", gvk.Kind, obj.GetName(), errCreate)
		}
	}
}

// waitFor polls cond until it returns true, failing the test after
// timeout. Pods take a while to fail in the expected ways
// (CrashLoopBackOff needs several restarts); the generous defaults keep
// the suite deterministic on a slow CI worker.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out (%s) waiting for %s", timeout, what)
		}
		time.Sleep(3 * time.Second)
	}
}

// evalSignals runs SignalService.Evaluate against the test cluster and
// returns the fired signal types.
func (e *env) evalSignals(t *testing.T, kind, name string) map[string]bool {
	t.Helper()
	svc := newSignalService(e)
	res, err := svc.Evaluate(context.Background(), signalOptions(testCluster, fixtureNamespace, kind, name))
	if err != nil {
		t.Fatalf("signals %s/%s: %v", kind, name, err)
	}
	out := map[string]bool{}
	for _, s := range res.Signals {
		out[s.Type] = true
	}
	return out
}

// workloadContext runs WorkloadService.Context against the test cluster.
func (e *env) workloadContext(t *testing.T, kind, name string, includeMetrics bool) *workloadResultAlias {
	t.Helper()
	svc := newWorkloadService(e, includeMetrics)
	res, err := svc.Context(context.Background(), workloadOptions(testCluster, fixtureNamespace, kind, name, includeMetrics))
	if err != nil {
		t.Fatalf("workload context %s/%s: %v", kind, name, err)
	}
	return res
}
