package pac

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
)

func TestNewParams(t *testing.T) {
	tests := []struct {
		name    string
		in      map[string]string
		want    Params
		wantErr string
	}{
		{name: "empty map yields nil params", in: map[string]string{}, want: nil},
		{name: "valid names", in: map[string]string{"environment": "prod", "a_b": "x", "c-d": "y", "E9": "z"},
			want: Params{"environment": "prod", "a_b": "x", "c-d": "y", "E9": "z"}},
		{name: "dotted name", in: map[string]string{"body.title": "x"}, wantErr: `invalid param name "body.title"`},
		{name: "cel prefix", in: map[string]string{"cel:x": "y"}, wantErr: `invalid param name "cel:x"`},
		{name: "empty name", in: map[string]string{"": "x"}, wantErr: `invalid param name ""`},
		{name: "leading digit", in: map[string]string{"9x": "y"}, wantErr: `invalid param name "9x"`},
		{name: "shadows a built-in", in: map[string]string{"revision": "x"}, wantErr: `param "revision" shadows a built-in template parameter`},
		{name: "shadows another built-in", in: map[string]string{"repo_url": "x"}, wantErr: "shadows a built-in"},
		{name: "files prefix", in: map[string]string{"files_dir": "src"}, wantErr: "reserved prefix"},
		{name: "body prefix without a dot", in: map[string]string{"bodytext": "x"}, wantErr: "reserved prefix"},
		{name: "headers prefix", in: map[string]string{"headers_x": "y"}, wantErr: "reserved prefix"},
		{name: "exact reserved word", in: map[string]string{"body": "x"}, wantErr: "would never substitute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewParams(tt.in)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestApplyParams(t *testing.T) {
	pr := &tektonv1.PipelineRun{}
	pr.Spec.Params = tektonv1.Params{
		{Name: "environment", Value: tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: "dev"}},
	}

	applyParams([]*tektonv1.PipelineRun{pr}, Params{"environment": "prod", "greeting": "hi"})

	require.Len(t, pr.Spec.Params, 2)
	// The existing entry is replaced in place, the new one appended.
	assert.Equal(t, "environment", pr.Spec.Params[0].Name)
	assert.Equal(t, "prod", pr.Spec.Params[0].Value.StringVal)
	assert.Equal(t, "greeting", pr.Spec.Params[1].Name)
	assert.Equal(t, "hi", pr.Spec.Params[1].Value.StringVal)
	assert.Equal(t, tektonv1.ParamTypeString, pr.Spec.Params[1].Value.Type)
}

func TestApplyParamsMultipleRuns(t *testing.T) {
	prs := []*tektonv1.PipelineRun{{}, {}}
	applyParams(prs, Params{"a": "1"})
	for _, pr := range prs {
		require.Len(t, pr.Spec.Params, 1)
		assert.Equal(t, "1", pr.Spec.Params[0].Value.StringVal)
	}
}

func TestApplyParamsEmptyIsNoOp(t *testing.T) {
	pr := &tektonv1.PipelineRun{}
	applyParams([]*tektonv1.PipelineRun{pr}, nil)
	assert.Empty(t, pr.Spec.Params)
}

func TestUsedParamNames(t *testing.T) {
	pr := &tektonv1.PipelineRun{}
	pr.Spec.Params = tektonv1.Params{{Name: "already-set"}}
	pr.Spec.PipelineSpec = &tektonv1.PipelineSpec{}
	pr.Spec.PipelineSpec.Params = []tektonv1.ParamSpec{{Name: "declared"}}

	raw := `
# a comment mentioning {{ in_comment }} counts too
value: "{{  spaced  }}"
cel: "{{ body.title }}"
expr: "{{ cel:x == 1 }}"
plain: "{{ revision }}"
`
	used := usedParamNames([]*tektonv1.PipelineRun{pr}, []string{raw})

	for _, name := range []string{"already-set", "declared", "spaced", "in_comment", "revision"} {
		assert.True(t, used[name], "%s must count as used", name)
	}
	for _, name := range []string{"body.title", "cel:x == 1"} {
		assert.False(t, used[name], "%s is not a custom param name", name)
	}
}

func TestParamsUnknown(t *testing.T) {
	p := Params{"alpha": "1", "beta": "2", "gamma": "3"}
	unknown := p.unknown(map[string]bool{"beta": true})
	assert.Equal(t, []string{"alpha", "gamma"}, unknown, "unknown names come back sorted")
}

func TestParamsUnknownErrIs(t *testing.T) {
	// ErrUnknownParams must stay wrappable so callers can errors.Is it.
	wrapped := errors.Join(ErrUnknownParams)
	assert.ErrorIs(t, wrapped, ErrUnknownParams)
}
