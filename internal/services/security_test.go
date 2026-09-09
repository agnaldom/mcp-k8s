package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgo "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/agnaldom/mcp-k8s/internal/config"
	"github.com/agnaldom/mcp-k8s/internal/kubernetes"
	"github.com/agnaldom/mcp-k8s/internal/policy"
)

// Security regression suite (spec §6.1/§11). These tests block merge:
// each one pins a deterministic security control. Do not weaken one
// without a spec change.

// securityResourceService builds a ResourceService with a custom
// policy, mapper, and seeded objects.
func securityResourceService(sec config.Security, objs ...runtime.Object) *ResourceService {
	listKinds := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "deployments"}: "DeploymentList",
		{Group: "", Version: "v1", Resource: "secrets"}:     "SecretList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "", Version: "v1"}})
	for kind, resource := range map[string]string{"Deployment": "deployments", "Secret": "secrets"} {
		gvk := schema.GroupVersionKind{Kind: kind, Version: "v1"}
		mapper.AddSpecific(gvk,
			schema.GroupVersionResource{Version: "v1", Resource: resource},
			schema.GroupVersionResource{Version: "v1", Resource: strings.ToLower(kind)},
			meta.RESTScopeNamespace)
	}
	return &ResourceService{
		Policy: policy.New(sec),
		Clients: func(_ context.Context, _ string) (dynamic.Interface, discovery.DiscoveryInterface, meta.RESTMapper, error) {
			return dyn, (&fake.Clientset{}).Discovery(), mapper, nil
		},
		DefaultLimit:   100,
		MaxLimit:       500,
		MaxObjectBytes: 4 * 1024 * 1024,
	}
}

func securitySecret(name, ns string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"data":       map[string]any{"password": "aHVudGVyMg=="},
		"stringData": map[string]any{"token": "hunter2"},
	}}
}

// SecuritySecretBlockedByPolicy: Secret is refused even though the
// cluster (fake) would serve it — policy runs before resolution
// (spec §6.1).
func TestSecuritySecretBlockedByPolicy(t *testing.T) {
	svc := securityResourceService(config.Default().Security, securitySecret("db", "payments"))
	_, err := svc.Get(context.Background(), ResourceGetOptions{
		Cluster: "prod", Kind: "Secret", Name: "db", Namespace: "payments", View: ViewFull,
	})
	if !errors.Is(err, policy.ErrDenied) {
		t.Fatalf("Secret must be denied by policy, got %v", err)
	}
}

// SecuritySecretDataNeverSerialized: even when policy allows the Kind
// (admin misconfiguration), data and stringData never leave the
// server — in any view (spec §6.1).
func TestSecuritySecretDataNeverSerialized(t *testing.T) {
	sec := config.Default().Security
	sec.Resources.Deny = nil // policy misconfigured: Secret allowed
	svc := securityResourceService(sec, securitySecret("db", "payments"))
	item, err := svc.Get(context.Background(), ResourceGetOptions{
		Cluster: "prod", Kind: "Secret", Name: "db", Namespace: "payments", View: ViewFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(item.Object)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "aHVudGVyMg==") {
		t.Fatalf("Secret material leaked into response: %s", raw)
	}
}

// SecurityEnvValueRedacted: literal env values become [REDACTED];
// secretKeyRef keeps names only (spec §6.1).
func TestSecurityEnvValueRedacted(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": "api", "namespace": "payments"},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{
					"name":  "app",
					"image": "example.com/app:1",
					"env": []any{
						map[string]any{"name": "PLAIN", "value": "hunter2"},
						map[string]any{"name": "FROM_SECRET", "valueFrom": map[string]any{
							"secretKeyRef": map[string]any{"name": "db", "key": "password"},
						}},
					},
				},
			},
		},
	}}
	// Pod is not in the resource mapper above; sanitize directly through
	// the same code path the service uses, then assert on the result.
	kubernetes.SanitizeObject(pod)
	raw, _ := json.Marshal(pod.Object)
	if strings.Contains(string(raw), "hunter2") {
		t.Fatalf("literal env value leaked: %s", raw)
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("env value must be [REDACTED]: %s", raw)
	}
	var check map[string]any
	_ = json.Unmarshal(raw, &check)
	env := check["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"].([]any)
	ref := env[1].(map[string]any)["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
	if ref["name"] != "db" || ref["key"] != "password" {
		t.Errorf("secretKeyRef names must survive: %v", ref)
	}
	if _, hasValue := ref["value"]; hasValue {
		t.Errorf("secretKeyRef must never carry a value: %v", ref)
	}
}

