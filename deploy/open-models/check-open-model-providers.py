#!/usr/bin/env python3
"""Read-only check of the open-model providers (Repo C design note OPEN_MODEL_ENGINES).

For Together AI and Groq it checks each key file (present, a regular file, not a link, owned by this user, mode
0600 or 0400; the key is never printed), asks the provider's GET /models, and prints model ids only: the trial
candidates it finds, with their context and, where the provider lists one, their price. --all-models prints every id.

--emit FILE writes the candidates it found as a models file for make-open-model-profiles.py (0600). Review it
before use: the levels (efforts) in it are guesses until --probe-responses has confirmed them.

--probe-responses (opt-in, spends a few hundred tokens, well under one US cent in all) sends one tiny Responses
API request per candidate: the call Codex makes, with one function tool, with and without a reasoning level. It
prints the HTTP status, whether usage (and cached tokens) came back, and the provider's error code. Codex speaks
only the Responses API to a provider, so a provider that answers 404 here cannot build through Codex directly.

Nothing is written unless --emit is given; nothing is sent but GET /models unless --probe-responses is given.
"""
import argparse, json, os, re, stat, sys, urllib.error, urllib.request
from pathlib import Path

SECRETS = "/home/devagent/local-integration/repo-c-abcp/runtime/secrets"
PROVIDERS = {
    "together": {"base_url": "https://api.together.xyz/v1", "key": "together-api-key"},
    "groq": {"base_url": "https://api.groq.com/openai/v1", "key": "groq-api-key"},
}
# Trial candidates (revised 2026-11-01, from Together's serverless list: no Mistral, Gemma or Llama served): name, id pattern, origin, role, levels to try, default level.
# Origin is the country of the company that made the model. CN models are price and quality benchmarks on QA's app
# only, never customers' data, unless the owner chooses otherwise in writing.
CANDIDATES = {
    "together": [
        ("gpt-oss-120b", r"(?i)^openai/gpt-oss-120b$", "US", "production-candidate", ["low", "medium", "high"], "high"),
        ("nemotron-3.5-super", r"(?i)nemotron.*3\.5.*super", "US", "production-candidate", [], ""),
        ("beam-1.0", r"(?i)^reflection/beam-1\.0", "US", "production-candidate", [], ""),
        ("muse-spark-open", r"(?i)muse-spark-open", "US", "production-candidate", [], ""),
        # Free on Together; PrismML (US) compressed it from Alibaba's Qwen, so a benchmark like the CN models.
        ("bonsai-27b", r"(?i)ternary-bonsai-27b", "US", "free-qwen-based", [], ""),
        ("devstral", r"(?i)mistral.*(devstral|codestral)", "FR", "watch", [], ""),
        ("gemma-4-31b", r"(?i)gemma-?4.*31b", "US", "watch", [], ""),
        ("llama-4-maverick", r"(?i)llama-?4-maverick", "US", "watch", [], ""),
        ("glm-5.3", r"(?i)glm-5\.3$", "CN", "benchmark-only", [], ""),
        ("deepseek-v4.1-flash", r"(?i)deepseek-v4\.1-flash", "CN", "benchmark-only", [], ""),
    ],
    "groq": [
        ("gpt-oss-120b", r"(?i)^openai/gpt-oss-120b$", "US", "production-candidate", ["low", "medium", "high"], "high"),
        ("gpt-oss-20b", r"(?i)^openai/gpt-oss-20b$", "US", "watch", ["low", "medium", "high"], "high"),
    ],
}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """Never follow a redirect: the key would go with it."""

    def redirect_request(self, *args, **kwargs):
        return None


OPENER = urllib.request.build_opener(NoRedirect)


def read_key(path: Path):
    try:
        info = os.lstat(path)
    except FileNotFoundError:
        return None, "missing"
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        return None, "not a regular file (a link?)"
    if info.st_uid != os.getuid():
        return None, "not owned by this user"
    if info.st_mode & 0o077:
        return None, f"mode {stat.S_IMODE(info.st_mode):o}: must be 0600"
    value = path.read_text().rstrip("\r\n")
    if "\n" in value or not re.fullmatch(r"[\x21-\x7e]{16,512}", value) or re.match(r"^[A-Z][A-Z0-9_]*=", value):
        return None, "must hold one line: the key alone"
    return value, "ok"


def request(method, url, key, body=None, timeout=30):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers={
        "Authorization": "Bearer " + key, "Accept": "application/json", "User-Agent": "abcp-open-model-check/1",
        **({"Content-Type": "application/json"} if data else {})})
    try:
        with OPENER.open(req, timeout=timeout) as response:
            return response.status, json.loads(response.read(16 << 20) or b"null"), dict(response.headers)
    except urllib.error.HTTPError as error:
        try:
            payload = json.loads(error.read(64 << 10) or b"null")
        except ValueError:
            payload = None
        return error.code, payload, dict(error.headers or {})
    except (urllib.error.URLError, TimeoutError, ValueError) as error:
        return 0, {"error": {"message": type(error).__name__}}, {}


