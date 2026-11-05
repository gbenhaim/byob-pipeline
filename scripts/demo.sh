#!/usr/bin/env bash
# Orchestrate the existing catalog on a local Konflux Kind cluster.
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
CLUSTER=${BYOB_KIND_CLUSTER:-konflux}
CONTEXT="kind-$CLUSTER"
NAMESPACE=${BYOB_NAMESPACE:-byob-demo}
GIT_URL=${BYOB_GIT_URL:-https://github.com/gbenhaim/byob-system-fixture}
GIT_REF=${BYOB_GIT_REF:-main}
REGISTRY=registry-service.kind-registry
STATE_DIR="$ROOT/dist/demo/$NAMESPACE"
TIMEOUT=${BYOB_DEMO_TIMEOUT:-600}
CLI_IMAGE=localhost/byob-cli:devel
UI_USER=${BYOB_UI_USER:-user1@konflux.dev}
UI_URL=${BYOB_UI_URL:-https://localhost:9443}
UI_URL=${UI_URL%/}

# fail reports an actionable error without printing credentials.
fail() { printf 'demo: %s\n' "$*" >&2; exit 1; }

# k always targets the selected local Kind context and demo namespace.
k() { kubectl --context "$CONTEXT" --namespace "$NAMESPACE" "$@"; }

# validate_settings rejects unsafe cluster, namespace, and timeout choices.
validate_settings() {
    [[ $CLUSTER =~ ^[a-z0-9][a-z0-9-]*$ ]] || fail 'invalid Kind cluster name'
    [[ $NAMESPACE =~ ^byob-demo(-[a-z0-9][a-z0-9-]*)?$ && ${#NAMESPACE} -le 63 ]] ||
        fail 'BYOB_NAMESPACE must be byob-demo or byob-demo-SUFFIX'
    [[ $TIMEOUT =~ ^[1-9][0-9]*$ ]] || fail 'timeout must be seconds'
}

# validate_image requires the same immutable manifest identity at every gate.
validate_image() {
    [[ ${1:-} =~ ^[a-zA-Z0-9.-]+(:[0-9]+)?/[a-zA-Z0-9._/-]+@sha256:[a-f0-9]{64}$ ]] ||
        fail 'expected an untagged registry/repository@sha256 digest reference'
}

# revision resolves a public pushed branch or accepts an explicit exact commit.
revision() {
    local commit=${BYOB_REVISION:-}
    if [[ -z $commit ]]; then
        commit=$(git ls-remote --exit-code "$GIT_URL" "refs/heads/$GIT_REF" | awk '{print $1}')
    fi
    [[ $commit =~ ^[a-f0-9]{40}$ ]] || fail 'source must resolve to one exact Git commit'
    printf '%s' "$commit"
}

# render emits reviewable resources without invoking Kubernetes.
render() {
    local resource=${1:-} image=${2:-} commit name
    name="byob-demo-$(date +%s)-$RANDOM"
    case "$resource" in
        build)
            commit=$(revision)
            jq -n --arg ns "$NAMESPACE" --arg name "$name" \
                --arg url "$GIT_URL" --arg revision "$commit" \
                --arg image "$REGISTRY/$NAMESPACE/output:$name" \
                --arg storage "$REGISTRY/$NAMESPACE/ta" '{
                apiVersion:"tekton.dev/v1",kind:"PipelineRun",
                metadata:{name:$name,namespace:$ns,labels:{
                    "appstudio.openshift.io/application":"byob-slsa",
                    "appstudio.openshift.io/component":"byob",
                    "pipelines.appstudio.openshift.io/type":"build",
                    "byob.demo/owned":"true"}},
                spec:{taskRunTemplate:{serviceAccountName:"byob-build"},
                    pipelineRef:{name:"byob"},params:[
                    {name:"git-url",value:$url},{name:"revision",value:$revision},
                    {name:"output-image",value:$image},{name:"oci-storage",value:$storage}]}}'
            ;;
        snapshot)
            validate_image "$image"
            jq -n --arg ns "$NAMESPACE" --arg name "$name" --arg image "$image" '{
                apiVersion:"appstudio.redhat.com/v1alpha1",kind:"Snapshot",
                metadata:{name:$name,namespace:$ns,labels:{
                    "appstudio.openshift.io/application":"byob-slsa",
                    "byob.demo/owned":"true"}},
                spec:{application:"byob-slsa",components:[{name:"byob",containerImage:$image}]}}'
            ;;
        release)
            validate_image "$image"
            jq -n --arg ns "$NAMESPACE" --arg name "$name-release" --arg image "$image" '{
                apiVersion:"tekton.dev/v1",kind:"PipelineRun",
                metadata:{name:$name,namespace:$ns,labels:{
                    "appstudio.openshift.io/application":"byob-slsa",
                    "pipelines.appstudio.openshift.io/type":"release",
                    "byob.demo/owned":"true"}},
                spec:{taskRunTemplate:{serviceAccountName:"byob-build",
                    podTemplate:{securityContext:{fsGroup:0}}},
                    pipelineRef:{name:"byob-demo-release"},params:[{name:"image",value:$image}]}}'
            ;;
        validator)
            jq --arg ns "$NAMESPACE" '{
                apiVersion:"tekton.dev/v1",kind:"Task",
                metadata:{name:"byob-demo-verify",namespace:$ns},
                spec:((.spec.tasks[0].taskSpec | del(.metadata, .spec)) +
                    {params:[{name:"SNAPSHOT",type:"string"}]})}'
            ;;
        ui-access)
            jq -n --arg ns "$NAMESPACE" --arg user "$UI_USER" '{
                apiVersion:"rbac.authorization.k8s.io/v1",kind:"RoleBinding",
                metadata:{name:"byob-demo-ui-viewer",namespace:$ns,
                    labels:{"byob.demo/owned":"true"}},
                roleRef:{apiGroup:"rbac.authorization.k8s.io",kind:"ClusterRole",
                    name:"konflux-viewer-user-actions"},
                subjects:[{apiGroup:"rbac.authorization.k8s.io",kind:"User",name:$user}]}'
            ;;
        *) fail 'render expects build, snapshot DIGEST_REFERENCE, release DIGEST_REFERENCE, validator, or ui-access' ;;
    esac
}

