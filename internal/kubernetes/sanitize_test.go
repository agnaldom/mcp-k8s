package kubernetes

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSanitizeRemovesManagedFields(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name": "payments-api",
			"managedFields": []any{
				map[string]any{"manager": "kubectl"},
			},
		},
	}}
	SanitizeObject(obj)
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "metadata", "managedFields"); found {
		t.Error("managedFields must be removed in every view (spec §3.4)")
	}
}

func TestSanitizeRemovesLastApplied(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name": "payments-api",
			"annotations": map[string]any{
				lastAppliedAnnotation: `{"spec":{"replicas":3}}`,
				"example.com/owner":   "payments",
			},
		},
	}}
	SanitizeObject(obj)
	annotations := obj.GetAnnotations()
	if _, ok := annotations[lastAppliedAnnotation]; ok {
		t.Error("last-applied-configuration must be removed")
	}
	if annotations["example.com/owner"] != "payments" {
		t.Error("ordinary annotations must survive")
	}
}

func TestSanitizeTruncatesOversizedAnnotations(t *testing.T) {
	big := strings.Repeat("x", 12847)
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name": "payments-api",
			"annotations": map[string]any{
				"example.com/big": big,
			},
		},
	}}
	SanitizeObject(obj)
	got := obj.GetAnnotations()["example.com/big"]
	want := "[TRUNCATED: 12847 bytes]"
	if got != want {
		t.Errorf("got %q, want %q (spec §3.4)", got, want)
	}
}

func TestSizeErrorMessage(t *testing.T) {
	err := &SizeError{Kind: "ConfigMap", Name: "big", Size: 5 * 1024 * 1024, Cap: 4 * 1024 * 1024}
	msg := err.Error()
	if !strings.Contains(msg, "view=summary") || !strings.Contains(msg, "paginate") {
		t.Errorf("error must tell the consumer how to recover (spec §3.5): %q", msg)
	}
}

func TestSanitizeRedactsEnvValues(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"kind": "Pod",
		"spec": map[string]any{
			"containers": []any{
				map[string]any{
					"name": "api",
					"env": []any{
						map[string]any{"name": "DB_PASSWORD", "value": "super-secret"},
						map[string]any{"name": "FROM_SECRET", "valueFrom": map[string]any{
							"secretKeyRef": map[string]any{"name": "db-creds", "key": "password"},
						}},
					},
				},
			},
			"initContainers": []any{
				map[string]any{
					"name": "migrate",
					"env":  []any{map[string]any{"name": "TOKEN", "value": "abc123"}},
				},
			},
		},
	}}
	SanitizeObject(obj)

	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "containers")
	env := containers[0].(map[string]any)["env"].([]any)
	if env[0].(map[string]any)["value"] != "[REDACTED]" {
		t.Errorf("literal env value must be redacted, got %v", env[0])
	}
	vf := env[1].(map[string]any)["valueFrom"].(map[string]any)
	ref := vf["secretKeyRef"].(map[string]any)
	if ref["name"] != "db-creds" || ref["key"] != "password" {
		t.Errorf("secretKeyRef must keep name+key, got %v", ref)
	}

	inits, _, _ := unstructured.NestedSlice(obj.Object, "spec", "initContainers")
	initEnv := inits[0].(map[string]any)["env"].([]any)
	if initEnv[0].(map[string]any)["value"] != "[REDACTED]" {
		t.Error("initContainers env must be redacted too")
	}
}

func TestSanitizeLeavesEnvWithoutValue(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"kind": "Pod",
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "api", "env": []any{map[string]any{"name": "ONLY_NAME"}}},
			},
		},
	}}
	SanitizeObject(obj)
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "containers")
	env := containers[0].(map[string]any)["env"].([]any)
	if _, ok := env[0].(map[string]any)["value"]; ok {
		t.Error("env without a literal value must stay untouched")
	}
}