def models_of(payload):
    entries = payload if isinstance(payload, list) else (payload or {}).get("data") or []
    return [m for m in entries if isinstance(m, dict) and isinstance(m.get("id"), str)]


def context_of(model):
    for field in ("context_length", "context_window", "max_context_length"):
        if isinstance(model.get(field), int):
            return model[field]
    return None


def price_of(model):
    pricing = model.get("pricing")
    if isinstance(pricing, dict) and isinstance(pricing.get("input"), (int, float)):
        cached = pricing.get("cached_input", pricing.get("input_cached"))
        cached = f", cached {cached}" if isinstance(cached, (int, float)) else ""
        return f"US${pricing['input']} -> US${pricing.get('output')} per 1M{cached}"
    return ""


def error_code(payload):
    error = (payload or {}).get("error") if isinstance(payload, dict) else None
    if isinstance(error, dict):
        text = str(error.get("code") or error.get("type") or "") + " " + str(error.get("message") or "")
        return re.sub(r"\s+", " ", text).strip()[:160]
    return ""


def probe(base, key, model_id, effort):
    body = {"model": model_id, "input": "Reply with the single word: ok", "max_output_tokens": 64, "stream": False,
            "store": False, "tools": [{"type": "function", "name": "noop", "description": "Does nothing.",
                                       "parameters": {"type": "object", "properties": {}}}]}
    if effort:
        body["reasoning"] = {"effort": effort}
    status, payload, headers = request("POST", base + "/responses", key, body, timeout=90)
    usage = (payload or {}).get("usage") if isinstance(payload, dict) else None
    details = ""
    if isinstance(usage, dict):
        cached = (usage.get("input_tokens_details") or {}).get("cached_tokens")
        details = f"usage in={usage.get('input_tokens')} out={usage.get('output_tokens')} cached={'yes' if cached is not None else 'not given'}"
    limits = " ".join(f"{h}={headers[h]}" for h in ("retry-after", "x-ratelimit-remaining-tokens", "x-ratelimit-remaining-requests") if h in headers)
    return f"HTTP {status} {details} {error_code(payload) if status != 200 else ''} {limits}".strip()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--secrets", default=SECRETS, help="folder of together-api-key and groq-api-key")
    parser.add_argument("--provider", choices=["together", "groq", "all"], default="all")
    parser.add_argument("--together-base", default=PROVIDERS["together"]["base_url"],
                        help="e.g. https://us-api.together.ai/v1 for US-only processing (1.25x price, fewer models)")
    parser.add_argument("--groq-base", default=PROVIDERS["groq"]["base_url"])
    parser.add_argument("--all-models", action="store_true", help="print every model id, not only the candidates")
    parser.add_argument("--emit", help="write the candidates found as a models file (0600)")
    parser.add_argument("--probe-responses", action="store_true", help="spend a few tokens to test the Responses API per candidate")
    args = parser.parse_args()
    emitted, problems = {}, 0
    for provider, settings in PROVIDERS.items():
        if args.provider not in ("all", provider):
            continue
        base = args.together_base if provider == "together" else args.groq_base
        path = Path(args.secrets) / settings["key"]
        key, why = read_key(path)
        print(f"== {provider}  {base}\n   key file {path}: {why}")
        if key is None:
            problems += 1
            continue
        status, payload, _ = request("GET", base + "/models", key)
        models = models_of(payload) if status == 200 else []
        if status != 200:
            print(f"   GET /models: HTTP {status} {error_code(payload)}")
            problems += 1
            continue
        print(f"   GET /models: {len(models)} models")
        if args.all_models:
            for model in sorted(models, key=lambda m: m["id"]):
                print(f"     {model['id']}")
        found = []
        for name, pattern, origin, role, efforts, default in CANDIDATES[provider]:
            matches = [m for m in models if re.search(pattern, m["id"])]
            if not matches:
                print(f"   {name:<22} not listed ({role})")
                continue
            for match in matches:
                print(f"   {name:<22} {match['id']}  context {context_of(match)}  {price_of(match)}  origin {origin}, {role}")
            chosen = sorted(matches, key=lambda m: len(m["id"]))[0]
            found.append({"name": name, "id": chosen["id"], "origin": origin, "role": role,
                          "context_window": context_of(chosen) or 131072, "efforts": efforts, "default_effort": default})
            if args.probe_responses:
                # The model's default level (or "high", to learn whether it takes one), then none.
                for effort in [default or "high", ""]:
                    print(f"     responses ({'reasoning ' + effort if effort else 'no reasoning level'}): {probe(base, key, chosen['id'], effort)}")
        emitted[provider] = {"base_url": base, "models": found}
    if args.emit:
        target = Path(args.emit)
        descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
        with os.fdopen(descriptor, "w") as handle:
            json.dump({"schema_version": 1, "providers": emitted}, handle, indent=2)
            handle.write("\n")
        os.chmod(target, 0o600)
        print(f"wrote {target}: review the ids, context windows and levels before make-open-model-profiles.py")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
