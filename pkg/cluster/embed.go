package cluster

import _ "embed"

// embeddedTektonRelease is the official Tekton Pipeline release manifest
// pact installs by default. Embedding it keeps `pact up` fully offline.
//
//go:embed manifests/tekton-release.yaml
var embeddedTektonRelease []byte

// embeddedTektonVersion is the Tekton Pipeline version of the embedded
// release manifest. It matches the github.com/tektoncd/pipeline Go
// dependency, so PipelineRuns resolved and validated client-side use the
// same API version the cluster serves.
const embeddedTektonVersion = "v1.18.0"
