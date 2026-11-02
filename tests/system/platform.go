package system

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// provisionPlatformSecret makes the build TaskRun's SSH secret available.
// A ready multi-platform controller creates that secret itself. Otherwise the
// test writes the localhost secret the controller would create for a local
// platform, which makes run-build-remote execute the build in the cluster.
func provisionPlatformSecret(ctx context.Context, pipelineRun string) error {
	installed, ready, err := platformController()
	if err != nil {
		return err
	}

	if installed {
		if !ready {
			return fmt.Errorf("multi-platform-controller is installed but not ready")
		}

		return nil
	}

	var lastErr error
	for {
		taskRun, err := taskRunName(pipelineRun, "build")
		if err != nil {
			lastErr = err
		} else if taskRun != "" {
			return ensureLocalhostSecret(taskRun)
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("build TaskRun for %s did not appear: %w", pipelineRun, lastErr)
			}

			return fmt.Errorf("build TaskRun for %s did not appear: %w", pipelineRun, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// platformController reports whether a multi-platform controller deployment
// exists and has a ready replica.
func platformController() (bool, bool, error) {
	stdout, stderr, err := kubectl("get", "deploy", "-A", "-o", "json")
	if err != nil {
		return false, false, fmt.Errorf("list deployments: %w\n%s%s", err, stdout, stderr)
	}

	var body struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				ReadyReplicas int `json:"readyReplicas"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &body); err != nil {
		return false, false, fmt.Errorf("decode deployments: %w", err)
	}

	installed := false
	ready := false
	for _, item := range body.Items {
		if item.Metadata.Name != "multi-platform-controller" {
			continue
		}

		installed = true
		if item.Status.ReadyReplicas > 0 {
			ready = true
		}
	}

	return installed, ready, nil
}

// taskRunName returns the TaskRun created for one PipelineTask.
// An empty name means Tekton has not created it yet.
func taskRunName(pipelineRun, task string) (string, error) {
	stdout, stderr, err := kubectl(
		"get", "taskruns", "-n", namespace,
		"-l", "tekton.dev/pipelineRun="+pipelineRun+",tekton.dev/pipelineTask="+task,
		"-o", "json",
	)
	if err != nil {
		return "", fmt.Errorf("list TaskRuns: %w\n%s%s", err, stdout, stderr)
	}

	var body struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &body); err != nil {
		return "", fmt.Errorf("decode TaskRuns: %w", err)
	}

	if len(body.Items) == 0 {
		return "", nil
	}

	return body.Items[0].Metadata.Name, nil
}

// ensureLocalhostSecret creates the controller SSH secret for a local build.
// An existing secret is left unchanged so a controller-written host wins.
func ensureLocalhostSecret(taskRun string) error {
	secretName := "multi-platform-ssh-" + taskRun
	stdout, stderr, err := kubectl("get", "secret", "-n", namespace, secretName)
	if err == nil {
		return nil
	}

	if !strings.Contains(stderr, "NotFound") && !strings.Contains(stdout, "NotFound") {
		return fmt.Errorf("read secret %s: %w\n%s%s", secretName, err, stdout, stderr)
	}

	manifest := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
  labels:
    build.appstudio.redhat.com/multi-platform-secret: "true"
type: Opaque
stringData:
  host: localhost
`, secretName, namespace)
	stdout, stderr, err = kubectlStdin(manifest, "create", "-f", "-")
	if err != nil && !strings.Contains(stderr, "AlreadyExists") {
		return fmt.Errorf("create secret %s: %w\n%s%s", secretName, err, stdout, stderr)
	}

	return nil
}
