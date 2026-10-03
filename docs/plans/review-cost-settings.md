# Review settings that cut a build's cost: the outside review and the review helpers

Status: **built**, 2026-10-03. Repo C design note `docs/product/design-notes/COST_PER_BUILD.md` (round 2, levers 3
and 4) asked for both. Each is a manifest setting that is off unless a template names it, so existing templates and
their digests do not change.

## Why

Round 1 (Repo C P0307) moved reviews to a cheaper model and ran one internal review pass. On the measured small app
that cut a build from US$3.72 to US$2.11. What is left is mostly review:
- the outside Codex review, and Claude weighing its findings: about US$1.10;
- the first internal review: five helper agents, each starting from nothing.

## The settings

| Manifest field | Values | Effect |
|---|---|---|
| `ralphex.external_review_tool` | `codex`, `none`; omitted keeps Ralphex's own choice | ABCP passes `--external-review-tool`. `custom` is refused because its script is not governed. Under the Codex executor only `none` is accepted, since Ralphex runs no outside review there. |
| `ralphex.review_agents` | two to five of `quality`, `implementation`, `testing`, `simplification`, `documentation`, each once | ABCP writes `prompts/review_first.txt` into the run's isolated Ralphex settings folder. It is Ralphex's own first-review prompt with only these agents launched, in Ralphex's order, and the count in its wording changed to match. |

**The prompt is tied to the pinned source.** ABCP's copy (`internal/ralphex/review_first.txt`) is Ralphex's prompt
at `pankajleh/ralphex-governance` `c66debc`, the `ralphex-v1.7.0-abcp2` pin. A manifest that names `review_agents`
must pin that source (`ralphex.source_sha`), or authority validation refuses it.

Templates are validated when `abcp serve` loads its profiles, so a new Ralphex pin with `review_agents` still set
stops the restart with a clear error instead of running an old prompt. The upgrade step is to copy the new
pin's prompt into `review_first.txt` and move `ReviewPromptSourceSHA` to the new source.

**What Ralphex does with the folder.** It reads prompts from `--config-dir` first and falls back to its built-in
ones per file. With one prompt present it installs none of its defaults there, and uses its built-in prompts for
everything else. Agent definitions are untouched; agents the prompt does not launch are simply not run.

## Tests

- `internal/ralphex`:
  - the argv carries `--external-review-tool`;
  - `custom` is refused;
  - Codex with `codex` is refused;
  - the prompt launches only the named agents, in order;
  - five agents give Ralphex's own prompt;
  - agent lists are validated;
  - the file is written mode 0600 where Ralphex reads it.
- `internal/authority`:
  - both fields are validated;
  - a manifest without them keeps its canonical form;
  - another source is refused.
- `internal/run`: a run hands Ralphex the flag. The fake engine finds the two-agent prompt in its settings folder.

## Live use

The host templates for the Claude API lane name `"external_review_tool": "none"` and
`"review_agents": ["quality", "implementation"]` for round 2. They take effect at the next ABCP restart, made with no
build running. Repo C measures the result on the same small app and a medium one. A setting that costs quality is
taken out of the templates again, which needs no code change.
