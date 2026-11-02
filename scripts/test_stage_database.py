import importlib.util
from contextlib import closing
from pathlib import Path
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('migration', Path(__file__).with_name('stage-database.py'))
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)


class StagedMigrationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=Path(__file__).parent)
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source, self.ref, self.output = [self.root / name for name in ('source.db', 'reference.db', 'stage.db')]
        with closing(sqlite3.connect(self.source)) as db:
            db.executescript("CREATE TABLE nodes(id TEXT PRIMARY KEY,name TEXT,token_hash TEXT);"
                             "CREATE TABLE projects(id TEXT PRIMARY KEY,name TEXT,node_id TEXT);"
                             "INSERT INTO nodes VALUES('node-a','Exact device name','unchanged-token-hash');"
                             "INSERT INTO projects VALUES('project-a','Exact project name','node-a');"
                             "CREATE TABLE retained_custom_table(id TEXT, data BLOB);"
                             "INSERT INTO retained_custom_table VALUES('immutable',X'000102ff');")
        with closing(sqlite3.connect(self.ref)) as db:
            db.executescript("CREATE TABLE nodes(id TEXT PRIMARY KEY,name TEXT,token_hash TEXT,tenant_id TEXT NOT NULL DEFAULT 'owner');"
                             "CREATE TABLE projects(id TEXT PRIMARY KEY,name TEXT,node_id TEXT,tenant_id TEXT NOT NULL DEFAULT 'owner');"
                             "CREATE INDEX nodes_tenant_idx ON nodes(tenant_id);"
                             "CREATE TABLE user(id TEXT PRIMARY KEY,redx_account_id TEXT UNIQUE,password_hash TEXT);")

    def test_missing_ownership_is_blocked_without_assigning_owner(self):
        result = migration.stage(self.source, self.ref, self.output)
        self.assertFalse(result['ready'])
        self.assertTrue(result['originalDataPreserved'])
        with closing(sqlite3.connect(self.output)) as db:
            self.assertEqual(db.execute('SELECT tenant_id FROM nodes').fetchone()[0], '__unassigned__')
            self.assertEqual(db.execute('SELECT data FROM retained_custom_table').fetchone()[0], bytes([0, 1, 2, 255]))
        with closing(sqlite3.connect(self.source)) as db:
            self.assertNotIn('tenant_id', migration.columns(db, 'nodes'))

    def test_explicit_mapping_preserves_names_ids_hashes_and_relations(self):
        result = migration.stage(self.source, self.ref, self.output, {
            'nodes': {'node-a': 'account-123'}, 'projects': {'project-a': 'account-123'},
        })
        self.assertTrue(result['ready'])
        with closing(sqlite3.connect(self.output)) as db:
            self.assertEqual(db.execute('SELECT * FROM nodes').fetchone(), ('node-a', 'Exact device name', 'unchanged-token-hash', 'account-123'))
            self.assertEqual(db.execute('SELECT * FROM projects').fetchone(), ('project-a', 'Exact project name', 'node-a', 'account-123'))
            self.assertEqual(db.execute('SELECT count(*) FROM user').fetchone()[0], 0)

    def test_cross_tenant_device_assignment_blocks_release(self):
        result = migration.stage(self.source, self.ref, self.output, {
            'nodes': {'node-a': 'account-a'}, 'projects': {'project-a': 'account-b'},
        })
        self.assertFalse(result['ready'])
        self.assertTrue(any('relationship' in item for item in result['blockers']))

    def test_unknown_ids_are_rejected(self):
        with self.assertRaises(ValueError):
            migration.stage(self.source, self.ref, self.output, {'nodes': {'missing': 'account-a'}})

    def test_reference_with_data_is_rejected(self):
        with closing(sqlite3.connect(self.ref)) as db:
            db.execute("INSERT INTO user VALUES('reference-user','subject','hash')")
            db.commit()
        with self.assertRaises(ValueError):
            migration.stage(self.source, self.ref, self.output)

    def test_credential_ids_are_preserved_and_incompatible_namespace_blocks_release(self):
        with closing(sqlite3.connect(self.source)) as db:
            db.executescript("CREATE TABLE credentials(id TEXT PRIMARY KEY,secret_enc TEXT);"
                             "INSERT INTO credentials VALUES('github','unchanged-ciphertext');")
        with closing(sqlite3.connect(self.ref)) as db:
            db.executescript("CREATE TABLE credentials(id TEXT PRIMARY KEY,secret_enc TEXT,tenant_id TEXT NOT NULL DEFAULT 'owner');")
        result = migration.stage(self.source, self.ref, self.output, {
            'nodes': {'node-a': 'account-a'}, 'projects': {'project-a': 'account-a'},
            'credentials': {'github': 'account-a'},
        })
        self.assertFalse(result['ready'])
        self.assertIn('Legacy credential namespace requires compatibility review', result['blockers'])
        with closing(sqlite3.connect(self.output)) as db:
            self.assertEqual(db.execute('SELECT id,secret_enc FROM credentials').fetchone(), ('github', 'unchanged-ciphertext'))

    def test_source_and_existing_output_cannot_be_overwritten(self):
        with self.assertRaises(ValueError):
            migration.stage(self.source, self.ref, self.source)
        self.output.write_text('keep')
        with self.assertRaises(FileExistsError):
            migration.stage(self.source, self.ref, self.output)
        self.assertEqual(self.output.read_text(), 'keep')

    def test_wal_commits_are_copied(self):
        db = sqlite3.connect(self.source)
        self.addCleanup(db.close)
        db.execute('PRAGMA journal_mode=WAL')
        db.execute("INSERT INTO nodes VALUES('node-wal','Committed in WAL','token-wal')")
        db.commit()
        migration.stage(self.source, self.ref, self.output)
        with closing(sqlite3.connect(self.output)) as restored:
            self.assertEqual(restored.execute('SELECT count(*) FROM nodes').fetchone()[0], 2)
            self.assertEqual(restored.execute('PRAGMA integrity_check').fetchall(), [('ok',)])

    def test_existing_auth_and_tenant_records_are_preserved(self):
        with closing(sqlite3.connect(self.source)) as db:
            db.executescript("CREATE TABLE user(id TEXT PRIMARY KEY,redx_account_id TEXT UNIQUE,password_hash TEXT);"
                             "INSERT INTO user VALUES('local-id','immutable-subject','existing-hash');"
                             "ALTER TABLE nodes ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'existing-account';")
        with self.assertRaises(ValueError):
            migration.stage(self.source, self.ref, self.output, {'nodes': {'node-a': 'someone-else'}})


if __name__ == '__main__':
    unittest.main()
