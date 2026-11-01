#!/usr/bin/env python3
"""Make the open-model trial engines' manifest templates and profile entries (Repo C design note OPEN_MODEL_ENGINES).

Input: a models file (check-open-model-providers.py --emit, reviewed by hand) and the host's ABCP configuration
folder. For each provider in the models file it derives, from the host's own Codex templates (so the pinned Ralphex
binary, budgets, validation and acceptance stay exactly the host's):
  - manifest-template-open-<provider>.json (web apps) and, with --kinds web,node, manifest-template-node-open-<provider>.json:
    the same template with its executor replaced by the provider's (task and review on the model's own default level);
  - profile entries local-p02-<provider> (and local-p02-node-<provider>): copies of local-p02 (and local-p02-node)
    naming the new template and offering the provider's models by name (executor_choices.models).

Without --write it prints what it would write and changes nothing. With --write it writes the templates (0600; it
refuses to replace one unless --force) and profiles.json.open-models (0600): the live profiles.json with the new
entries added. It never writes profiles.json itself. Put that file in place during an announced ABCP restart with no
build running (the hard rule); keep the previous profiles.json to undo.
"""
import argparse, copy, json, os, sys
from pathlib import Path

CONFIG = "/home/devagent/local-integration/repo-c-abcp/runtime/abcp-config"
SECRETS = "/home/devagent/local-integration/repo-c-abcp/runtime/secrets"
LANES = "/home/devagent/.codex/lanes"
KINDS = {"web": ("manifest-template.json", "local-p02", "manifest-template-open-{p}.json", "local-p02-{p}"),
         "node": ("manifest-template-node.json", "local-p02-node", "manifest-template-node-open-{p}.json", "local-p02-node-{p}")}
# How many builds may run on one model at once. Groq's base limits (8K tokens a minute) cannot carry one build; on
# its paid plan one build at a time is the start.
SLOTS = {"together": 1, "groq": 1}


def provider_block(provider, entry, secrets, lanes, preflight):
    models = []
    for model in entry["models"]:
        out = {"name": model["name"], "id": model["id"], "origin": model["origin"], "context_window": int(model["context_window"]),
               "max_concurrent_builds": SLOTS.get(provider, 1)}
        if model.get("efforts"):
            out["efforts"] = list(model["efforts"])
        if model.get("default_effort"):
            out["default_effort"] = model["default_effort"]
        models.append(out)
    if not models:
        raise SystemExit(f"{provider}: the models file lists no model")
    return {
        "id": provider, "base_url": entry["base_url"], "key_file": f"{secrets}/{provider}-api-key", "wire_api": "responses",
        "codex_home": f"{lanes}/{provider}", "models": models, "preflight": preflight, "check_models": True,
        "queue_timeout": "20m", "on_unavailable": "wait",
    }


def write_private(path: Path, value, force: bool):
    if path.exists() and not force:
        raise SystemExit(f"{path} exists: pass --force to replace it")
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "w") as handle:
        json.dump(value, handle, indent=2)
        handle.write("\n")
    os.chmod(path, 0o600)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--models", required=True, help="the reviewed models file")
    parser.add_argument("--config-dir", default=CONFIG)
    parser.add_argument("--secrets", default=SECRETS)
    parser.add_argument("--lanes", default=LANES, help="folder of the providers' Codex homes (the usage collector reads it)")
    parser.add_argument("--kinds", default="web", help="web, or web,node")
    parser.add_argument("--providers", default="", help="only these providers, e.g. together")
    parser.add_argument("--first-model", default="", help="the profile's task model when a build picks none (default: the first production candidate)")
    parser.add_argument("--no-preflight", action="store_true", help="skip the free GET /models before each build")
    parser.add_argument("--write", action="store_true")
    parser.add_argument("--force", action="store_true")
    args = parser.parse_args()
    config = Path(args.config_dir)
    models = json.loads(Path(args.models).read_text())
    if models.get("schema_version") != 1:
        raise SystemExit("unknown models file")
    profiles = json.loads((config / "profiles.json").read_text())
    by_id = {p["profile_id"]: p for p in profiles["profiles"]}
    wanted = {p for p in args.providers.split(",") if p}
    added, templates = [], {}
    for provider, entry in models["providers"].items():
        if wanted and provider not in wanted:
            continue
        block = provider_block(provider, entry, args.secrets, args.lanes, not args.no_preflight)
        names = [m["name"] for m in block["models"]]
        first = args.first_model or next((m["name"] for m in entry["models"] if m.get("role") == "production-candidate"), names[0])
        if first not in names:
            raise SystemExit(f"{provider}: {first} is not in the models file")
        for kind in args.kinds.split(","):
            base_template, base_profile, template_name, profile_name = KINDS[kind]
            template = json.loads((config / base_template).read_text())
            if template.get("executor", {}).get("executor") != "codex":
                raise SystemExit(f"{base_template} is not a Codex template")
            template["executor"] = {"executor": "codex", "task_model": first, "provider": block}
            template_path = config / template_name.format(p=provider)
            templates[template_path] = template
            profile = copy.deepcopy(by_id[base_profile])
            profile["profile_id"] = profile_name.format(p=provider)
            profile["manifest_template_path"] = str(template_path)
            profile["executor_choices"] = {"models": names}
            added.append(profile)
    if not added:
        raise SystemExit("nothing to make")
    merged = copy.deepcopy(profiles)
    new_ids = {p["profile_id"] for p in added}
    merged["profiles"] = [p for p in merged["profiles"] if p["profile_id"] not in new_ids] + added
    if len(merged["profiles"]) > 64:
        raise SystemExit("ABCP takes at most 64 profiles")
    for path, template in templates.items():
        print(f"--- {path}: executor\n{json.dumps(template['executor'], indent=2)}")
    print("--- profiles added to profiles.json.open-models:")
    for profile in added:
        print(f"  {profile['profile_id']}: {profile['manifest_template_path']} models {profile['executor_choices']['models']}")
    if not args.write:
        print("dry run: nothing written (pass --write)")
        return 0
    for path, template in templates.items():
        write_private(path, template, args.force)
    write_private(config / "profiles.json.open-models", merged, True)
    lanes = Path(args.lanes)
    for provider in {p["provider"]["id"] for p in (t["executor"] for t in templates.values())}:
        home = lanes / provider
        home.mkdir(mode=0o700, parents=True, exist_ok=True)
        os.chmod(home, 0o700)
    print(f"wrote {len(templates)} template(s) and {config / 'profiles.json.open-models'}")
    print("next, with no build running: keep profiles.json as profiles.json.pre-open-models, move profiles.json.open-models "
          "into its place (0600), restart abcp serve, and read GET /v1/engines")
    return 0


if __name__ == "__main__":
    sys.exit(main())