# state reads one retained nonsecret value and fails on missing prerequisites.
state() {
    [[ -f $STATE_DIR/state.json ]] || fail 'no state; run prepare and build first'
    jq -er --arg key "$1" '.[$key] // empty' "$STATE_DIR/state.json" ||
        fail "missing $1; complete the preceding demo stage"
}

# require_prepared prevents accidentally reusing another cluster's evidence.
require_prepared() {
    [[ $(state context) == "$CONTEXT" ]] ||
        fail 'evidence belongs to another context; choose a new demo namespace'
}

# ui_links prints the application activity and the retained rehearsal run URLs.
ui_links() {
    local base="$UI_URL/ns/$NAMESPACE/applications/byob-slsa" key name
    printf '\nKonflux UI: %s/activity/pipelineruns\n' "$base"
    printf 'Workspace: %s | Application: byob-slsa | Login: %s\n' "$NAMESPACE" "$UI_USER"
    if [[ -f $STATE_DIR/state.json ]]; then
        for key in good_build good_test good_release bad_build bad_test bad_release; do
            name=$(jq -r --arg key "$key" '.[$key] // empty' "$STATE_DIR/state.json")
            [[ -n $name ]] || continue
            printf '%s: %s/pipelineruns/%s\n' "$key" "$base" "$name"
        done
    fi
}

# configure_ui exposes only owned demo resources through the local Konflux UI.
configure_ui() {
    local owner
    owner=$(k get namespace "$NAMESPACE" -o jsonpath='{.metadata.labels.byob\.demo/owned}')
    [[ $owner == true ]] || fail 'namespace is missing the demo ownership label'
    k label namespace "$NAMESPACE" konflux-ci.dev/type=tenant --overwrite
    render ui-access | k apply -f -
    # Upgrade completed rehearsals as well as labeling future rendered runs.
    k label pipelineruns -l 'byob.demo/owned=true,tekton.dev/pipeline in (byob,byob-demo-rejected)' \
        appstudio.openshift.io/application=byob-slsa \
        pipelines.appstudio.openshift.io/type=build --overwrite
    k label pipelineruns -l 'byob.demo/owned=true,tekton.dev/pipeline=byob-demo-release' \
        appstudio.openshift.io/application=byob-slsa \
        pipelines.appstudio.openshift.io/type=release --overwrite
    ui_links
}

