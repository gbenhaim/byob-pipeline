package policy

import rego.v1

# Conforma queries every rule by reference and only scores package policy.
# The input is an AttestationList of in-toto statements. Chains stores one
# statement for each image. Helpers below are sets, not functions.

# approved_checkout is the immutable git-clone task the catalog publishes.
approved_checkout := "quay.io/konflux-ci/tekton-catalog/task-git-clone-oci-ta:0.2.4@sha256:df3c42d78223f07b40a84dd29e5c8860d14777ffdf150ea08c738770f51216dc"

# approved_image is the immutable developer image the catalog publishes.
approved_image := "quay.io/konflux-ci/task-runner:2.1.0@sha256:c34c933c269e2401bb042fe69e2999cf288331b6586d4f4eca9c845270d9b1f9"

# approved_digest is the digest portion of approved_image.
approved_digest := "sha256:c34c933c269e2401bb042fe69e2999cf288331b6586d4f4eca9c845270d9b1f9"

# required names the pipelines this policy evaluates.
required := {"byob", "byob-chained", "byob-remote", "byob-matrix"}

# command_pattern matches a relative path inside the reviewed source tree.
command_pattern := `^\./[A-Za-z0-9_./-]+$`

# statements is every Chains SLSA provenance statement in the input.
statements contains statement if {
	some statement in object.get(input, "items", [])
	statement.predicateType == "https://slsa.dev/provenance/v0.2"
	statement.predicate.buildType == "tekton.dev/v1/PipelineRun"
}

# tasks is every task recorded in a statement, with the pipeline parameters.
tasks contains item if {
	some statement in statements
	some index, task in object.get(statement.predicate.buildConfig, "tasks", [])
	item := {
		"index": index,
		"run": statement.subject[0].digest.sha256,
		"pipeline": object.get(statement.predicate.invocation.environment.labels, "tekton.dev/pipeline", ""),
		"parameters": object.get(statement.predicate.invocation, "parameters", {}),
		"materials": object.get(statement.predicate, "materials", []),
		"task": task,
	}
}

# build_tasks is every run-build or run-build-remote task.
build_tasks contains item if {
	some item in tasks
	item.task.ref.name in {"run-build", "run-build-remote"}
	not item.task.ref.resolver
}

# runs is every attested image digest.
runs contains run if {
	some item in tasks
	run := item.run
}

# pipeline_of maps an image digest to its pipeline name.
pipeline_of contains pair if {
	some item in tasks
	pair := {"run": item.run, "pipeline": item.pipeline}
}

# source_results is the checkout SOURCE_ARTIFACT for one image.
source_results contains found if {
	some item in tasks
	item.task.name == "checkout"
	some result in object.get(item.task, "results", [])
	result.name == "SOURCE_ARTIFACT"
	found := {"run": item.run, "value": result.value}
}

# build_results is a BUILD_ARTIFACT produced by a build task.
build_results contains found if {
	some item in build_tasks
	some result in object.get(item.task, "results", [])
	result.name == "BUILD_ARTIFACT"
	found := {"run": item.run, "value": result.value}
}

# checkout_ok names an image whose checkout is the pinned git-clone bundle.
checkout_ok contains run if {
	some item in tasks
	item.task.name == "checkout"
	run := item.run
	item.task.ref.resolver == "bundles"
	some bundle in object.get(item.task.ref, "params", [])
	bundle.name == "bundle"
	bundle.value == approved_checkout
	some ref_name in object.get(item.task.ref, "params", [])
	ref_name.name == "name"
	ref_name.value == "git-clone-oci-ta"
	some kind in object.get(item.task.ref, "params", [])
	kind.name == "kind"
	kind.value == "task"
}

# source_ok names an image whose checkout used the pipeline Git URL and revision.
source_ok contains run if {
	some item in tasks
	item.task.name == "checkout"
	run := item.run
	url := object.get(item.parameters, "git-url", "")
	revision := object.get(item.parameters, "revision", "")
	url != ""
	revision != ""
	object.get(item.task.invocation.parameters, "url", "") == url
	object.get(item.task.invocation.parameters, "revision", "") == revision
	some material in item.materials
	startswith(object.get(material, "uri", ""), "git+")
	contains(object.get(material, "uri", ""), url)
	object.get(object.get(material, "digest", {}), "sha1", "") == revision
}

# git_results_ok names an image whose checkout results repeat that Git identity.
git_results_ok contains run if {
	some item in tasks
	item.task.name == "checkout"
	run := item.run
	some url in object.get(item.task, "results", [])
	url.name == "CHAINS-GIT_URL"
	url.value == object.get(item.parameters, "git-url", "")
	some commit in object.get(item.task, "results", [])
	commit.name == "CHAINS-GIT_COMMIT"
	commit.value == object.get(item.parameters, "revision", "")
}

