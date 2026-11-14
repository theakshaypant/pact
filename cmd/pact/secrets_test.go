package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/pkg/cluster"
	"github.com/theakshaypant/pact/pkg/git"
	"github.com/theakshaypant/pact/pkg/pac"
	"github.com/theakshaypant/pact/pkg/runner"
)

func TestParseSecretFlag(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantRef string
		wantVal string
		wantErr string
	}{
		{name: "plain", in: "db.password=hunter2", wantRef: "db.password", wantVal: "hunter2"},
		{name: "value keeps equals", in: "a.b=c=d", wantRef: "a.b", wantVal: "c=d"},
		{name: "empty value", in: "db.password=", wantRef: "db.password", wantVal: ""},
		{name: "key keeps dots", in: "regcred..dockerconfigjson={}", wantRef: "regcred..dockerconfigjson", wantVal: "{}"},
		{name: "ref trimmed", in: "  db.password  = x", wantRef: "db.password", wantVal: " x"},
		{name: "missing equals", in: "db.password", wantErr: "invalid --secret: want name.key=value"},
		{name: "empty ref", in: "=value", wantErr: "invalid --secret: want name.key=value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, value, err := parseSecretFlag(tt.in)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantRef, ref)
			assert.Equal(t, tt.wantVal, value)
		})
	}
}

func TestSecretFlagsToSecrets(t *testing.T) {
	t.Run("unset yields nil secrets", func(t *testing.T) {
		sf := secretFlags{}
		secrets, err := sf.toSecrets()
		require.NoError(t, err)
		assert.Nil(t, secrets)
	})

	t.Run("file entries load", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "secrets.env")
		require.NoError(t, os.WriteFile(file, []byte("db.password=hunter2\ndb.user=app\n"), 0o600))
		sf := secretFlags{file: file}
		secrets, err := sf.toSecrets()
		require.NoError(t, err)
		assert.Equal(t, runner.Secrets{"db": {"password": "hunter2", "user": "app"}}, secrets)
	})

	t.Run("flags overlay the file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "secrets.env")
		require.NoError(t, os.WriteFile(file, []byte("db.password=old\n"), 0o600))
		sf := secretFlags{file: file, secrets: []string{"db.password=new", "token.value=t"}}
		secrets, err := sf.toSecrets()
		require.NoError(t, err)
		assert.Equal(t, runner.Secrets{"db": {"password": "new"}, "token": {"value": "t"}}, secrets)
	})

	t.Run("missing file is an error", func(t *testing.T) {
		sf := secretFlags{file: filepath.Join(t.TempDir(), "nope.env")}
		_, err := sf.toSecrets()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading --secret-file")
	})

	t.Run("invalid name is an error that never quotes the value", func(t *testing.T) {
		sf := secretFlags{secrets: []string{"Bad_Name.k=seekrit"}}
		_, err := sf.toSecrets()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid secret name")
		assert.NotContains(t, err.Error(), "seekrit")
	})
}

func TestRunForwardsSecrets(t *testing.T) {
	mgr := &fakeClusterManager{kubeconfig: []byte("kc")}
	rr := &fakeRunRunner{result: runner.Result{}}

	_, _, err := runCLIRunner(t, cliSnapshot(t.TempDir()), runEngine("pr-a"), mgr, rr,
		"run", "--secret", "db.password=hunter2", "--secret", "regcred..dockerconfigjson={}")
	require.NoError(t, err)
	assert.Equal(t, runner.Secrets{
		"db":      {"password": "hunter2"},
		"regcred": {".dockerconfigjson": "{}"},
	}, rr.gotOpts.Secrets)
}

func TestBadSecretErrorsBeforeGitWork(t *testing.T) {
	insp := &fakeInspector{snap: cliSnapshot(t.TempDir())}
	var out, errOut bytes.Buffer
	d := deps{
		openGit: func(string) (git.Inspector, error) {
			assert.Fail(t, "openGit must not run when the secrets are invalid")
			return insp, nil
		},
		newEngine: func(bool, pac.Params) pac.Engine { return fakeEng() },
		newCluster: func(cluster.Options) (clusterManager, error) {
			assert.Fail(t, "no cluster work is needed for an invalid secret")
			return &fakeClusterManager{}, nil
		},
		newRunner: func(io.Writer) runRunner { return &fakeRunRunner{} },
		out:       &out,
		errOut:    &errOut,
	}
	root := newRootCmd(d)
	root.SetArgs([]string{"run", "--secret", "noequals"})
	root.SetOut(&out)
	root.SetErr(&errOut)
	require.Error(t, root.ExecuteContext(context.Background()))
}

func TestResolveAndMatchHaveNoSecretFlags(t *testing.T) {
	for _, cmd := range []string{"resolve", "match"} {
		for _, flag := range []string{"--secret", "--secret-file"} {
			_, err := runCLIParams(t, cmd, flag, "x=1")
			require.Error(t, err, "%s must not accept %s", cmd, flag)
			assert.Contains(t, err.Error(), "unknown flag")
		}
	}
}

func TestExplicitlyEmptySecretFileIsAnError(t *testing.T) {
	_, err := runCLIParams(t, "run", "--secret-file", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--secret-file must not be empty")
}
