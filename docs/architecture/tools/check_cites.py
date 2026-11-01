#!/usr/bin/env python3
"""Verify every `path:line` cite AND every bare `:line` shorthand in the artifacts.

Bare shorthand resolves against the nearest preceding file path on the same line
(path may itself be written without a line spec). Unresolvable shorthand is
reported as BARE for manual review instead of being silently skipped.
"""
import os, re

ROOT = os.getcwd()
FILES = [
    'docs/architecture/current-architecture.md',
    'docs/architecture/flows.md',
    'docs/architecture/risk-review.md',
    'docs/architecture/module-deps.dot',
    'docs/architecture/flows.dot',
    'docs/architecture/bald-c4.dsl',
    'docs/architecture/README.md',
]
EXT = r'(?:go|proto|yml|yaml|json|mod|md|dot|dsl|sum)'
# a file path; optionally followed by :line spec
PATH = re.compile(r'(?<![A-Za-z0-9_./-])([A-Za-z0-9_.][A-Za-z0-9_./-]*\.' + EXT + r')((?::\d+(?:-\d+)?)(?:,\d+(?:-\d+)?)*)?(?![.\w-])')
BARE = re.compile(r'(?<![A-Za-z0-9_./-]):(\d+)(?:-(\d+))?')

all_files = []
for dp, dn, fn in os.walk(ROOT):
    if '/.git' in dp:
        continue
    for f in fn:
        all_files.append(os.path.relpath(os.path.join(dp, f), ROOT))
by_suffix = {}
for p in all_files:
    parts = p.split('/')
    for i in range(len(parts)):
        by_suffix.setdefault('/'.join(parts[i:]), []).append(p)


def resolve(path):
    if path.startswith('./'):
        path = path[2:]
    if os.path.isfile(os.path.join(ROOT, path)):
        return path, None
    c = sorted(set(by_suffix.get(path, [])))
    if len(c) == 1:
        return c[0], None
    if len(c) > 1:
        return None, 'AMBIGUOUS(%d): %s' % (len(c), ', '.join(c[:3]))
    return None, 'NOFILE'


_cache = {}
def lines_of(rel):
    if rel not in _cache:
        with open(os.path.join(ROOT, rel), encoding='utf-8', errors='replace') as fh:
            _cache[rel] = fh.read().splitlines()
    return _cache[rel]


checked, problems, bare_ok, bare_bad = set(), 0, 0, 0
for doc in FILES:
    if not os.path.exists(doc):
        print('MISSING DOC', doc); continue
    print('\n### ' + doc)
    with open(doc, encoding='utf-8', errors='replace') as fh:
        raw = fh.read().replace('\\n', ' ')
    current = None
    for lineno, line in enumerate(raw.splitlines(), 1):
        cont = bool(re.match(r'^(\s|[-*] |\d+\. )', line))
        if not cont:
            current = None
        tokens = []
        for m in PATH.finditer(line):
            if '*' in m.group(1) or '{' in m.group(1):
                continue  # glob pattern, not a citation
            tokens.append((m.start(), m.group(1), m.group(2)))
        for m in BARE.finditer(line):
            if not m.group(2):  # 'N-M' shorthand handled by BARE range only via PATH; treat single
                pass
            tokens.append((m.start(), None, ':' + m.group(1) + (('-' + m.group(2)) if m.group(2) else '')))
        tokens.sort()
        for pos, path, spec in tokens:
            if path:
                current = path
                rel, err = resolve(path)
                if err:
                    print('  !! L%d %-28s %s %s' % (lineno, path, spec or '', err)); problems += 1
                    current = None
                    continue
            else:
                rel = None
                if current:
                    rel, err = resolve(current)
                    if err:
                        rel = None
            if not spec:
                continue
            for one in spec.split(','):
                body = one.lstrip(':')
                nums = [int(x) for x in body.split('-') if x]
                for n in nums:
                    if n == 0:
                        continue  # ':0' is the dynamic-port token, not a line
                    if rel is None:
                        if path is None and ('端口' not in line[max(0, pos - 8):pos]):
                            print('  ?? L%d BARE %-10s (no preceding file)' % (lineno, one)); bare_bad += 1
                        continue
                    key = (rel, n)
                    if key in checked:
                        continue
                    checked.add(key)
                    ls = lines_of(rel)
                    if n > len(ls):
                        print('  !! L%d OOR %s%s (file has %d lines)' % (lineno, rel, one, len(ls)))
                        problems += 1
                        continue
                    content = ls[n - 1].strip()
                    if path is None:
                        bare_ok += 1
                    mark = '    ' if content else ' ~  '
                    print('  %sL%-4d %-42s :%-5d | %s' % (mark, lineno, rel, n, content[:90]))

print('\nchecked cites: %d, problems: %d, bare-resolved: %d, bare-unresolved: %d'
      % (len(checked), problems, bare_ok, bare_bad))
