package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theakshaypant/pact/pkg/git"
	"github.com/theakshaypant/pact/pkg/pac"
)

func TestParseParamFlag(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantName  string
		wantValue string
		wantErr   string
	}{
		{name: "plain", in: "environment=prod", wantName: "environment", wantValue: "prod"},
		{name: "value keeps equals", in: "a=b=c", wantName: "a", wantValue: "b=c"},
		{name: "empty value", in: "a=", wantName: "a", wantValue: ""},
		{name: "value with comma", in: "a=x,y", wantName: "a", wantValue: "x,y"},
		{name: "name trimmed", in: "  a  =b", wantName: "a", wantValue: "b"},
		{name: "missing equals", in: "environment", wantErr: `invalid --param "environment": want name=value`},
		{name: "empty name", in: "=value", wantErr: `invalid --param "=value": empty name`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, value, err := parseParamFlag(tt.in)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantValue, value)
		})
	}
}

func TestParseDotenv(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    map[string]string
		wantErr string
	}{
		{name: "plain", in: "environment=prod\n", want: map[string]string{"environment": "prod"}},
		{name: "comments and blanks", in: "# comment\n\nenvironment=prod\n   \n", want: map[string]string{"environment": "prod"}},
		{name: "crlf", in: "environment=prod\r\ngreeting=hi\r\n", want: map[string]string{"environment": "prod", "greeting": "hi"}},
		{name: "bom", in: "\xef\xbb\xbfenvironment=prod\n", want: map[string]string{"environment": "prod"}},
		{name: "double quotes stripped", in: `a="b c"`, want: map[string]string{"a": "b c"}},
		{name: "single quotes stripped", in: "a='b c'", want: map[string]string{"a": "b c"}},
		{name: "mismatched quotes kept", in: `a="b`, want: map[string]string{"a": `"b`}},
		{name: "value keeps equals", in: "a=b=c", want: map[string]string{"a": "b=c"}},
		{name: "spaces trimmed", in: "  a  =  b  ", want: map[string]string{"a": "b"}},
		{name: "later line wins", in: "a=1\na=2\n", want: map[string]string{"a": "2"}},
		{name: "empty file", in: "", want: map[string]string{}},
		{name: "missing equals", in: "environment\n", wantErr: "params.env:1: want name=value"},
		{name: "empty name", in: "=value\n", wantErr: "params.env:1: empty name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDotenv(strings.NewReader(tt.in), "params.env")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParamFlagsToParams(t *testing.T) {
	t.Run("unset yields nil params", func(t *testing.T) {
		pf := paramFlags{}
		params, err := pf.toParams()
		require.NoError(t, err)
		assert.Nil(t, params)
	})

	t.Run("flags overlay the file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "params.env")
		require.NoError(t, os.WriteFile(file, []byte("environment=staging\ngreeting=hello\n"), 0o644))
		pf := paramFlags{file: file, params: []string{"environment=prod"}}
		params, err := pf.toParams()
		require.NoError(t, err)
		assert.Equal(t, pac.Params{"environment": "prod", "greeting": "hello"}, params)
	})

	t.Run("missing file is an error", func(t *testing.T) {
		pf := paramFlags{file: filepath.Join(t.TempDir(), "nope.env")}
		_, err := pf.toParams()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading --param-file")
	})

	t.Run("invalid name is an error", func(t *testing.T) {
		pf := paramFlags{params: []string{"body.title=x"}}
		_, err := pf.toParams()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid param name")
	})

	t.Run("shadowing a built-in is an error", func(t *testing.T) {
		pf := paramFlags{params: []string{"revision=x"}}
		_, err := pf.toParams()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "shadows a built-in")
	})
}

// runCLIParams executes args with a deps recording the params handed to
// newEngine.
func runCLIParams(t *testing.T, args ...string) (pac.Params, error) {
	t.Helper()
	var gotParams pac.Params
	insp := &fakeInspector{snap: cliSnapshot(t.TempDir())}
	var out, errOut strings.Builder
	d := deps{
		openGit: func(string) (git.Inspector, error) { return insp, nil },
		newEngine: func(_ bool, params pac.Params) pac.Engine {
			gotParams = params
			return fakeEng()
		},
		out:    &out,
		errOut: &errOut,
	}
	root := newRootCmd(d)
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.ExecuteContext(context.Background())
	return gotParams, err
}

func TestResolveForwardsParams(t *testing.T) {
	params, err := runCLIParams(t, "resolve", "--param", "environment=prod", "--param", "greeting=hi")
	require.NoError(t, err)
	assert.Equal(t, pac.Params{"environment": "prod", "greeting": "hi"}, params)
}

func TestRunForwardsParams(t *testing.T) {
	params, err := runCLIParams(t, "run", "--param", "environment=prod")
	require.NoError(t, err)
	assert.Equal(t, pac.Params{"environment": "prod"}, params)
}

func TestMatchHasNoParamFlags(t *testing.T) {
	for _, flag := range []string{"--param", "--param-file"} {
		_, err := runCLIParams(t, "match", flag, "x=1")
		require.Error(t, err, "match must not accept %s", flag)
		assert.Contains(t, err.Error(), "unknown flag")
	}
}

func TestBadParamErrorsBeforeGitWork(t *testing.T) {
	insp := &fakeInspector{snap: cliSnapshot(t.TempDir())}
	var out, errOut strings.Builder
	d := deps{
		openGit: func(string) (git.Inspector, error) {
			assert.Fail(t, "openGit must not run when the params are invalid")
			return insp, nil
		},
		newEngine: func(bool, pac.Params) pac.Engine { return fakeEng() },
		out:       &out,
		errOut:    &errOut,
	}
	root := newRootCmd(d)
	root.SetArgs([]string{"resolve", "--param", "noequals"})
	root.SetOut(&out)
	root.SetErr(&errOut)
	require.Error(t, root.ExecuteContext(context.Background()))
}

func TestExplicitlyEmptyParamFileIsAnError(t *testing.T) {
	_, err := runCLIParams(t, "resolve", "--param-file", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--param-file must not be empty")
}