# image_result_ok names an image whose pack digest is the attestation subject.
image_result_ok contains run if {
	some item in tasks
	item.task.ref.name == "pack"
	run := item.run
	some digest in object.get(item.task, "results", [])
	digest.name == "IMAGE_DIGEST"
	digest.value == sprintf("sha256:%s", [run])
}

# shape_ok names an image that ran checkout, pack, and a build task.
shape_ok contains run if {
	some checkout in tasks
	checkout.task.name == "checkout"
	run := checkout.run
	some pack in tasks
	pack.run == run
	pack.task.ref.name == "pack"
	not pack.task.ref.resolver
	some build in build_tasks
	build.run == run
}

# allowed is a non-checkout task on the closed task allowlist.
allowed contains id if {
	some item in tasks
	item.task.name != "checkout"
	not item.task.ref.resolver
	item.task.ref.name in {"run-build", "run-build-remote", "pack"}
	id := sprintf("%s/%d", [item.run, item.index])
}

# image_ok names a build task that used the pinned task-runner image.
image_ok contains id if {
	some item in build_tasks
	id := sprintf("%s/%d", [item.run, item.index])
	object.get(item.task.invocation.parameters, "image", "") == approved_image
}

# source_input_ok names a build task whose SOURCE_ARTIFACT is the checkout
# source or a BUILD_ARTIFACT from a build task in the same attestation.
source_input_ok contains id if {
	some item in build_tasks
	id := sprintf("%s/%d", [item.run, item.index])
	value := object.get(item.task.invocation.parameters, "SOURCE_ARTIFACT", "")
	some found in source_results
	found.run == item.run
	found.value == value
}

source_input_ok contains id if {
	some item in build_tasks
	id := sprintf("%s/%d", [item.run, item.index])
	value := object.get(item.task.invocation.parameters, "SOURCE_ARTIFACT", "")
	some found in build_results
	found.run == item.run
	found.value == value
}

# build_input_ok names a build task whose extra inputs are build outputs.
build_input_ok contains id if {
	some item in build_tasks
	id := sprintf("%s/%d", [item.run, item.index])
	values := object.get(item.task.invocation.parameters, "BUILD_ARTIFACTS", [])
	is_array(values)
	every entry in values {
		some found in build_results
		found.run == item.run
		found.value == entry
	}
}

# pack_ok names an image whose pack published those build outputs.
pack_ok contains run if {
	some item in tasks
	item.task.ref.name == "pack"
	run := item.run
	object.get(item.task.invocation.parameters, "IMAGE", "") == object.get(item.parameters, "output-image", "")
	object.get(item.task.invocation.parameters, "artifactType", "") == "application/vnd.konflux.build-output.v1"
	values := object.get(item.task.invocation.parameters, "BUILD_ARTIFACTS", [])
	is_array(values)
	count(values) > 0
	every entry in values {
		some found in build_results
		found.run == run
		found.value == entry
	}
}

# storage_ok names a checkout or build task that used the pipeline storage.
storage_ok contains id if {
	some item in tasks
	item.task.name == "checkout"
	id := sprintf("%s/%d", [item.run, item.index])
	value := object.get(item.task.invocation.parameters, "ociStorage", "")
	prefix := object.get(item.parameters, "oci-storage", "")
	prefix != ""
	value == prefix
}

storage_ok contains id if {
	some item in tasks
	item.task.name == "checkout"
	id := sprintf("%s/%d", [item.run, item.index])
	value := object.get(item.task.invocation.parameters, "ociStorage", "")
	prefix := object.get(item.parameters, "oci-storage", "")
	prefix != ""
	startswith(value, sprintf("%s:", [prefix]))
}

storage_ok contains id if {
	some item in tasks
	item.task.name == "checkout"
	id := sprintf("%s/%d", [item.run, item.index])
	value := object.get(item.task.invocation.parameters, "ociStorage", "")
	prefix := object.get(item.parameters, "oci-storage", "")
	prefix != ""
	startswith(value, sprintf("%s/", [prefix]))
}

storage_ok contains id if {
	some item in build_tasks
	id := sprintf("%s/%d", [item.run, item.index])
	value := object.get(item.task.invocation.parameters, "ociStorage", "")
	prefix := object.get(item.parameters, "oci-storage", "")
	prefix != ""
	value == prefix
}

storage_ok contains id if {
	some item in build_tasks
	id := sprintf("%s/%d", [item.run, item.index])
	value := object.get(item.task.invocation.parameters, "ociStorage", "")
	prefix := object.get(item.parameters, "oci-storage", "")
	prefix != ""
	startswith(value, sprintf("%s:", [prefix]))
}

