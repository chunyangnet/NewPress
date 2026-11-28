#!/usr/bin/env python3
"""Run a deterministic slice of a large Go package, including all subtests."""
import argparse
import re
import subprocess


def partition(names, index, total):
    if total < 1 or not 0 <= index < total:
        raise ValueError('invalid shard index/count')
    ordered = sorted(set(names))
    if len(ordered) != len(names):
        raise ValueError('duplicate Go test names')
    selected = ordered[index::total]
    if not selected:
        raise ValueError('empty shard; reduce the configured shard count')
    return selected


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--package', choices=('./internal/httpserver', './internal/content', './internal/pluginjobs'), required=True)
    parser.add_argument('--index', type=int, required=True)
    parser.add_argument('--total', type=int, required=True)
    args = parser.parse_args()
    output = subprocess.check_output(['go', 'test', '-race', '-cover', '-list', '^(Test|Example|Fuzz)', args.package], text=True)
    names = [line for line in output.splitlines() if line.startswith(('Test', 'Example', 'Fuzz')) and line.isidentifier()]
    selected = partition(names, args.index, args.total)
    print(f'{args.package}: shard {args.index + 1}/{args.total}, {len(selected)} of {len(names)} top-level tests; all matching subtests included.', flush=True)
    pattern = '^(' + '|'.join(re.escape(name) for name in selected) + ')$'
    coverage = f'coverage-{args.package.rsplit("/", 1)[-1]}-{args.index}.out'
    subprocess.run(['go', 'test', '-race', '-timeout=35m', '-coverprofile=' + coverage,
                    '-run', pattern, args.package], check=True)


if __name__ == '__main__':
    main()
