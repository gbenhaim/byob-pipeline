"""Read the actual Task definitions used by Tekton for behavioral tests."""
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]


def task(name):
    return yaml.safe_load((ROOT / "task" / name / "0.1" / f"{name}.yaml").read_text())


def step(task_name, step_name):
    return next(s for s in task(task_name)["spec"]["steps"] if s["name"] == step_name)