# remember atomically records run names and digest references, never credentials.
remember() {
    local key=$1 value=$2
    mkdir -p "$STATE_DIR"
    [[ -f $STATE_DIR/state.json ]] || printf '{}\n' > "$STATE_DIR/state.json"
    jq --arg key "$key" --arg value "$value" '.[$key]=$value' \
        "$STATE_DIR/state.json" > "$STATE_DIR/state.tmp"
    mv "$STATE_DIR/state.tmp" "$STATE_DIR/state.json"
}

# reset_run removes downstream evidence when a new build supersedes it.
reset_run() {
    jq --arg prefix "${1}_" 'with_entries(select(.key | startswith($prefix) | not))' \
        "$STATE_DIR/state.json" > "$STATE_DIR/state.tmp"
    mv "$STATE_DIR/state.tmp" "$STATE_DIR/state.json"
}

# diagnostics captures nonsecret TaskRun status and step logs for a PipelineRun.
diagnostics() {
    local name=$1
    k get taskruns -l "tekton.dev/pipelineRun=$name" -o wide >&2 || true
    k logs -l "tekton.dev/pipelineRun=$name" --all-containers \
        --prefix --tail=100 --max-log-requests=10 >&2 || true
}

# wait_run distinguishes a terminal failure from a timeout.
wait_run() {
    local name=$1 expected=${2:-True} started=$SECONDS phase
    while (( SECONDS - started < TIMEOUT )); do
        phase=$(k get pipelinerun "$name" -o json | jq -r \
            '[.status.conditions[]? | select(.type=="Succeeded")][0].status // "Unknown"')
        if [[ $phase == True || $phase == False ]]; then
            k get pipelinerun "$name" -o json > "$STATE_DIR/$name.json"
            k logs -l "tekton.dev/pipelineRun=$name" --all-containers \
                --prefix --tail=-1 --max-log-requests=10 > "$STATE_DIR/$name.log" 2>&1 || true
            if [[ $phase != "$expected" ]]; then
                diagnostics "$name"
                fail "$name finished $phase; expected $expected"
            fi
            return
        fi
        sleep 3
    done
    diagnostics "$name"
    fail "timed out waiting for $name"
}

# wait_signed waits for Chains to publish provenance for the output digest.
wait_signed() {
    local name=$1 started=$SECONDS signed
    while (( SECONDS - started < TIMEOUT )); do
        signed=$(k get pipelinerun "$name" -o json | jq -r '.metadata.annotations["chains.tekton.dev/signed"] // ""')
        [[ $signed != failed ]] || fail "Chains failed to sign $name"
        if [[ $signed == true ]]; then
            printf 'Chains signed %s\n' "$name"
            return
        fi
        sleep 3
    done
    fail "Chains did not sign $name; inspect tekton-chains-controller logs"
}

# merge_ca preserves public roots while adding the local registry CA.
merge_ca() {
    local target=$1 ca=$2 bundle patch
    bundle=$(kubectl --context "$CONTEXT" -n "$target" get configmap trusted-ca \
        --ignore-not-found -o json | jq -r '.data["ca-bundle.crt"] // ""')
    if [[ $bundle != *"$ca"* ]]; then
        bundle=$(printf '%s\n%s\n' "$bundle" "$ca")
        patch=$(jq -n --arg bundle "$bundle" '{data:{"ca-bundle.crt":$bundle}}')
        if kubectl --context "$CONTEXT" -n "$target" get configmap trusted-ca >/dev/null 2>&1; then
            kubectl --context "$CONTEXT" -n "$target" patch configmap trusted-ca --type merge -p "$patch" >/dev/null
        else
            printf '%s' "$bundle" | kubectl --context "$CONTEXT" -n "$target" \
                create configmap trusted-ca --from-file=ca-bundle.crt=/dev/stdin >/dev/null
        fi
    fi
}

# preflight checks the platform without changing the current kubeconfig context.
preflight() {
    local tool ready deployment
    for tool in kubectl jq git; do
        command -v "$tool" >/dev/null || fail "install $tool first"
    done
    ready=$(k get konflux konflux -o json | jq -r \
        '[.status.conditions[] | select(.type=="Ready")][0].status')
    [[ $ready == True ]] || fail 'Konflux is not Ready; bare Kind is insufficient'
    for deployment in tekton-pipelines-controller tekton-pipelines-remote-resolvers tekton-chains-controller; do
        kubectl --context "$CONTEXT" -n tekton-pipelines rollout status \
            "deployment/$deployment" --timeout=30s
    done
    kubectl --context "$CONTEXT" -n integration-service rollout status \
        deployment/integration-service-controller-manager --timeout=30s
    k get service -n kind-registry registry-service >/dev/null
    [[ $(k get configmap -n tekton-pipelines chains-config -o json | \
        jq -r '.data["artifacts.pipelinerun.enable-deep-inspection"]') == true ]] ||
        fail 'Chains must enable PipelineRun deep inspection'
}

