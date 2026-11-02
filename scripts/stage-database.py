#!/usr/bin/env python3
"""Additive Host migration on a NEW SQLite copy; never modifies the source.

The reference is an empty database created from this release's Drizzle schema.
Ownership must be explicitly mapped by immutable row ID for legacy rows. No
account, password, provider credential, resource ID or original field is replaced.
This tool does not migrate RedX engine files or authenticate ownership assertions.
"""
import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3


def quote(value):
    return '"' + value.replace('"', '""') + '"'


def readonly(path):
    return sqlite3.connect(Path(path).resolve().as_uri() + '?mode=ro', uri=True)


def tables(db):
    return dict(db.execute("SELECT name,sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"))


def columns(db, table):
    return {row[1]: row for row in db.execute('PRAGMA table_info(' + quote(table) + ')')}


def fingerprint(db, table, names):
    # Compare every original field, including ciphertext, without logging it.
    fields = ','.join(map(quote, names))
    digest = hashlib.sha256()
    count = 0
    for row in db.execute('SELECT ' + fields + ' FROM ' + quote(table) + ' ORDER BY ' + fields):
        digest.update(repr(row).encode('utf-8'))
        digest.update(b'\n')
        count += 1
    return count, digest.hexdigest()


def stage(source, reference, output, ownership=None):
    output = Path(output).resolve()
    if output in (Path(source).resolve(), Path(reference).resolve()):
        raise ValueError('Output must be a new file, separate from both inputs.')
    # Exclusive creation also rejects existing files and symlinks.
    fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(fd)
    mapping = ownership or {}
    report = {'ready': False, 'tables': {}, 'blockers': []}
    with closing(readonly(source)) as src, closing(readonly(reference)) as ref, closing(sqlite3.connect(output)) as dst:
        src.backup(dst)
        original = {t: (list(columns(dst, t)), None) for t in tables(dst)}
        original = {t: (names, fingerprint(dst, t, names)) for t, (names, _) in original.items()}
        target = tables(ref)
        if any(ref.execute('SELECT count(*) FROM ' + quote(t)).fetchone()[0] for t in target):
            raise ValueError('Reference must contain schema only, without account or application records.')
        for table in mapping:
            if table not in target or 'tenant_id' not in columns(ref, table):
                raise ValueError('Ownership manifest contains an unknown table.')
        with dst:
            for table, create_sql in target.items():
                if table not in tables(dst):
                    dst.execute(create_sql)
                old_columns = columns(dst, table)
                for name, col in columns(ref, table).items():
                    if name in old_columns:
                        continue
                    _, _, declared_type, required, default, primary = col
                    if primary or name not in ('tenant_id', 'redx_account_id'):
                        raise ValueError('Unsupported legacy schema; additive review required for ' + table + '.' + name)
                    statement = quote(name) + ' ' + declared_type
                    if name == 'tenant_id':
                        # Never silently turn historical rows into owner data.
                        statement += " NOT NULL DEFAULT '__unassigned__'"
                    elif required:
                        raise ValueError('Unsupported required identity column.')
                    dst.execute('ALTER TABLE ' + quote(table) + ' ADD COLUMN ' + statement)
                if 'tenant_id' not in columns(ref, table):
                    continue
                assignments = mapping.get(table, {})
                if not isinstance(assignments, dict):
                    raise ValueError('Ownership table entries must map immutable row IDs to account subjects.')
                seen = set()
                for row_id, tenant in dst.execute('SELECT id,tenant_id FROM ' + quote(table)).fetchall():
                    seen.add(row_id)
                    assigned = assignments.get(row_id)
                    if assigned is not None:
                        if not isinstance(assigned, str) or not re.fullmatch(r'[A-Za-z0-9._:@-]{1,256}', assigned) or assigned == '__unassigned__':
                            raise ValueError('Invalid account subject in ownership manifest.')
                        if tenant != '__unassigned__' and assigned != tenant:
                            raise ValueError('Refusing to change an existing tenant assignment.')
                        dst.execute('UPDATE ' + quote(table) + ' SET tenant_id=? WHERE id=?', (assigned, row_id))
                if set(assignments) - seen:
                    raise ValueError('Ownership manifest contains an unknown row ID.')
                missing = dst.execute('SELECT count(*) FROM ' + quote(table) + " WHERE tenant_id IS NULL OR tenant_id='' OR tenant_id='__unassigned__'").fetchone()[0]
                report['tables'][table] = {'rows': len(seen), 'unassigned': missing}
                if missing:
                    report['blockers'].append(table + ': explicit ownership required')
            for (index_sql,) in ref.execute("SELECT sql FROM sqlite_master WHERE type='index' AND sql IS NOT NULL"):
                dst.execute(index_sql.replace('CREATE UNIQUE INDEX ', 'CREATE UNIQUE INDEX IF NOT EXISTS ', 1).replace('CREATE INDEX ', 'CREATE INDEX IF NOT EXISTS ', 1))
            # Keep all original fields and all original rows exactly as stored.
            for table, (names, expected) in original.items():
                if fingerprint(dst, table, names) != expected:
                    raise ValueError('Original data verification failed for ' + table)
            for child, key, parent in (
                ('projects', 'node_id', 'nodes'), ('deployments', 'project_id', 'projects'),
                ('deployments', 'node_id', 'nodes'), ('project_replicas', 'project_id', 'projects'),
                ('project_replicas', 'node_id', 'nodes'), ('domains', 'project_id', 'projects'),
                ('usage_events', 'project_id', 'projects'), ('usage_events', 'node_id', 'nodes'),
                ('app_builds', 'project_id', 'projects'),
            ):
                if child not in target or parent not in target:
                    continue
                mismatch = dst.execute('SELECT count(*) FROM ' + quote(child) + ' c LEFT JOIN ' + quote(parent) + ' p ON c.' + quote(key) + '=p.id WHERE c.' + quote(key) + ' IS NOT NULL AND (p.id IS NULL OR c.tenant_id<>p.tenant_id)').fetchone()[0]
                if mismatch:
                    report['blockers'].append(child + ': resource ownership relationship requires review')
            if dst.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
                raise ValueError('Staged database integrity check failed.')
            if dst.execute('PRAGMA foreign_key_check').fetchall():
                report['blockers'].append('Foreign-key relationships require review')
            if 'credentials' in target:
                for credential_id, tenant in dst.execute('SELECT id,tenant_id FROM credentials'):
                    if tenant not in ('owner', '__unassigned__') and (not isinstance(credential_id, str) or not credential_id.startswith(tenant + ':')):
                        report['blockers'].append('Legacy credential namespace requires compatibility review')
                        break
            if 'projects' in target and 'bundle_key' in columns(dst, 'projects'):
                for key, tenant in dst.execute('SELECT bundle_key,tenant_id FROM projects WHERE bundle_key IS NOT NULL'):
                    segment = 'owner' if tenant == 'owner' else hashlib.sha256(tenant.encode()).hexdigest()[:24]
                    if not re.fullmatch(r'bundles/[a-z0-9_-]{1,64}/[A-Za-z0-9._-]{1,220}', key) or not key.startswith('bundles/' + segment + '/'):
                        report['blockers'].append('Legacy bundle namespace requires compatibility review')
                        break
        report['ready'] = not report['blockers']
        report['originalDataPreserved'] = True
    return report


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ('source', 'reference', 'output'):
        parser.add_argument('--' + flag, required=True)
    parser.add_argument('--ownership', help='Private JSON map: table -> immutable row ID -> verified account subject')
    args = parser.parse_args()
    try:
        manifest = json.loads(Path(args.ownership).read_text()) if args.ownership else None
        result = stage(args.source, args.reference, args.output, manifest)
        print(json.dumps(result, indent=2))
        raise SystemExit(0 if result['ready'] else 2)
    except (ValueError, sqlite3.Error, OSError) as error:
        # SQLite errors may contain data values; do not echo their text.
        print(json.dumps({'ready': False, 'error': type(error).__name__, 'message': str(error) if isinstance(error, ValueError) else 'Staging failed; inspect inputs securely.'}))
        raise SystemExit(1)
