package pac

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripNoise(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "adjacent noise lines are all removed",
			in:   "kind: PipelineRun\nmetadata:\n  creationTimestamp: null\n  name: keep\nspec: null\nstatus: {}\n",
			want: "kind: PipelineRun\nmetadata:\n  name: keep\n",
		},
		{
			name: "noise inside sequence items is removed",
			in:   "steps:\n- computeResources: {}\n  image: alpine\n",
			want: "steps:\n  - image: alpine\n",
		},
		{
			name: "literal empty sequence items are kept",
			in:   "items:\n- {}\n- name: keep\n",
			want: "items:\n  - {}\n  - name: keep\n",
		},
		{
			name: "non-empty values for noise keys are kept",
			in:   "spec:\n  param: x\nmetadata:\n  name: m\nstatus:\n  condition: ok\n",
			want: "spec:\n  param: x\nmetadata:\n  name: m\nstatus:\n  condition: ok\n",
		},
		{
			name: "end-of-document noise without trailing newline is removed",
			in:   "kind: PipelineRun\nstatus: {}",
			want: "kind: PipelineRun\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := stripNoise([]byte(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}
