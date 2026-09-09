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
// It also enforces the secret controls (spec §6.1): literal env values
// become [REDACTED]; secretKeyRef keeps the secret and key names, never
// a resolved value. It mutates the copy it is given.
func SanitizeObject(obj *unstructured.Unstructured) {
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	redactContainerEnv(obj.Object)

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

// containerEnvPaths are the spec locations that carry container env.
var containerEnvPaths = [][]string{
	{"spec", "containers"},
	{"spec", "initContainers"},
	{"spec", "ephemeralContainers"},
}

// redactContainerEnv replaces literal env values with [REDACTED] in every
// container list (spec §6.1). valueFrom.secretKeyRef is left intact: it
// names the secret and key but never resolves the value.
func redactContainerEnv(object map[string]any) {
	for _, path := range containerEnvPaths {
		containers, found, _ := unstructured.NestedSlice(object, path...)
		if !found {
			continue
		}
		for i, c := range containers {
			container, ok := c.(map[string]any)
			if !ok {
				continue
			}
			env, found, _ := unstructured.NestedSlice(container, "env")
			if !found {
				continue
			}
			for j, e := range env {
				entry, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if _, hasValue := entry["value"]; hasValue {
					entry["value"] = "[REDACTED]"
					env[j] = entry
				}
			}
			container["env"] = env
			containers[i] = container
		}
		_ = unstructured.SetNestedSlice(object, containers, path...)
	}
}
