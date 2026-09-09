package kubernetes

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// lastAppliedAnnotation is dropped always (spec §3.4): it duplicates the
// whole object and has no operational value.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// maxAnnotationBytes bounds annotation values in responses (spec §3.4).
const maxAnnotationBytes = 4 * 1024

// SanitizeObject applies the removals that happen in every view (spec §3.4):
// managedFields, last-applied-configuration, and oversized annotations.
// It mutates the copy it is given.
func SanitizeObject(obj *unstructured.Unstructured) {
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")

	annotations := obj.GetAnnotations()
	if len(annotations) == 0 {
		return
	}
	clean := make(map[string]string, len(annotations))
	for k, v := range annotations {
		if k == lastAppliedAnnotation {
			continue
		}
		if len(v) > maxAnnotationBytes {
			clean[k] = fmt.Sprintf("[TRUNCATED: %d bytes]", len(v))
			continue
		}
		clean[k] = v
	}
	obj.SetAnnotations(clean)
}

// SizeError reports that a single object exceeds the response cap
// (spec §3.5): the answer is an error, never a silently cut JSON.
type SizeError struct {
	Kind string
	Name string
	Size int64
	Cap  int64
}

func (e *SizeError) Error() string {
	return fmt.Sprintf("object %s/%s is %d bytes, exceeding the %d-byte response cap: request view=summary or paginate",
		e.Kind, e.Name, e.Size, e.Cap)
}
