package runner

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tektoncd/pipeline/pkg/apis/pipeline/pod"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Secrets are user-supplied secret contents, created as real Kubernetes
// Secrets in the run namespace before any PipelineRun is submitted:
// secret name -> key -> value. Manifests consume them by name through
// the standard Tekton shapes (secretKeyRef, envFrom, volumes,
// workspaces, imagePullSecrets).
type Secrets map[string]map[string]string

// NewSecrets groups flat "name.key" -> value entries into Secrets. The
// name is everything before the first dot and must be a DNS-1123 label
// (a dot-free name keeps the split unambiguous); the key is the rest and
// may contain dots (.dockerconfigjson, config.json). An empty map
// returns nil secrets.
func NewSecrets(flat map[string]string) (Secrets, error) {
	if len(flat) == 0 {
		return nil, nil
	}
	secrets := Secrets{}
	for _, ref := range slices.Sorted(maps.Keys(flat)) {
		name, key, found := strings.Cut(ref, ".")
		if !found {
			return nil, fmt.Errorf("invalid secret %q: want name.key", ref)
		}
		if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
			return nil, fmt.Errorf("invalid secret name %q: %s", name, strings.Join(errs, "; "))
		}
		if errs := validation.IsConfigMapKey(key); len(errs) > 0 {
			return nil, fmt.Errorf("invalid secret key %q in %q: %s", key, name, strings.Join(errs, "; "))
		}
		if secrets[name] == nil {
			secrets[name] = map[string]string{}
		}
		secrets[name][key] = flat[ref]
	}
	return secrets, nil
}

// newSecret builds the Kubernetes Secret for one supplied secret. A
// secret whose only key is .dockerconfigjson is typed
// kubernetes.io/dockerconfigjson, which the kubelet requires of image
// pull secrets; everything else is opaque.
func newSecret(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		StringData: data,
	}
	if len(data) == 1 {
		if _, ok := data[corev1.DockerConfigJsonKey]; ok {
			s.Type = corev1.SecretTypeDockerConfigJson
		}
	}
	return s
}

// secretRef is one Secret reference found in a PipelineRun; key is
// empty for whole-secret references (volumes, envFrom,
// imagePullSecrets).
type secretRef struct {
	name string
	key  string
}

// display renders the ref in --secret syntax, so a warning is
// copy-pasteable.
func (r secretRef) display() string {
	if r.key == "" {
		return r.name
	}
	return r.name + "." + r.key
}

// missing returns the sorted, de-duplicated refs the given PipelineRuns
// reference but s does not supply. A pod referencing a missing secret
// hangs in CreateContainerConfigError until the timeout, so the runner
// warns on these before submitting.
func (s Secrets) missing(prs []*tektonv1.PipelineRun) []string {
	var missing []string
	for _, pr := range prs {
		for _, ref := range referencedSecrets(pr) {
			if !s.supplies(ref) {
				missing = append(missing, ref.display())
			}
		}
	}
	slices.Sort(missing)
	return slices.Compact(missing)
}

// supplies reports whether the secret ref is satisfied: its secret
// exists, and for keyed refs (secretKeyRef) that secret has the key.
func (s Secrets) supplies(ref secretRef) bool {
	keys, ok := s[ref.name]
	if !ok {
		return false
	}
	if ref.key == "" {
		return true
	}
	_, ok = keys[ref.key]
	return ok
}

// referencedSecrets walks a resolved PipelineRun for the Secret
// references its pods will resolve: secretKeyRef and envFrom env vars,
// secret volumes, secret-backed workspaces and imagePullSecrets, across
// the pipeline's tasks and finally tasks, step templates, steps and
// sidecars. Optional references and $(params.x)-templated names are
// skipped: the first is allowed to be missing, the second cannot be
// resolved offline.
func referencedSecrets(pr *tektonv1.PipelineRun) []secretRef {
	var refs []secretRef
	add := func(name, key string, optional *bool) {
		if optional != nil && *optional {
			return
		}
		if strings.Contains(name, "$(") {
			return
		}
		refs = append(refs, secretRef{name: name, key: key})
	}
	addEnvs := func(env []corev1.EnvVar, envFrom []corev1.EnvFromSource) {
		for _, e := range env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				ref := e.ValueFrom.SecretKeyRef
				add(ref.Name, ref.Key, ref.Optional)
			}
		}
		for _, e := range envFrom {
			if e.SecretRef != nil {
				add(e.SecretRef.Name, "", e.SecretRef.Optional)
			}
		}
	}
	addPodTemplate := func(tpl *pod.PodTemplate) {
		if tpl == nil {
			return
		}
		for _, s := range tpl.ImagePullSecrets {
			add(s.Name, "", nil)
		}
	}

	for _, w := range pr.Spec.Workspaces {
		if w.Secret != nil {
			add(w.Secret.SecretName, "", nil)
		}
	}
	addPodTemplate(pr.Spec.TaskRunTemplate.PodTemplate)
	for _, spec := range pr.Spec.TaskRunSpecs {
		addPodTemplate(spec.PodTemplate)
	}

	if pr.Spec.PipelineSpec == nil {
		return refs
	}
	for _, task := range slices.Concat(pr.Spec.PipelineSpec.Tasks, pr.Spec.PipelineSpec.Finally) {
		taskSpec := task.TaskSpec
		if taskSpec == nil {
			continue
		}
		for _, v := range taskSpec.Volumes {
			if v.Secret != nil {
				add(v.Secret.SecretName, "", nil)
			}
		}
		if st := taskSpec.StepTemplate; st != nil {
			addEnvs(st.Env, st.EnvFrom)
		}
		for _, step := range taskSpec.Steps {
			addEnvs(step.Env, step.EnvFrom)
		}
		for _, sidecar := range taskSpec.Sidecars {
			addEnvs(sidecar.Env, sidecar.EnvFrom)
		}
	}
	return refs
}
