package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/pod"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestNewSecrets(t *testing.T) {
	t.Run("groups flat refs by name", func(t *testing.T) {
		secrets, err := NewSecrets(map[string]string{
			"db.password":               "hunter2",
			"db.user":                   "app",
			"regcred..dockerconfigjson": "{}",
		})
		require.NoError(t, err)
		assert.Equal(t, Secrets{
			"db":      {"password": "hunter2", "user": "app"},
			"regcred": {".dockerconfigjson": "{}"},
		}, secrets)
	})

	t.Run("empty map yields nil secrets", func(t *testing.T) {
		secrets, err := NewSecrets(map[string]string{})
		require.NoError(t, err)
		assert.Nil(t, secrets)
	})

	t.Run("errors are deterministic and sorted", func(t *testing.T) {
		_, err := NewSecrets(map[string]string{"zz": "", "aa": ""})
		require.Error(t, err)
		assert.EqualError(t, err, `invalid secret "aa": want name.key`)
	})

	tests := []struct {
		ref     string
		wantErr string
	}{
		{ref: "db", wantErr: `invalid secret "db": want name.key`},
		{ref: "Bad_Name.k", wantErr: `invalid secret name "Bad_Name"`},
		{ref: ".k", wantErr: `invalid secret name ""`},
		{ref: "db.a b", wantErr: `invalid secret key "a b" in "db"`},
		{ref: "db.", wantErr: `invalid secret key "" in "db"`},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			_, err := NewSecrets(map[string]string{tt.ref: "v"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNewSecret(t *testing.T) {
	t.Run("only .dockerconfigjson is typed for image pulls", func(t *testing.T) {
		s := newSecret("regcred", map[string]string{corev1.DockerConfigJsonKey: "{}"})
		assert.Equal(t, corev1.SecretTypeDockerConfigJson, s.Type)
		assert.Equal(t, "regcred", s.Name)
	})
	t.Run("anything else stays opaque", func(t *testing.T) {
		s := newSecret("db", map[string]string{"password": "x"})
		assert.Empty(t, s.Type)
	})
	t.Run(".dockerconfigjson beside another key stays opaque", func(t *testing.T) {
		s := newSecret("mixed", map[string]string{corev1.DockerConfigJsonKey: "{}", "note": "x"})
		assert.Empty(t, s.Type)
	})
}

// secretPipeline builds a resolved PipelineRun exercising every secret
// consumption shape the scan covers: an imagePullSecret, a secret
// workspace, a step-template envFrom, a secretKeyRef, an envFrom, a
// secret volume, a finally-task secretKeyRef — plus an optional ref and
// a $(params.x)-templated name that must both be skipped.
func secretPipeline(name string) *tektonv1.PipelineRun {
	optional := true
	pr := wsPipeline(name, "source")
	pr.Spec.TaskRunTemplate.PodTemplate = &pod.PodTemplate{
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "pull"}},
	}
	pr.Spec.Workspaces = append(pr.Spec.Workspaces, tektonv1.WorkspaceBinding{
		Name:   "credentials",
		Secret: &corev1.SecretVolumeSource{SecretName: "ws-secret"},
	})
	task := tektonv1.PipelineTask{Name: "use", TaskSpec: &tektonv1.EmbeddedTask{}}
	ts := &task.TaskSpec.TaskSpec
	ts.StepTemplate = &tektonv1.StepTemplate{
		EnvFrom: []corev1.EnvFromSource{{
			SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "template-secret"}},
		}},
	}
	ts.Steps = []tektonv1.Step{{
		Name: "step",
		Env: []corev1.EnvVar{
			{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "password",
				},
			}},
			{Name: "OPT", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "skipped-optional"},
					Key:                  "k", Optional: &optional,
				},
			}},
		},
		EnvFrom: []corev1.EnvFromSource{{
			SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "whole"}},
		}},
	}}
	ts.Sidecars = []tektonv1.Sidecar{{
		Name: "side",
		Env: []corev1.EnvVar{{Name: "S", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "$(params.secret-name)"}, Key: "k",
			},
		}}},
	}}
	ts.Volumes = []corev1.Volume{{
		Name: "v",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: "vol-secret"},
		},
	}}
	pr.Spec.PipelineSpec.Tasks = append(pr.Spec.PipelineSpec.Tasks, task)

	// Finally tasks commonly hold notify steps that use secrets.
	pr.Spec.PipelineSpec.Finally = []tektonv1.PipelineTask{{
		Name: "notify", TaskSpec: &tektonv1.EmbeddedTask{},
	}}
	pr.Spec.PipelineSpec.Finally[0].TaskSpec.Steps = []tektonv1.Step{{
		Name: "notify",
		Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "notify"}, Key: "token",
			},
		}}},
	}}
	return pr
}

