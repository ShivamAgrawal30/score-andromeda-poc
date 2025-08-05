package internal

import (
	"slices"
)

const (
	AnnotationPrefix              = "andromeda.score.dev/"
	WorkloadKindAnnotation        = AnnotationPrefix + "kind"
	WorkloadServiceNameAnnotation = AnnotationPrefix + "service-name"
)

func ListAnnotations(metadata map[string]interface{}) []string {
	a, ok := metadata["annotations"].(map[string]interface{})
	if ok {
		out := make([]string, 0, len(a))
		for s, _ := range a {
			out = append(out, s)
		}
		slices.Sort(out)
		return out
	}
	return nil
}

func FindAnnotation(metadata map[string]interface{}, annotation string) (string, bool) {
	a, ok := metadata["annotations"].(map[string]interface{})
	if ok {
		if v, ok := a[annotation].(string); ok {
			return v, true
		}
	}
	return "", false
}