# prepare installs only demo resources and the existing catalog in an owned namespace.
prepare() {
    local owner namespace_json ca auth public_key archive validator
    preflight
    namespace_json=$(k get namespace "$NAMESPACE" --ignore-not-found -o json)
    if [[ -n $namespace_json ]]; then
        owner=$(jq -r '.metadata.labels["byob.demo/owned"] // ""' <<< "$namespace_json")
        [[ $owner == true ]] || fail 'namespace already exists without the demo ownership label; choose a new suffix'
    else
        k create namespace "$NAMESPACE"
        k label namespace "$NAMESPACE" byob.demo/owned=true
    fi
    for tool in podman kind; do
        command -v "$tool" >/dev/null || fail "install $tool and add it to PATH"
    done
    if [[ ${BYOB_DEMO_SKIP_IMAGE:-0} != 1 ]]; then
        make -C "$ROOT" image
        archive=$(mktemp /tmp/byob-demo-image.XXXXXX.tar)
        podman save "$CLI_IMAGE" -o "$archive"
        kind load image-archive "$archive" --name "$CLUSTER"
        rm -f "$archive"
    fi
    ca=$(k get secret -n kind-registry local-registry-tls -o json | jq -er '.data["ca.crt"]' | base64 -d)
    [[ $ca == *'BEGIN CERTIFICATE'* ]] || fail 'registry CA is missing'
    merge_ca "$NAMESPACE" "$ca"
    # Chains uses the same registry; preserve its existing public CA roots.
    merge_ca tekton-pipelines "$ca"
    k apply -f "$ROOT/examples/platform/serviceaccounts.yaml" \
        -f "$ROOT/task/run-build/0.1/run-build.yaml" \
        -f "$ROOT/task/pack/0.1/pack.yaml" \
        -f "$ROOT/pipelines/byob.yaml" \
        -f "$ROOT/stepactions/unpack-build-output/0.1/unpack-build-output.yaml"
    auth=$(k get secret -n kind-registry internal-registry-client-auth --ignore-not-found -o json)
    if [[ -n $auth ]]; then
        jq --arg ns "$NAMESPACE" '.metadata={name:"byob-registry-auth",namespace:$ns} | del(.status)' <<< "$auth" | k apply -f - >/dev/null
        k patch serviceaccount byob-build --type merge -p \
            '{"secrets":[{"name":"byob-registry-auth"}],"imagePullSecrets":[{"name":"byob-registry-auth"}]}' >/dev/null
    fi
    public_key=$(k get secret -n openshift-pipelines public-key -o json | jq -er '.data["cosign.pub"]')
    jq -n --arg ns "$NAMESPACE" --arg key "$public_key" '{apiVersion:"v1",kind:"Secret",
        metadata:{name:"chains-public-key",namespace:$ns},type:"Opaque",data:{"cosign.pub":$key}}' | k apply -f - >/dev/null
    k create configmap byob-pipeline-policy \
        --from-file="policy.yaml=$ROOT/policy/policy.yaml" \
        --from-file="pipeline.rego=$ROOT/policy/rules/pipeline.rego" \
        --dry-run=client -o json | k apply -f -
    k apply -f "$ROOT/examples/integration/byob-pipeline-policy.yaml"
    sed "s/__NAMESPACE__/$NAMESPACE/g" "$ROOT/examples/integration/byob-slsa.yaml" | k apply -f -
    # Reuse the exact integration validator, including its signature checks and assert step.
    mkdir -p "$STATE_DIR"
    validator="$STATE_DIR/verify-task.json"
    k get pipeline byob-pipeline-policy -o json | render validator > "$validator"
    k apply -f "$validator" -f "$ROOT/examples/demo/release.yaml"
    remember context "$CONTEXT"
    configure_ui
    printf '\nPrepared %s / %s. Next: bash scripts/demo.sh build\n' "$CONTEXT" "$NAMESPACE"
}