// SecurityManagedFieldsRemoved: managedFields and
// last-applied-configuration never appear in responses (spec §3.4/§6.1).
func TestSecurityManagedFieldsRemoved(t *testing.T) {
	dep := deployment("payments-api", "payments", 3)
	dep.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply", Operation: metav1.ManagedFieldsOperationApply}})
	dep.SetAnnotations(map[string]string{
		"team": "payments",
		"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"apps/v1","kind":"Deployment","spec":{"replicas":3,"template":{"spec":{"containers":[{"env":[{"name":"PASSWORD","value":"hunter2"}]}]}}}}`,
	})
	svc := newResourceService(dep)
	item, err := svc.Get(context.Background(), ResourceGetOptions{
		Cluster: "prod", Kind: "Deployment", Name: "payments-api", Namespace: "payments", View: ViewFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(item.Object)
	if strings.Contains(string(raw), "managedFields") {
		t.Errorf("managedFields leaked: %s", raw)
	}
	if strings.Contains(string(raw), "last-applied-configuration") {
		t.Errorf("last-applied annotation leaked: %s", raw)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Errorf("last-applied content leaked material: %s", raw)
	}
}

// SecurityNamespacePolicyRespected: denied namespaces are filtered
// from every response (spec §7).
func TestSecurityNamespacePolicyRespected(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
	)
	svc := &NamespaceService{
		Policy:  policy.New(config.Default().Security),
		Clients: func(_ context.Context, _ string) (clientgo.Interface, error) { return clientset, nil },
	}
	names, err := svc.List(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if n == "kube-system" {
			t.Fatalf("denied namespace leaked into response: %v", names)
		}
	}
	if len(names) != 1 || names[0] != "payments" {
		t.Errorf("namespaces: %v", names)
	}
}

// SecurityClusterPolicyRespected: a cluster outside the allow list is
// refused (spec §7).
func TestSecurityClusterPolicyRespected(t *testing.T) {
	sec := config.Default().Security
	sec.Clusters.Allow = []string{"prod"}
	svc := securityResourceService(sec, deployment("payments-api", "payments", 3))
	_, err := svc.List(context.Background(), ResourceListOptions{
		Cluster: "staging", Kind: "Deployment", Namespace: "payments", View: ViewSummary,
	})
	if !errors.Is(err, policy.ErrDenied) {
		t.Fatalf("cluster outside allow list must be denied, got %v", err)
	}
}

// SecurityResponseLimitRespected: a single object beyond the response
// cap is an error, never a silently cut JSON (spec §3.5/§8).
func TestSecurityResponseLimitRespected(t *testing.T) {
	svc := newResourceService(deployment("payments-api", "payments", 3))
	svc.MaxObjectBytes = 64
	_, err := svc.Get(context.Background(), ResourceGetOptions{
		Cluster: "prod", Kind: "Deployment", Name: "payments-api", Namespace: "payments", View: ViewFull,
	})
	var sizeErr *kubernetes.SizeError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("expected SizeError, got %v", err)
	}
}

// SecurityLogLimitRespected: log responses are capped at the configured
// byte budget (spec §8).
func TestSecurityLogLimitRespected(t *testing.T) {
	clientset := fake.NewSimpleClientset(podWithContainers("api", "payments", "app"))
	body := []byte(strings.Repeat("log-line-with-some-content\n", 200))
	clientset.Fake.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "log" {
			return false, nil, nil
		}
		return true, &runtime.Unknown{Raw: body}, nil
	})
	svc := &LogService{
		Policy:       policy.New(config.Default().Security),
		Clients:      func(_ context.Context, _ string) (clientgo.Interface, error) { return clientset, nil },
		MaxTailLines: 5000,
		MaxBytes:     256,
		Timeout:      logsTimeout,
	}
	res, err := svc.Logs(context.Background(), PodLogsOptions{
		Cluster: "prod", Namespace: "payments", Pod: "api", Container: "app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Logs) > 256 {
		t.Fatalf("logs exceed the cap: %d bytes", len(res.Logs))
	}
	if !res.Truncated {
		t.Error("truncation must be reported")
	}
}
