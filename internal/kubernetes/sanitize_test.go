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
