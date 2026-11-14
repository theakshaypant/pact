package pacdef

import (
	"encoding/json"

	k8syaml "sigs.k8s.io/yaml"
)

// noiseKeys are keys the typed API objects marshal even when the field
// is unset: status, creationTimestamp, taskRunTemplate and the empty
// metadata, spec and computeResources of embedded tasks and steps.
// Hand-written fixtures omit them, so the prune pass drops them again.
// The set mirrors noiseKeys in pkg/pac/render.go; keep the two in sync.
var noiseKeys = map[string]bool{
	"status":            true,
	"taskRunTemplate":   true,
	"creationTimestamp": true,
	"metadata":          true,
	"computeResources":  true,
	"spec":              true,
}

// render marshals obj and prunes the noise keys out of the result.
// Marshal failures panic: the fixtures are static, so they cannot fail.
func render(obj any) string {
	// The prune pass works on a generic map, so the struct goes through
	// JSON directly; only the final document is YAML.
	raw, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		panic(err)
	}
	prune(doc)
	out, err := k8syaml.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// prune removes empty noise keys from m, recursing into nested maps
// and the maps inside sequences. Recursing first means a noise key
// whose only content is itself noise is emptied and then dropped.
func prune(m map[string]any) {
	for k, v := range m {
		pruneValue(v)
		if noiseKeys[k] && isEmpty(v) {
			delete(m, k)
		}
	}
}

func pruneValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		prune(t)
	case []any:
		for _, item := range t {
			pruneValue(item)
		}
	}
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}
