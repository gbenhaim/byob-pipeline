package main

import "testing"

// TestValidResourcesReturnNoValidationError checks successful validation for
// each Tekton resource kind handled by the validator.
func TestValidResourcesReturnNoValidationError(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{
			name: "task",
			path: "../../task/run-build/0.1/run-build.yaml",
		},
		{
			name: "pipeline",
			path: "../../pipelines/byob.yaml",
		},
		{
			name: "pipeline run",
			path: "../../examples/pipelineruns/byob.yaml",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateResource(validationContext(), test.path)
			if err != nil {
				t.Fatalf("valid resource returned validation error: %#v", err)
			}
		})
	}
}
