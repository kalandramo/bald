#!/usr/bin/env python3
"""Structural sanity check for the DOT artifacts.

Checks per file:
  - balanced {} and [] outside quoted strings (multi-line statements joined)
  - every edge endpoint is declared as a node somewhere
  - duplicate node declarations
  - nodes declared but never used by any edge (reported, not an error)
This is a cheap pre-filter, not a render check — render with
`dot -Tsvg file.dot -o /tmp/out.svg` and require an empty stderr.
"""
import re, sys

KEYWORDS = {'digraph', 'graph', 'subgraph', 'node', 'edge', 'strict'}


def strip_strings(text):
    """Remove // comments and replace quoted strings with \"\" while keeping newlines."""
    out, i, n = [], 0, len(text)
    while i < n:
        c = text[i]
        if c == '"':
            i += 1
            while i < n:
                if text[i] == '\\':
                    i += 2
                    continue
                if text[i] == '"':
                    i += 1
                    break
                if text[i] == '\n':
                    out.append('\n')
                i += 1
            out.append('""')
            continue
        if c == '/' and i + 1 < n and text[i + 1] == '/':
            while i < n and text[i] != '\n':
                i += 1
            continue
        out.append(c)
        i += 1
    return ''.join(out)


def check(path):
    errs, warns = [], []
    text = strip_strings(open(path, encoding='utf-8').read())

    if text.count('{') + text.count('}') == 0:
        errs.append('no braces at all')
    depth = 0
    lineno = 1
    buf, buf_line, bracket = [], None, 0
    declared, endpoints, used = {}, set(), set()

    def flush(stmt, line):
        nonlocal depth
        stmt = stmt.strip()
        if not stmt:
            return
        if re.match(r'^(strict\s+)?(di)?graph\b', stmt):
            return
        if stmt.startswith('subgraph'):
            return
        if re.match(r'^(graph|node|edge)\s*\[', stmt):
            return
        body = stmt.rstrip(';{').strip()
        if not body:
            return
        if body.startswith('{'):
            body = body[1:].strip()
        if body.startswith('rank'):
            return
        if '->' in body:
            for seg in body.split('->'):
                name = seg.split('[')[0].strip()
                if name:
                    endpoints.add(name)
                    used.add(name)
            return
        m = re.match(r'^([A-Za-z_]\w*)\s*(\[[^]]*\])?$', body)
        if m:
            name = m.group(1)
            if name in KEYWORDS:
                return
            if name in declared:
                if m.group(2):  # only attribute statements count as redeclaration
                    warns.append('L%d duplicate node %s (first L%d)' % (line, name, declared[name]))
            else:
                declared[name] = line
            return
        # subgraph bodies may hold several statements; split on ';'
        for part in body.split(';'):
            part = part.strip()
            if part and re.match(r'^[A-Za-z_]\w*$', part) and part not in KEYWORDS:
                declared.setdefault(part, line)

    for ch in text:
        if ch == '\n':
            lineno += 1
        if ch == '{':
            depth += 1
        elif ch == '}':
            depth -= 1
            if depth < 0:
                errs.append('L%d brace depth negative' % lineno)
                depth = 0
        if ch == '[':
            bracket += 1
        elif ch == ']':
            bracket -= 1
            if bracket < 0:
                errs.append('L%d bracket depth negative' % lineno)
                bracket = 0
        if ch in '{}\n' and bracket == 0:
            flush(''.join(buf), buf_line or lineno)
            buf, buf_line = [], None
        elif ch == ';' and bracket == 0:
            flush(''.join(buf), buf_line or lineno)
            buf, buf_line = [], None
        else:
            if buf_line is None and not ch.isspace():
                buf_line = lineno
            buf.append(ch)

    if bracket != 0:
        errs.append('unbalanced [] at EOF (depth %d)' % bracket)
    if depth != 0:
        errs.append('unbalanced {} at EOF (depth %d)' % depth)

    for n in sorted(endpoints - set(declared)):
        errs.append('edge endpoint never declared: %s' % n)
    isolated = sorted(set(declared) - used)

    print('== %s' % path)
    print('   nodes declared: %d, edge endpoints: %d' % (len(declared), len(endpoints)))
    print('   declared but no edge: %s' % (', '.join(isolated) if isolated else '(none)'))
    for w in warns:
        print('   WARN %s' % w)
    for e in errs:
        print('   ERR  %s' % e)
    print('   RESULT: %s' % ('FAIL' if errs else 'PASS (structure only; render separately with dot -Tsvg)'))
    return not errs


ok = True
for p in sys.argv[1:]:
    ok = check(p) and ok
sys.exit(0 if ok else 1)
