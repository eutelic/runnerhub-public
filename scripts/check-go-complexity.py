#!/usr/bin/env python3
"""Reject new functions above 30 and increases in measured legacy debt (#165)."""
import json
from pathlib import Path
import sys


def check(lines, baseline):
    violations = []
    count = 0
    for line in lines:
        if not line.strip():
            continue
        score, package, function, location = line.split(maxsplit=3)
        path, line_number, column = location.rsplit(':', 2)
        int(line_number), int(column)  # Fail closed on malformed scanner output.
        score = int(score)
        key = f'{package} {function} {path}'
        limit = baseline.get(key, 30)
        if score > limit:
            violations.append(f'{key}: complexity {score} exceeds {limit}')
        count += 1
    if not count:
        raise ValueError('complexity scanner produced no functions')
    return violations


def main():
    baseline = json.loads(Path(__file__).with_name('go-complexity-baseline.json').read_text())
    violations = check(sys.stdin, baseline)
    if violations:
        print('\n'.join(violations), file=sys.stderr)
        return 1
    print('Production complexity matches or improves the measured baseline.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
