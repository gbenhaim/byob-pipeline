// Validate generated objects with the actual target Tekton admission types.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/tektoncd/pipeline/pkg/apis/config"
	v1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"sigs.k8s.io/yaml"
)

func main() {
	flags, err := config.NewFeatureFlagsFromMap(map[string]string{"enable-api-fields": "beta"})
	if err != nil {
		panic(err)
	}
	defaults, err := config.NewDefaultsFromMap(nil)
	if err != nil {
		panic(err)
	}
	ctx := config.ToContext(context.Background(), &config.Config{FeatureFlags: flags, Defaults: defaults})
	failed := false
	for _, path := range os.Args[1:] {
		data, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal(data, &kind); err != nil {
			panic(err)
		}
		switch kind.Kind {
		case "Task":
			var task v1.Task
			if e := yaml.UnmarshalStrict(data, &task); e != nil {
				err = e
			} else if e := task.Validate(ctx); e != nil {
				err = e
			}
		case "Pipeline":
			var pipeline v1.Pipeline
			if e := yaml.UnmarshalStrict(data, &pipeline); e != nil {
				err = e
			} else if e := pipeline.Validate(ctx); e != nil {
				err = e
			}
		case "PipelineRun":
			var run v1.PipelineRun
			if e := yaml.UnmarshalStrict(data, &run); e != nil {
				err = e
			} else if e := run.Validate(ctx); e != nil {
				err = e
			}
		default:
			err = fmt.Errorf("unsupported kind %q", kind.Kind)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			failed = true
		} else {
			fmt.Printf("valid: %s\n", path)
		}
	}
	if failed {
		os.Exit(1)
	}
}