// wantMissingAll is every secret reference secretPipeline makes.
var wantMissingAll = []string{
	"db.password", "notify.token", "pull", "template-secret", "vol-secret", "whole", "ws-secret",
}

// suppliedAll supplies every secret secretPipeline references.
func suppliedAll() Secrets {
	return Secrets{"db": {"password": "x"}, "notify": {"token": "x"}, "pull": {},
		"template-secret": {}, "vol-secret": {}, "whole": {}, "ws-secret": {}}
}

func TestSecretsMissing(t *testing.T) {
	prs := []*tektonv1.PipelineRun{secretPipeline("pr-a")}

	t.Run("nothing supplied reports every reference", func(t *testing.T) {
		missing := Secrets{}.missing(prs)
		assert.Equal(t, wantMissingAll, missing)
	})

	t.Run("a keyed ref needs its exact key", func(t *testing.T) {
		missing := Secrets{"db": {"user": "app"}}.missing(prs)
		assert.Contains(t, missing, "db.password")
	})

	t.Run("supplied refs and skipped shapes drop out", func(t *testing.T) {
		assert.Empty(t, suppliedAll().missing(prs))
	})

	t.Run("refs are de-duplicated across runs", func(t *testing.T) {
		missing := Secrets{}.missing([]*tektonv1.PipelineRun{secretPipeline("pr-a"), secretPipeline("pr-b")})
		assert.Equal(t, wantMissingAll, missing)
	})
}

func TestRunnerRunCreatesSecrets(t *testing.T) {
	client := &fakeClient{followResults: map[string]waitResult{"pr-a": {success: true}}}
	r, progress := newTestRunner(t, client)
	secrets, err := NewSecrets(map[string]string{
		"db.password": "hunter2", "regcred..dockerconfigjson": "{}", "token.value": "t",
	})
	require.NoError(t, err)

	_, err = r.Run(context.Background(), []byte("kc"), []*tektonv1.PipelineRun{runPipeline("pr-a")},
		Options{RepoRoot: newSourceRepo(t), Secrets: secrets})
	require.NoError(t, err)

	// Secrets are created in the run namespace, sorted by name, before
	// anything is submitted.
	ns := client.ensuredNamespaces[0]
	assert.Equal(t, []createdSecret{
		{ns: ns, name: "db", data: map[string]string{"password": "hunter2"}},
		{ns: ns, name: "regcred", data: map[string]string{".dockerconfigjson": "{}"}},
		{ns: ns, name: "token", data: map[string]string{"value": "t"}},
	}, client.createdSecrets)
	require.Len(t, client.submitted, 1)

	// Progress names the secrets but never their values.
	out := progress.String()
	assert.Contains(t, out, "created secret db (1 keys)")
	assert.NotContains(t, out, "hunter2")
	assert.NotContains(t, out, "warning: pipelines reference secret")
}

func TestRunnerRunWarnsOnUnsuppliedSecrets(t *testing.T) {
	t.Run("warns before any cluster work", func(t *testing.T) {
		client := &fakeClient{followResults: map[string]waitResult{"pr-a": {success: true}}}
		r, progress := newTestRunner(t, client)

		_, err := r.Run(context.Background(), []byte("kc"), []*tektonv1.PipelineRun{secretPipeline("pr-a")},
			Options{RepoRoot: newSourceRepo(t)})
		require.NoError(t, err)

		out := progress.String()
		assert.Contains(t, out,
			"warning: pipelines reference secret(s) not supplied with --secret/--secret-file: db.password, notify.token, pull")
		assert.Contains(t, out, "CreateContainerConfigError")
		// The warning precedes the namespace setup.
		assert.Less(t, strings.Index(out, "warning:"), strings.Index(out, "packaging source"))
	})

	t.Run("stays silent when everything is supplied", func(t *testing.T) {
		client := &fakeClient{followResults: map[string]waitResult{"pr-a": {success: true}}}
		r, progress := newTestRunner(t, client)

		_, err := r.Run(context.Background(), []byte("kc"), []*tektonv1.PipelineRun{secretPipeline("pr-a")},
			Options{RepoRoot: newSourceRepo(t), Secrets: suppliedAll()})
		require.NoError(t, err)
		assert.NotContains(t, progress.String(), "warning:")
	})
}

func TestRunnerRunSecretFailureCleansNamespace(t *testing.T) {
	client := &fakeClient{secretErr: errors.New("forbidden")}
	r, _ := newTestRunner(t, client)

	result, err := r.Run(context.Background(), []byte("kc"), []*tektonv1.PipelineRun{runPipeline("pr-a")},
		Options{RepoRoot: newSourceRepo(t), Secrets: Secrets{"db": {"password": "x"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating secret")
	assert.Empty(t, client.submitted, "nothing must be submitted when a secret fails")
	assert.True(t, result.Cleaned, "a pre-submission failure must delete the namespace")
	require.Len(t, client.deletedNamespaces, 1)
}
