/* Offline presentation: examples explain the policy; only the terminal runs it. */
(() => {
  "use strict";

  const slides = [...document.querySelectorAll(".slide")];
  const outline = [...document.querySelectorAll("#outline a")];
  const dots = [...document.querySelectorAll("#progress-dots span")];
  const labels = [
    "THE IDEA",
    "THE FRICTION",
    "THE BOUNDARY",
    "THE IMPLEMENTATION",
    "THE RELEASE GATE",
    "SHOW IT WORKING",
    "THE TAKEAWAY",
  ];
  const repo =
    "https://github.com/gbenhaim/byob-pipeline/blob/feat/byob-pipelines/";
  const sources = {
    adr: "https://github.com/konflux-ci/architecture/blob/8a1811d7d8c59ebf6093ea3e795ac91cdd27980c/ADR/0073-bring-your-own-build-logic.md",
    pipeline: repo + "pipelines/byob.yaml",
    policy: repo + "policy/rules/pipeline.rego",
  };
  document.querySelectorAll("[data-source]").forEach((link) => {
    link.href = sources[link.dataset.source];
    link.target = "_blank";
    link.rel = "noreferrer";
  });

  let current = 0;
  let toastTimer;
  const setText = (id, value) => {
    document.getElementById(id).textContent = value;
  };

  function toast(message) {
    const element = document.getElementById("toast");
    element.textContent = message;
    element.classList.add("visible");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => element.classList.remove("visible"), 3500);
  }

  function show(index, moveFocus = false) {
    current = Math.max(0, Math.min(slides.length - 1, index));
    slides.forEach((slide, position) => {
      slide.hidden = position !== current;
    });
    outline.forEach((link, position) => {
      if (position === current) link.setAttribute("aria-current", "step");
      else link.removeAttribute("aria-current");
    });
    dots.forEach((dot, position) =>
      dot.classList.toggle("active", position === current),
    );
    document.getElementById("previous").disabled = current === 0;
    document.getElementById("next-slide").disabled =
      current === slides.length - 1;
    setText("current-number", String(current + 1).padStart(2, "0"));
    setText("slide-label", labels[current]);
    document.title = `${labels[current]} · BYOB / Konflux`;
    if (location.hash !== `#${slides[current].id}`)
      location.hash = slides[current].id;
    if (moveFocus)
      document.getElementById("slide-content").focus({ preventScroll: true });
    window.scrollTo({ top: 0, behavior: "instant" });
  }

  function fromHash() {
    const index = slides.findIndex((slide) => `#${slide.id}` === location.hash);
    show(index < 0 ? 0 : index);
  }

  function toggleNotes() {
    const active = document.body.classList.toggle("show-notes");
    document
      .getElementById("notes-toggle")
      .setAttribute("aria-pressed", String(active));
  }

  async function toggleFullscreen() {
    try {
      if (document.fullscreenElement) await document.exitFullscreen();
      else if (document.documentElement.requestFullscreen)
        await document.documentElement.requestFullscreen();
      else toast("Use your browser’s fullscreen command to present.");
    } catch {
      toast(
        "Fullscreen is unavailable here. Use your browser’s fullscreen command.",
      );
    }
  }

  // Group controls keep keyboard focus and expose their selected state.
  function selectGroup(selector, selected) {
    document.querySelectorAll(selector).forEach((button) => {
      const active = button === selected;
      button.classList.toggle("active", active);
      button.setAttribute("aria-pressed", String(active));
    });
  }

  const boundary = {
    image: [
      "BUILD IMAGE",
      "A digest-pinned image supplies the toolchain. The current demo policy recognizes the catalog’s task-runner image.",
    ],
    command: [
      "BUILD COMMAND",
      "The repo uses build-cmd + build-args as argv, without shell parsing. The default is ./build.sh from the restored source at /var/source.",
    ],
    outputs: [
      "BUILD OUTPUT",
      "Write deliverables to $BUILD_OUTPUT (/var/byob/output). Only validated output files are exported; the source checkout and caches are excluded.",
    ],
  };
  document.querySelectorAll("[data-boundary]").forEach((button) =>
    button.addEventListener("click", () => {
      selectGroup("[data-boundary]", button);
      const [label, text] = boundary[button.dataset.boundary];
      const detail = document.getElementById("boundary-detail");
      detail.querySelector(".label").textContent = label;
      detail.querySelector("p").textContent = text;
    }),
  );

  const stages = {
    checkout: {
      label: "CHECKOUT / EXACT SOURCE",
      title: "Start with an immutable revision.",
      description:
        "The pinned git-clone bundle checks out the requested commit and exports a Trusted Artifact. Git credentials, when used, are mapped only to checkout.",
      file: "pipelines/byob.yaml",
      path: "pipelines/byob.yaml",
      code: "- name: revision\n  value: $(params.revision)\n\nSOURCE_ARTIFACT\nCHAINS-GIT_URL\nCHAINS-GIT_COMMIT",
    },
    build: {
      label: "RUN-BUILD / DEVELOPER LOGIC",
      title: "Bring a command. Export the outputs.",
      description:
        "The developer step runs as UID 1000, with capabilities dropped and no registry or Git credential mounts. Trusted prepare and export steps handle the artifacts.",
      file: "task/run-build/0.1/run-build.yaml",
      path: "task/run-build/0.1/run-build.yaml",
      code: "command:\n- $(params.build-cmd[*])\nargs:\n- $(params.build-args[*])\nworkingDir: /var/source\n# BUILD_OUTPUT=/var/byob/output",
    },
    pack: {
      label: "PACK / COMPONENT ARTIFACT",
      title: "Package once. Identify by digest.",
      description:
        "Pack combines validated build outputs into payload.tar.gz in an OCI artifact. Only pack emits the final IMAGE_URL and IMAGE_DIGEST results; the Pipeline exposes them to Chains.",
      file: "task/pack/0.1/pack.yaml",
      path: "task/pack/0.1/pack.yaml",
      code: "artifactType:\n  application/vnd.konflux.build-output.v1\n\nlayer: payload.tar.gz\nresults:\n  IMAGE_URL + IMAGE_DIGEST",
    },
    chains: {
      label: "CHAINS / PLATFORM EVIDENCE",
      title: "Sign outside the developer’s reach.",
      description:
        "Tekton Chains records and signs provenance for the packaged subject. The Kind demo needs PipelineRun deep inspection and OCI storage. Build steps never receive the signing key.",
      file: "Kind platform prerequisite (not repo YAML)",
      path: "README.md#cluster-and-chains-assumptions",
      code: '# chains-config on the demo cluster\nartifacts.pipelinerun.format: in-toto\nartifacts.pipelinerun.storage: oci\nartifacts.pipelinerun.enable-deep-inspection:\n  "true"',
    },
  };
  document.querySelectorAll("[data-stage]").forEach((button) =>
    button.addEventListener("click", () => {
      selectGroup("[data-stage]", button);
      const stage = stages[button.dataset.stage];
      setText("stage-label", stage.label);
      setText("stage-title", stage.title);
      setText("stage-description", stage.description);
      setText("code-filename", stage.file);
      setText("stage-code", stage.code);
      document.getElementById("stage-source").href = repo + stage.path;
    }),
  );

  const cases = {
    good: [
      "ALLOW",
      "TEST_OUTPUT.result = SUCCESS",
      "The signed subject, source identity, approved builder, Task shape, and artifact wiring satisfy the current policy.",
      "The release Task may consume this digest.",
    ],
    format: [
      "DENY",
      "policy.artifact_pack",
      "The build finished and Chains signed it. The recorded pack artifact type differs from application/vnd.konflux.build-output.v1, so the current policy rejects it.",
      "TEST_OUTPUT.result = FAILURE · unpack does not run.",
    ],
    foreign: [
      "DENY",
      "policy.artifact_source",
      "The input digest came from another run. A valid Trusted Artifact is insufficient: the policy requires a connection to this attestation’s checkout or build results.",
      "TEST_OUTPUT.result = FAILURE · broken build lineage.",
    ],
    unsigned: [
      "DENY",
      "builtin.image.signature_check",
      "The artifact lacks a trusted signature. Cryptographic validation fails before a successful policy summary; no build-policy success can substitute for that evidence.",
      "No successful verification · release must stop.",
    ],
  };
  document.querySelectorAll("[data-case]").forEach((button) =>
    button.addEventListener("click", () => {
      selectGroup("[data-case]", button);
      const selected = button.dataset.case;
      const [status, code, description, footer] = cases[selected];
      setText("result-status", status);
      setText("result-code", code);
      setText("result-description", description);
      setText("result-footer", footer);
      setText(
        "gate-last",
        selected === "good" ? "Release consumption" : "Release blocked",
      );
      document
        .getElementById("policy-result")
        .classList.toggle("rejected", selected !== "good");
    }),
  );

  const demo = {
    build: [
      "Three successful TaskRuns + a signed digest",
      "Inspect checkout, build, and pack. The script waits for Chains and prints registry/repository@sha256:…",
    ],
    verify: [
      "TEST_OUTPUT.result = SUCCESS",
      "The script creates a real Snapshot. The integration service starts byob-pipeline-policy, verifies signatures, and evaluates the signed provenance.",
    ],
    release: [
      "hello.txt restored by the StepAction",
      "The release Pipeline reuses the same Conforma validator. Its dependent Task invokes unpack-build-output on the same digest, then reads hello.txt.",
    ],
    reject: [
      "Build succeeded. Release rejected. Unpack never ran.",
      "A separate build variant changes the artifact type. Require policy.artifact_pack in the integration report and zero unpack TaskRuns in the rejected release. The variant name can also fail pipeline_name.",
    ],
  };
  document.querySelectorAll("[data-demo]").forEach((button) =>
    button.addEventListener("click", () => {
      selectGroup("[data-demo]", button);
      const action = button.dataset.demo;
      setText("demo-command", `bash scripts/demo.sh ${action}`);
      setText("demo-evidence-title", demo[action][0]);
      setText("demo-evidence", demo[action][1]);
    }),
  );

  document
    .getElementById("copy-command")
    .addEventListener("click", async () => {
      const command = document.getElementById("demo-command").textContent;
      try {
        await navigator.clipboard.writeText(command);
        toast("Command copied. Run it from the repository root.");
      } catch {
        const selection = window.getSelection();
        const range = document.createRange();
        range.selectNodeContents(document.getElementById("demo-command"));
        selection.removeAllRanges();
        selection.addRange(range);
        toast("Command selected. Use your browser’s Copy command.");
      }
    });

  document
    .getElementById("previous")
    .addEventListener("click", () => show(current - 1, true));
  document
    .getElementById("next-slide")
    .addEventListener("click", () => show(current + 1, true));
  document
    .getElementById("notes-toggle")
    .addEventListener("click", toggleNotes);
  document
    .getElementById("fullscreen-toggle")
    .addEventListener("click", toggleFullscreen);
  outline.forEach((link, index) =>
    link.addEventListener("click", (event) => {
      event.preventDefault();
      show(index, true);
    }),
  );
  window.addEventListener("hashchange", fromHash);
  document.querySelector(".skip-link").addEventListener("click", (event) => {
    event.preventDefault();
    document.getElementById("slide-content").focus({ preventScroll: true });
  });
  window.addEventListener("keydown", (event) => {
    if (
      event.altKey ||
      event.ctrlKey ||
      event.metaKey ||
      event.target.closest(
        "input, textarea, select, button, summary, [contenteditable=true]",
      )
    )
      return;
    if (
      event.key === "ArrowRight" ||
      event.key === "PageDown" ||
      event.key === " "
    ) {
      event.preventDefault();
      show(current + 1, true);
    } else if (event.key === "ArrowLeft" || event.key === "PageUp") {
      event.preventDefault();
      show(current - 1, true);
    } else if (event.key === "Home") {
      event.preventDefault();
      show(0, true);
    } else if (event.key === "End") {
      event.preventDefault();
      show(slides.length - 1, true);
    } else if (event.key.toLowerCase() === "n") toggleNotes();
    else if (event.key.toLowerCase() === "f") toggleFullscreen();
  });
  fromHash();
})();
