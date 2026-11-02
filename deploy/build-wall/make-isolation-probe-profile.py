#!/usr/bin/env python3
"""The proof build's profiles (Repo C design note BUILD_ISOLATION.md §8.1): each a copy of a live profile whose manifest
template gets one more validation step, the wall's own check (isolation-check.sh), with the canary's name and its
branch's blob filled in. A dry run prints what it would write; --write writes the templates (0600) and
profiles.json.isolation-probe beside profiles.json, never profiles.json itself.

  python3 make-isolation-probe-profile.py --canary <hex> --blob <sha> [--write]
"""
import argparse, json, os, pathlib, re, sys

C = pathlib.Path('/home/devagent/local-integration/repo-c-abcp/runtime/abcp-config')
# One per engine family: the server's Codex, Codex on an API key, an open model through the relay, Claude Code.
SOURCES = {'local-p02': 'isolation-probe', 'local-p02-codex-api': 'isolation-probe-codex-api',
           'local-p02-together': 'isolation-probe-together', 'local-p02-claude-api': 'isolation-probe-claude-api'}

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--canary', required=True, help='the canary name: lower-case hex')
    parser.add_argument('--blob', required=True, help="the canary branch's canary.txt blob")
    parser.add_argument('--write', action='store_true')
    args = parser.parse_args()
    if not re.fullmatch(r'[0-9a-f]{8,64}', args.canary) or not re.fullmatch(r'[0-9a-f]{40}', args.blob):
        sys.exit('canary must be hex and blob a 40-hex object id')
    check = (pathlib.Path(__file__).with_name('isolation-check.sh').read_text()
             .replace('@CANARYBLOB@', args.blob).replace('@CANARY@', args.canary))
    profiles = json.loads((C / 'profiles.json').read_text())
    by_id = {p['profile_id']: p for p in profiles['profiles']}
    added, templates = [], {}
    for source, suffix in SOURCES.items():
        if source not in by_id:
            print(f'skipped {source}: not a live profile'); continue
        profile = dict(by_id[source])
        template_path = pathlib.Path(profile['manifest_template_path'])
        template = json.loads(template_path.read_text())
        template['ralphex'].setdefault('validation', []).append({
            'id': 'wall-isolation-check', 'argv': ['/usr/bin/bash', '-c', check],
            'timeout': '5m', 'expected_duration': '30s', 'stall_timeout': '4m'})
        target = C / f'manifest-template-{suffix}.json'
        profile['profile_id'] = 'local-p02-' + suffix
        profile['manifest_template_path'] = str(target)
        templates[target] = template
        added.append(profile)
        print(f"{profile['profile_id']}: {source}'s template + wall-isolation-check -> {target.name}")
    if any(p['profile_id'] in by_id for p in added):
        sys.exit('a probe profile is there already')
    out = dict(profiles, profiles=profiles['profiles'] + added)
    if not args.write:
        print('dry run: nothing written'); return
    for path, template in templates.items():
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as f: json.dump(template, f, indent=1); f.write('\n')
    fd = os.open(C / 'profiles.json.isolation-probe', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as f: json.dump(out, f, indent=1); f.write('\n')
    print('written: the templates and profiles.json.isolation-probe')

if __name__ == '__main__':
    main()
