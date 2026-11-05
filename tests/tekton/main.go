// Validate catalog objects and examples with the actual target Tekton admission types.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/tektoncd/pipeline/pkg/apis/config"
	v1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"sigs.k8s.io/yaml"
)

// main validates catalog resources against Tekton's configured admission types.
func main() {
	os.Exit(run(os.Args[1:]))
}

// run validates each requested file and returns a process status for the batch.
func run(paths []string) int {
	ctx := validationContext()
	failed := false
	for _, path := range paths {
		if err := validateResource(ctx, path); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			failed = true
			continue
		}

		fmt.Printf("valid: %s\n", path)
	}

	if failed {
		return 1
	}

	return 0
}

// validationContext enables the beta Tekton fields used by this catalog.
func validationContext() context.Context {
	flags, err := config.NewFeatureFlagsFromMap(map[string]string{"enable-api-fields": "beta"})
	if err != nil {
		panic(err)
	}

	defaults, err := config.NewDefaultsFromMap(nil)
	if err != nil {
		panic(err)
	}

	return config.ToContext(
		context.Background(),
		&config.Config{FeatureFlags: flags, Defaults: defaults},
	)
}

// validateResource decodes one catalog file and applies its matching Tekton admission rules.
func validateResource(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var header struct {
		Kind string `json:"kind"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return err
	}

	switch header.Kind {
	case "Task":
		return validateTask(ctx, data)
	case "Pipeline":
		return validatePipeline(ctx, data)
	case "PipelineRun":
		return validatePipelineRun(ctx, data)
	case "StepAction":
		return validateStepAction(ctx, data)
	default:
		return fmt.Errorf("unsupported kind %q", header.Kind)
	}
}

// validateStepAction strictly decodes and validates one Tekton StepAction.
func validateStepAction(ctx context.Context, data []byte) error {
	var action v1beta1.StepAction
	if err := yaml.UnmarshalStrict(data, &action); err != nil {
		return err
	}

	action.SetDefaults(ctx)
	if err := action.Validate(ctx); err != nil {
		return err
	}

	return nil
}

// validateTask strictly decodes and validates one Tekton Task.
func validateTask(ctx context.Context, data []byte) error {
	var task v1.Task
	if err := yaml.UnmarshalStrict(data, &task); err != nil {
		return err
	}

	if err := task.Validate(ctx); err != nil {
		return err
	}

	return nil
}

// validatePipeline strictly decodes and validates one Tekton Pipeline.
func validatePipeline(ctx context.Context, data []byte) error {
	var pipeline v1.Pipeline
	if err := yaml.UnmarshalStrict(data, &pipeline); err != nil {
		return err
	}

	if err := pipeline.Validate(ctx); err != nil {
		return err
	}

	return nil
}

// validatePipelineRun strictly decodes and validates one Tekton PipelineRun.
func validatePipelineRun(ctx context.Context, data []byte) error {
	var pipelineRun v1.PipelineRun
	if err := yaml.UnmarshalStrict(data, &pipelineRun); err != nil {
		return err
	}

	// The API server assigns a name before admission for generateName.
	if pipelineRun.Name == "" && pipelineRun.GenerateName != "" {
		pipelineRun.Name = pipelineRun.GenerateName + "check"
	}

	if err := pipelineRun.Validate(ctx); err != nil {
		return err
	}

	return nil
}