storage_ok contains id if {
	some item in build_tasks
	id := sprintf("%s/%d", [item.run, item.index])
	value := object.get(item.task.invocation.parameters, "ociStorage", "")
	prefix := object.get(item.parameters, "oci-storage", "")
	prefix != ""
	startswith(value, sprintf("%s/", [prefix]))
}

# command_ok names a build task whose command stayed in the source tree
# and was the developer step that actually ran.
command_ok contains id if {
	some item in build_tasks
	id := sprintf("%s/%d", [item.run, item.index])
	commands := object.get(item.task.invocation.parameters, "build-cmd", [])
	is_array(commands)
	count(commands) > 0
	every entry in commands {
		is_string(entry)
		regex.match(command_pattern, entry)
		not contains(entry, "..")
	}
	some step in object.get(item.task, "steps", [])
	contains(object.get(object.get(step, "environment", {}), "image", ""), approved_digest)
	step.entryPoint in commands
}

# METADATA
# title: The input contains Chains provenance
# description: Each snapshot image must have a Tekton PipelineRun SLSA statement.
# custom:
#   short_name: attestation
#   failure_msg: Chains provenance is missing
#   solution: Wait for Chains to sign the image and publish its attestation
#   collections:
#   - byob
deny contains result if {
	count(statements) == 0
	result := {
		"code": "policy.attestation",
		"msg": "the snapshot image has no Chains PipelineRun provenance",
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: The attested pipeline is a catalog pipeline
# description: The build must be byob, byob-chained, byob-remote, or byob-matrix.
# custom:
#   short_name: pipeline_name
#   failure_msg: The attested pipeline is not a catalog pipeline
#   solution: Run one of the published catalog pipelines
#   collections:
#   - byob
deny contains result if {
	some pair in pipeline_of
	not required[pair.pipeline]
	result := {
		"code": "policy.pipeline_name",
		"msg": sprintf("pipeline %q is not one of byob, byob-chained, byob-remote, or byob-matrix", [pair.pipeline]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Checkout, build, and pack ran
# description: The attestation must include checkout, pack, and a build task.
# custom:
#   short_name: required_tasks
#   failure_msg: The attestation is missing checkout, pack, or a build task
#   solution: Run checkout, run-build or run-build-remote, and pack
#   collections:
#   - byob
deny contains result if {
	some pair in pipeline_of
	not shape_ok[pair.run]
	result := {
		"code": "policy.required_tasks",
		"msg": sprintf("%s must run checkout, pack, and run-build or run-build-remote", [pair.pipeline]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Checkout is the pinned git-clone bundle
# description: Checkout must be the published git-clone-oci-ta bundle digest.
# custom:
#   short_name: checkout_identity
#   failure_msg: Checkout is not the pinned git-clone bundle
#   solution: Restore the catalog checkout bundle reference
#   collections:
#   - byob
deny contains result if {
	some pair in pipeline_of
	not checkout_ok[pair.run]
	result := {
		"code": "policy.checkout_identity",
		"msg": sprintf("%s checkout is not the pinned git-clone-oci-ta bundle", [pair.pipeline]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Tasks stay on the closed allowlist
# description: Only checkout, run-build, run-build-remote, and pack may run.
# custom:
#   short_name: allowed_tasks
#   failure_msg: A task is outside the allowlist
#   solution: Remove the task or replace it with an allowed task reference
#   collections:
#   - byob
deny contains result if {
	some item in tasks
	item.task.name != "checkout"
	not allowed[sprintf("%s/%d", [item.run, item.index])]
	result := {
		"code": "policy.allowed_tasks",
		"msg": sprintf("%s task %s is outside the checkout, run-build, run-build-remote, and pack allowlist", [item.pipeline, item.task.name]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Checkout used the attested Git revision
# description: The checkout URL and revision must match the pipeline parameters and the Git material.
# custom:
#   short_name: source_identity
#   failure_msg: Checkout did not use the attested Git URL and revision
#   solution: Check out the git-url and revision recorded for the build
#   collections:
#   - byob
deny contains result if {
	some pair in pipeline_of
	not source_ok[pair.run]
	result := {
		"code": "policy.source_identity",
		"msg": sprintf("%s checkout must use the attested git-url and revision", [pair.pipeline]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Provenance results bind the image to the source
# description: Checkout must record the Git URL and commit, and pack must record the subject digest.
# custom:
#   short_name: provenance_results
#   failure_msg: Provenance results do not match the source or the image
#   solution: Restore the checkout Git results and the pack image digest
#   collections:
#   - byob
deny contains result if {
	some pair in pipeline_of
	not git_results_ok[pair.run]
	result := {
		"code": "policy.provenance_results",
		"msg": sprintf("%s checkout results must record the attested Git URL and commit", [pair.pipeline]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Pack records the attested image digest
# description: The pack IMAGE_DIGEST result must be the attestation subject.
# custom:
#   short_name: image_digest
#   failure_msg: The pack digest is not the attested image
#   solution: Publish the image whose digest Chains signed
#   collections:
#   - byob
deny contains result if {
	some pair in pipeline_of
	not image_result_ok[pair.run]
	result := {
		"code": "policy.image_digest",
		"msg": sprintf("%s pack must record the attested image digest", [pair.pipeline]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Build tasks use the pinned task-runner image
# description: The developer image must be the published task-runner digest.
# custom:
#   short_name: builder_image
#   failure_msg: A build task did not use the pinned task-runner image
#   solution: Restore the catalog task-runner image digest
#   collections:
#   - byob
deny contains result if {
	some item in build_tasks
	not image_ok[sprintf("%s/%d", [item.run, item.index])]
	result := {
		"code": "policy.builder_image",
		"msg": sprintf("%s task %s must run the pinned task-runner image", [item.pipeline, item.task.name]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Build inputs are the source or a build output
# description: SOURCE_ARTIFACT must be the checkout source or a BUILD_ARTIFACT from a build task in this attestation.
# custom:
#   short_name: artifact_source
#   failure_msg: SOURCE_ARTIFACT is not the checkout source or a build output
#   solution: Pass the checkout SOURCE_ARTIFACT or a previous build BUILD_ARTIFACT
#   collections:
#   - byob
deny contains result if {
	some item in build_tasks
	not source_input_ok[sprintf("%s/%d", [item.run, item.index])]
	result := {
		"code": "policy.artifact_source",
		"msg": sprintf("%s task %s SOURCE_ARTIFACT must be the checkout source or a build output from this attestation", [item.pipeline, item.task.name]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Extra build inputs are build outputs
# description: Every BUILD_ARTIFACTS entry must be a BUILD_ARTIFACT produced in this attestation.
# custom:
#   short_name: artifact_build
#   failure_msg: BUILD_ARTIFACTS is not a build output from this attestation
#   solution: Pass BUILD_ARTIFACT results from run-build or run-build-remote
#   collections:
#   - byob
deny contains result if {
	some item in build_tasks
	not build_input_ok[sprintf("%s/%d", [item.run, item.index])]
	result := {
		"code": "policy.artifact_build",
		"msg": sprintf("%s task %s BUILD_ARTIFACTS must be build outputs from this attestation", [item.pipeline, item.task.name]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Pack publishes the build outputs
# description: Pack must publish the build artifacts as the attested image.
# custom:
#   short_name: artifact_pack
#   failure_msg: Pack did not publish the build outputs
#   solution: Restore the pack artifact references, image, and artifact type
#   collections:
#   - byob
deny contains result if {
	some pair in pipeline_of
	not pack_ok[pair.run]
	result := {
		"code": "policy.artifact_pack",
		"msg": sprintf("%s pack must publish the build outputs from this attestation", [pair.pipeline]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Artifacts stayed in the pipeline storage
# description: Checkout and build tasks must use the oci-storage repository from the pipeline parameters.
# custom:
#   short_name: trusted_storage
#   failure_msg: A task did not use the attested artifact storage
#   solution: Store artifacts in the oci-storage repository
#   collections:
#   - byob
deny contains result if {
	some item in tasks
	item.task.name == "checkout"
	not storage_ok[sprintf("%s/%d", [item.run, item.index])]
	result := {
		"code": "policy.trusted_storage",
		"msg": sprintf("%s task %s must store artifacts in the attested oci-storage repository", [item.pipeline, item.task.name]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: Build tasks store artifacts in the pipeline storage
# description: Build tasks must use the oci-storage repository from the pipeline parameters.
# custom:
#   short_name: build_storage
#   failure_msg: A build task did not use the attested artifact storage
#   solution: Store build artifacts in the oci-storage repository
#   collections:
#   - byob
deny contains result if {
	some item in build_tasks
	not storage_ok[sprintf("%s/%d", [item.run, item.index])]
	result := {
		"code": "policy.build_storage",
		"msg": sprintf("%s task %s must store artifacts in the attested oci-storage repository", [item.pipeline, item.task.name]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}

# METADATA
# title: The attested command stayed inside the source tree
# description: build-cmd must be a relative path, and that path must be the developer step that ran.
# custom:
#   short_name: build_command
#   failure_msg: The build command can leave the reviewed source
#   solution: Use a relative path such as ./build.sh
#   collections:
#   - byob
deny contains result if {
	some item in build_tasks
	not command_ok[sprintf("%s/%d", [item.run, item.index])]
	result := {
		"code": "policy.build_command",
		"msg": sprintf("%s task %s build-cmd must be a relative path inside the reviewed source", [item.pipeline, item.task.name]),
		"collections": ["byob"],
		"effective_on": "2024-01-01T00:00:00Z",
	}
}
