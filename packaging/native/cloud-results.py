#!/usr/bin/env python3
"""Summarize a pinned RELEASE report without executing or exposing log contents."""
import argparse
import json
import math
import os
from pathlib import Path
import re
import stat

LIMIT = 32 << 20


def reject_constant(value):
    raise ValueError('non-finite JSON constant')


def summary(raw, expected_sha):
    if not re.fullmatch('[0-9a-f]{40}', expected_sha):
        raise ValueError('expected_sha must be a full commit SHA')
    if len(raw) > LIMIT:
        raise ValueError('report exceeds budget')
    plan = None
    checks = []
    failed = set()
    for line in raw.decode('utf-8', errors='replace').splitlines():
        try:
            item = json.loads(line, parse_constant=reject_constant)
        except ValueError:
            continue
        if not isinstance(item, dict):
            continue
        if item.get('tier') == 'RELEASE' and 'steps' in item:
            if plan is not None:
                raise ValueError('multiple release plans')
            plan = item
        if 'argv' in item and 'exit' in item:
            checks.append(item)
        if item.get('Action') == 'fail':
            name = str(item.get('Package', '')) + ':' + str(item.get('Test', ''))
            failed.add(re.sub('[^A-Za-z0-9_/.:-]', '_', name)[:256])
    if plan is None or plan.get('head') != expected_sha or plan.get('dirty') is not False:
        raise ValueError('missing, dirty or mismatched source identity')
    steps = plan.get('steps')
    if not isinstance(steps, list) or not steps:
        raise ValueError('missing release checks')
    codes = [item.get('exit') for item in checks]
    if any(type(code) is not int for code in codes):
        raise ValueError('invalid exit code')
    planned = [item.get('argv') for item in steps if isinstance(item, dict)]
    observed = [item.get('argv') for item in checks]
    if any(not isinstance(argv, list) or not argv or
           any(not isinstance(arg, str) or not arg for arg in argv)
           for argv in planned + observed):
        raise ValueError('invalid check command')
    # Reports are observations; the provider run status remains separate authority.
    state = 'failed' if failed or any(codes) else 'unknown'
    if state != 'failed' and len(planned) == len(steps) and observed == planned:
        state = 'passed'
    seconds = [item.get('seconds') for item in checks]
    elapsed = sum(seconds) if all(type(x) in (int, float) and math.isfinite(x) and 0 <= x <= 3600 for x in seconds) else None
    return {'schema': 'mcpshka.cloud-results/v1', 'trust': 'UNTRUSTED',
            'source_sha': expected_sha, 'state': state,
            'planned_checks': len(steps), 'observed_checks': len(checks),
            'exit_codes': codes, 'seconds': elapsed,
            'failed_tests': sorted(failed), 'deploy_authorized': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--log', required=True)
    parser.add_argument('--expected-sha', required=True)
    args = parser.parse_args()
    fd = os.open(args.log, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size > LIMIT:
            raise ValueError('not a bounded regular report')
        result = summary(stream.read(LIMIT + 1), args.expected_sha)
    print(json.dumps(result, allow_nan=False))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError):
        print(json.dumps({'state': 'unknown', 'error': 'report verification failed', 'deploy_authorized': False}))
        raise SystemExit(1)