# build_one executes a catalog run and records its signed immutable output.
build_one() {
    local mode=${1:-good} manifest name tagged digest image
    mkdir -p "$STATE_DIR"
    manifest=$(render build)
    reset_run "$mode"
    if [[ $mode == bad ]]; then
        # The separate variant cannot change the original byob Pipeline.
        k get pipeline byob -o json | jq '{apiVersion,kind,
            metadata:{name:"byob-demo-rejected"},spec} |
            (.spec.tasks[] | select(.name=="pack").params[] | select(.name=="artifactType").value) =
            "application/vnd.konflux.build-output.unsafe"' | k apply -f - >/dev/null
        manifest=$(jq '.spec.pipelineRef.name="byob-demo-rejected"' <<< "$manifest")
    fi
    name=$(k create -f - -o jsonpath='{.metadata.name}' <<< "$manifest")
    remember "${mode}_build" "$name"
    printf 'Running %s (checkout -> build -> pack)\n' "$name"
    wait_run "$name"
    k get taskruns -l "tekton.dev/pipelineRun=$name"
    tagged=$(k get pipelinerun "$name" -o json | jq -er '.status.results[] | select(.name=="IMAGE_URL").value')
    digest=$(k get pipelinerun "$name" -o json | jq -er '.status.results[] | select(.name=="IMAGE_DIGEST").value')
    image="${tagged%:*}@$digest"
    validate_image "$image"
    remember "${mode}_image" "$image"
    wait_signed "$name"
    k get pipelinerun "$name" -o json > "$STATE_DIR/$name.json"
    printf 'Immutable output: %s\n' "$image"
}

# report requires the integration summary as well as the PipelineRun condition.
report() {
    local name=$1 expected=$2 code=${3:-} summary rg_code
    summary=$(k get pipelinerun "$name" -o json | jq -er '.status.results[] | select(.name=="TEST_OUTPUT").value')
    printf '%s\n' "$summary" | jq .
    [[ $(jq -r .result <<< "$summary") == "$expected" ]] || fail "expected TEST_OUTPUT=$expected"
    if [[ -n $code ]]; then
        rg_code=$(grep -F "$code" "$STATE_DIR/$name.log" || true)
        [[ -n $rg_code ]] || fail "failed without expected code $code; inspect $STATE_DIR/$name.log"
        printf '%s\n' "$rg_code"
    fi
}

# verify_one triggers the actual integration service through a Snapshot.
verify_one() {
    local mode=${1:-good} image snapshot name='' started=$SECONDS expected=True
    image=$(state "${mode}_image")
    snapshot=$(render snapshot "$image" | k create -f - -o jsonpath='{.metadata.name}')
    remember "${mode}_snapshot" "$snapshot"
    printf 'Snapshot %s triggers IntegrationTestScenario byob-pipeline-policy\n' "$snapshot"
    while (( SECONDS - started < TIMEOUT )); do
        name=$(k get pipelineruns -l "appstudio.openshift.io/snapshot=$snapshot,pipelines.appstudio.openshift.io/type=test" \
            -o json | jq -r '.items[0].metadata.name // ""')
        [[ -z $name ]] || break
        sleep 3
    done
    [[ -n $name ]] || fail 'integration service did not create a test PipelineRun'
    remember "${mode}_test" "$name"
    [[ $mode == good ]] || expected=False
    wait_run "$name" "$expected"
    if [[ $mode == good ]]; then
        report "$name" SUCCESS
    else
        report "$name" FAILURE policy.artifact_pack
    fi
}

# release_one verifies again in the release Pipeline before consuming the payload.
release_one() {
    local mode=${1:-good} image name expected=True count
    image=$(state "${mode}_image")
    name=$(render release "$image" | k create -f - -o jsonpath='{.metadata.name}')
    remember "${mode}_release" "$name"
    [[ $mode == good ]] || expected=False
    wait_run "$name" "$expected"
    k get taskruns -l "tekton.dev/pipelineRun=$name"
    if [[ $mode == good ]]; then
        report "$name" SUCCESS
        k logs -l "tekton.dev/pipelineRun=$name,tekton.dev/pipelineTask=unpack" -c step-consume
    else
        # Pipeline results may be absent when an earlier task failed; inspect the verify TaskRun.
        grep -F policy.artifact_pack "$STATE_DIR/$name.log" >/dev/null || fail 'release failed without artifact_pack'
        count=$(k get taskruns -l "tekton.dev/pipelineRun=$name,tekton.dev/pipelineTask=unpack" -o json | jq '.items | length')
        [[ $count == 0 ]] || fail 'rejected release created an unpack TaskRun'
        printf 'Release gate rejected the artifact. Unpack TaskRuns: 0\n'
    fi
}

# status shows retained real evidence without rerunning any workloads.
status() {
    [[ -f $STATE_DIR/state.json ]] || fail 'no demo evidence yet'
    jq . "$STATE_DIR/state.json"
    k get pipelineruns
    printf '\nEvidence: %s\n' "$STATE_DIR"
    printf 'Inspect: kubectl --context %s -n %s get pipelinerun NAME -o yaml\n' "$CONTEXT" "$NAMESPACE"
    ui_links
}

# evidence prints a compact replay of actual retained results without a cluster.
evidence() {
    local stage=${1:-} name image
    case "$stage" in
        build)
            name=$(state good_build)
            image=$(state good_image)
            printf 'Build: %s\nOutput: %s\n' "$name" "$image"
            jq '{condition:.status.conditions, tasks:.status.childReferences,
                chains_signed:.metadata.annotations["chains.tekton.dev/signed"]}' "$STATE_DIR/$name.json"
            ;;
        verify)
            name=$(state good_test)
            printf 'Integration PipelineRun: %s\n' "$name"
            jq '.status.results[] | select(.name=="TEST_OUTPUT") | .value | fromjson' "$STATE_DIR/$name.json"
            ;;
        release)
            name=$(state good_release)
            printf 'Release PipelineRun: %s\n' "$name"
            jq '{condition:.status.conditions, results:.status.results, tasks:.status.childReferences}' "$STATE_DIR/$name.json"
            grep -E 'Release Task consumed|Built on' "$STATE_DIR/$name.log"
            ;;
        reject)
            name=$(state bad_test)
            printf 'Rejected integration PipelineRun: %s\n' "$name"
            jq '.status.results[] | select(.name=="TEST_OUTPUT") | .value | fromjson' "$STATE_DIR/$name.json"
            grep -F policy.artifact_pack "$STATE_DIR/$name.log"
            name=$(state bad_release)
            printf '\nRejected release PipelineRun: %s\n' "$name"
            jq '{condition:.status.conditions,tasks:.status.childReferences,skipped:.status.skippedTasks}' "$STATE_DIR/$name.json"
            ;;
        *) fail 'evidence expects build, verify, release, or reject' ;;
    esac
    printf '\nRetained real evidence; files: %s\n' "$STATE_DIR"
}

# main dispatches stages; setup is deliberately separate from the timed talk.
main() {
    validate_settings
    case "${1:-help}" in
        build|verify|release|reject|ui) require_prepared ;;
        prepare|all)
            if [[ -f $STATE_DIR/state.json ]]; then
                require_prepared
            fi
            ;;
    esac
    case "${1:-help}" in
        render) shift; render "$@" ;;
        prepare) prepare ;;
        build) build_one good ;;
        verify) verify_one good ;;
        release) release_one good ;;
        reject) build_one bad; verify_one bad; release_one bad ;;
        status) status ;;
        ui) configure_ui ;;
        evidence) evidence "${2:-}" ;;
        all) prepare; build_one good; verify_one good; release_one good;
            build_one bad; verify_one bad; release_one bad; status ;;
        help|--help|-h)
            printf '%s\n' 'Usage: bash scripts/demo.sh prepare|build|verify|release|reject|ui|status|all' \
                'Read-only: render build|snapshot DIGEST_REFERENCE|release DIGEST_REFERENCE' \
                'UI setup preview: render ui-access (BYOB_UI_USER=user1@konflux.dev)' \
                'UI links: ui (also upgrades existing runs); BYOB_UI_URL=https://localhost:9443' \
                'Replay retained results: evidence build|verify|release|reject' \
                'Defaults: BYOB_KIND_CLUSTER=konflux BYOB_NAMESPACE=byob-demo' \
                'Requires an existing Konflux Kind cluster. See docs/presentation/README.md.' ;;
        *) fail "unknown command: $1" ;;
    esac
}

main "$@"
