"""SQLite-backed transactional document store with revision checks and scoped keys.

Local-first preview: one service/OS account. This is not a distributed database.
"""
from __future__ import annotations
import base64
import hashlib
import hmac
import json
import os
import re
import sqlite3
import time
import uuid
from contextlib import contextmanager
from pathlib import Path
from typing import Any
from .language import Box, Document, Packer, Parser, Value, empty_source, format_document, parse
from .security import OWNER_EMAIL, PASSWORDS, Vault, digest, private_file, token, validate_registration, verify_password


class StoreError(ValueError):
    status = 400

class Unauthorized(StoreError):
    status = 401

class Forbidden(StoreError):
    status = 403

class Missing(StoreError):
    status = 404

class Conflict(StoreError):
    status = 409


def data_root() -> Path:
    if os.environ.get('REDXAI_DATA_DIR'):
        return Path(os.environ['REDXAI_DATA_DIR']).expanduser().resolve()
    if os.name == 'nt':
        return Path(os.environ.get('LOCALAPPDATA', str(Path.home()))) / 'Red-XAI'
    import sys
    if sys.platform == 'darwin':
        return Path.home() / 'Library' / 'Application Support' / 'Red-XAI'
    return Path(os.environ.get('XDG_DATA_HOME', str(Path.home() / '.local' / 'share'))) / 'Red-XAI'


def normalize_filename(name: str) -> str:
    if not isinstance(name, str) or '/' in name or '\\' in name or '\x00' in name:
        raise StoreError('Database name must be a filename, not a path')
    if name.lower().endswith('.red-xai'):
        name = name[:-8]
    elif '.' in name:
        name = name.rsplit('.', 1)[0]
    name = name.strip()
    if not re.fullmatch(r'[\w][\w .-]{0,79}', name, re.UNICODE) or name in ('.', '..'):
        raise StoreError('Name must contain 1–80 letters, digits, spaces, dots, underscores, or dashes')
    if name.upper().split('.')[0] in {'CON','PRN','AUX','NUL',*[f'COM{i}' for i in range(1,10)],*[f'LPT{i}' for i in range(1,10)]}:
        raise StoreError('Reserved system filename')
    return name + '.Red-XAI'


def read_literal(source: str) -> Value:
    if not isinstance(source, str):
        raise StoreError('value_source must be Red-XAI text')
    parser = Parser(source); value = parser.value(0); parser.take('EOF'); return value


def _json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':'))


class ClosingConnection(sqlite3.Connection):
    def __exit__(self, *args):
        try:
            return super().__exit__(*args)
        finally:
            self.close()


class Store:
    def __init__(self, root: Path | str | None = None):
        self.root = Path(root) if root is not None else data_root()
        self.vault = Vault(self.root)
        self.path = self.root / 'redxai.sqlite3'
        self.setup_file = self.root / 'setup.token'
        try:
            private_file(self.setup_file, token('rxsetup_').encode())
        except FileExistsError:
            pass
        with self.connect() as db:
            db.executescript('''
            PRAGMA journal_mode=WAL;
            CREATE TABLE IF NOT EXISTS users(
                id TEXT PRIMARY KEY, username TEXT UNIQUE COLLATE NOCASE,
                email TEXT UNIQUE COLLATE NOCASE, password_hash TEXT NOT NULL,
                role TEXT NOT NULL, verified INTEGER NOT NULL DEFAULT 0,
                created REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS credentials(
                hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id),
                db_id TEXT, kind TEXT NOT NULL, permissions TEXT NOT NULL,
                box_path TEXT, revision INTEGER, expires REAL NOT NULL,
                key_ref TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);
            CREATE TABLE IF NOT EXISTS databases(
                id TEXT PRIMARY KEY, owner_id TEXT NOT NULL REFERENCES users(id),
                project TEXT NOT NULL, name TEXT NOT NULL, revision INTEGER NOT NULL,
                source BLOB NOT NULL, deleted INTEGER NOT NULL DEFAULT 0,
                created REAL NOT NULL, updated REAL NOT NULL,
                UNIQUE(owner_id, project, name));
            CREATE TABLE IF NOT EXISTS history(
                db_id TEXT NOT NULL REFERENCES databases(id), revision INTEGER NOT NULL,
                source BLOB NOT NULL, created REAL NOT NULL,
                PRIMARY KEY(db_id, revision));
            CREATE TABLE IF NOT EXISTS audit(
                sequence INTEGER PRIMARY KEY AUTOINCREMENT, at REAL NOT NULL,
                actor TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL,
                detail TEXT NOT NULL, previous TEXT NOT NULL, hash TEXT NOT NULL);
            ''')
        if os.name != 'nt': self.path.chmod(0o600)

    def connect(self) -> sqlite3.Connection:
        db = sqlite3.connect(self.path, timeout=15, factory=ClosingConnection)
        db.row_factory = sqlite3.Row
        db.execute('PRAGMA foreign_keys=ON'); db.execute('PRAGMA busy_timeout=15000')
        return db

    @contextmanager
    def transaction(self):
        db = self.connect()
        try:
            db.execute('BEGIN IMMEDIATE')
            yield db
            db.commit()
        except BaseException:
            db.rollback(); raise
        finally:
            db.close()

    def audit(self, db, actor: str, action: str, target: str, detail: Any = None):
        previous = db.execute('SELECT hash FROM audit ORDER BY sequence DESC LIMIT 1').fetchone()
        previous = previous[0] if previous else '0' * 64
        now, detail = time.time(), _json(detail)
        checksum = digest(_json([now, actor, action, target, detail, previous]))
        db.execute('INSERT INTO audit(at,actor,action,target,detail,previous,hash) VALUES(?,?,?,?,?,?,?)',
                   (now, actor, action, target, detail, previous, checksum))

    def setup_needed(self) -> bool:
        with self.connect() as db:
            return not db.execute("SELECT 1 FROM users WHERE role='owner'").fetchone()

    def bootstrap(self, setup_token: str, registration: dict) -> dict:
        email = validate_registration(**registration)
        if email != OWNER_EMAIL:
            raise Forbidden('This local installation is reserved for the configured owner email')
        if not hmac.compare_digest(setup_token, self.setup_file.read_text()):
            raise Forbidden('Local first-run enrollment token is invalid')
        encoded = PASSWORDS.hash(registration['password'])
        with self.transaction() as db:
            if db.execute("SELECT 1 FROM users WHERE role='owner'").fetchone():
                raise Conflict('Owner already enrolled; sign in instead')
            uid = uuid.uuid4().hex
            db.execute('INSERT INTO users VALUES(?,?,?,?,?,?,?)',
                       (uid, registration['username'], email, encoded, 'owner', 1, time.time()))
            self.audit(db, uid, 'owner.enrolled', uid, {'scope': 'local-installation', 'email_verified': False})
        return self.login(email, registration['password'])

    def register(self, registration: dict) -> dict:
        email = validate_registration(**registration)
        if self.setup_needed():
            raise Forbidden('Enroll the installation owner first')
        encoded = PASSWORDS.hash(registration['password'])
        with self.transaction() as db:
            uid = uuid.uuid4().hex
            try:
                db.execute('INSERT INTO users VALUES(?,?,?,?,?,?,?)',
                           (uid, registration['username'], email, encoded, 'member', 0, time.time()))
            except sqlite3.IntegrityError as exc:
                raise Conflict('Username or email already registered') from exc
            self.audit(db, uid, 'registration.pending', uid)
        return {'id': uid, 'status': 'pending_local_owner_approval',
                'email_delivery': 'not_configured', 'email_verified': False}

    def login(self, email: str, password: str) -> dict:
        if not isinstance(email, str) or not isinstance(password, str):
            raise Unauthorized('Invalid credentials')
        with self.transaction() as db:
            user = db.execute('SELECT * FROM users WHERE email=? COLLATE NOCASE', (email.strip(),)).fetchone()
            if not user or not verify_password(user['password_hash'], password):
                raise Unauthorized('Invalid credentials')
            if not user['verified']:
                raise Forbidden('Account awaits local owner approval; email verification is not configured')
            secret, ref = token('rxsession_'), 'session:' + uuid.uuid4().hex
            db.execute('INSERT INTO credentials VALUES(?,?,?,?,?,?,?,?,?,0)',
                       (digest(secret), user['id'], None, 'session', 'read,write,admin', None, None,
                        time.time() + 8 * 3600, ref))
            self.audit(db, user['id'], 'session.created', user['id'])
            return {'token': secret, 'expires_in': 8 * 3600,
                    'user': {'id': user['id'], 'username': user['username'], 'email': user['email'], 'role': user['role']}}

    def principal(self, bearer: str) -> dict:
        if not isinstance(bearer, str) or not 20 <= len(bearer) <= 512:
            raise Unauthorized('Authentication required')
        with self.connect() as db:
            row = db.execute('SELECT c.*,u.role,u.username,u.email FROM credentials c JOIN users u ON u.id=c.user_id WHERE c.hash=? AND c.revoked=0 AND c.expires>?',
                             (digest(bearer), time.time())).fetchone()
            if not row: raise Unauthorized('Credential invalid or expired')
            return dict(row)

    @staticmethod
    def session(principal: dict):
        if principal['kind'] != 'session': raise Forbidden('User sign-in required for this operation')

    def authorize(self, principal: dict, row, permission='read'):
        if row is None or row['deleted']: raise Missing('Database not found')
        if row['owner_id'] != principal['user_id']: raise Forbidden('Database belongs to another account')
        if permission not in principal['permissions'].split(','): raise Forbidden('Permission denied')
        if principal['kind'] != 'session' and principal['db_id'] != row['id']:
            raise Forbidden('This key is scoped to a different database')
        if principal['box_path'] is not None and principal['revision'] != row['revision']:
            raise Forbidden('Box access grant expired after a database edit; regenerate it')

    def _source(self, row):
        return self.vault.decrypt(row['source'], f"{row['id']}:{row['revision']}")

    def list_databases(self, p: dict) -> list[dict]:
        self.session(p)
        with self.connect() as db:
            return [dict(r) for r in db.execute('SELECT id,project,name,revision,created,updated FROM databases WHERE owner_id=? AND deleted=0 ORDER BY name', (p['user_id'],))]

    def create_database(self, p: dict, name: str, project: str = 'Default') -> dict:
        self.session(p); name = normalize_filename(name)
        if not isinstance(project, str) or not 1 <= len(project.strip()) <= 100:
            raise StoreError('Project must contain 1–100 characters')
        uid, ref, secret, now = uuid.uuid4().hex, 'keyref:' + uuid.uuid4().hex, token('rxdb_'), time.time()
        source = empty_source(ref)
        cipher = self.vault.encrypt(source, f'{uid}:1')
        with self.transaction() as db:
            try:
                db.execute('INSERT INTO databases VALUES(?,?,?,?,?,?,0,?,?)',
                           (uid, p['user_id'], project.strip(), name, 1, cipher, now, now))
            except sqlite3.IntegrityError as exc: raise Conflict('A database with this name already exists in the project') from exc
            db.execute('INSERT INTO history VALUES(?,?,?,?)', (uid, 1, cipher, now))
            db.execute('INSERT INTO credentials VALUES(?,?,?,?,?,?,?,?,?,0)',
                       (digest(secret), p['user_id'], uid, 'api', 'read,write', None, None, now + 90 * 86400, ref))
            self.audit(db, p['user_id'], 'database.created', uid, {'name': name, 'project': project})
        return {'id': uid, 'name': name, 'revision': 1, 'source': source,
                'api_key': secret, 'key_ref': ref, 'expires_in_days': 90}

    def read_database(self, p: dict, uid: str, include_source=True) -> dict:
        with self.connect() as db:
            row = db.execute('SELECT * FROM databases WHERE id=?', (uid,)).fetchone()
            self.authorize(p, row)
            source = self._source(row); document = parse(source); index = document.index()
            result = {k: row[k] for k in ('id','name','project','revision','created','updated')}
            if p['box_path'] is not None:
                path = json.loads(p['box_path'])
                index = [r for r in index if r['path'][:len(path)] == path]
                include_source = False
            result['objects'] = index
            if include_source: result['source'] = source
            return result

    def _check_project_ids(self, db, current, document: Document):
        """Compare public identities with the owner's other databases in this project."""
        def identities(doc: Document):
            result = set()
            for row in doc.index():
                if row['kind'] == 'box' and row['path']:
                    for scope in ('local_id', 'global_id'):
                        value = row[scope]
                        if value is not None: result.add(('box', scope, type(value).__name__, value))
                if row['kind'] == 'packer' and row['global_id'] is not None:
                    value = row['global_id']; result.add(('packer', row['name'], type(value).__name__, value))
            return result
        own = identities(document)
        for other in db.execute('SELECT * FROM databases WHERE owner_id=? AND project=? AND id<>? AND deleted=0',
                                (current['owner_id'], current['project'], current['id'])):
            if own.intersection(identities(parse(self._source(other)))):
                raise Conflict('Project-wide box or Packer global identity conflicts with another database')

    def save_source(self, p: dict, uid: str, source: str, revision: int) -> dict:
        document = parse(source)
        with self.transaction() as db:
            row = db.execute('SELECT * FROM databases WHERE id=?', (uid,)).fetchone()
            self.authorize(p, row, 'write')
            if p['box_path'] is not None: raise Forbidden('Box-scoped keys cannot replace the whole database')
            if type(revision) is not int or row['revision'] != revision:
                raise Conflict('Revision changed; reload and reconcile before saving')
            self._check_project_ids(db, row, document)
            new_revision, now = revision + 1, time.time()
            cipher = self.vault.encrypt(source, f'{uid}:{new_revision}')
            db.execute('UPDATE databases SET source=?,revision=?,updated=? WHERE id=?', (cipher, new_revision, now, uid))
            db.execute('INSERT INTO history VALUES(?,?,?,?)', (uid, new_revision, cipher, now))
            self.audit(db, p['user_id'], 'database.saved', uid, {'revision': new_revision, 'source_sha256': digest(source)})
        return {'id': uid, 'revision': new_revision}

    def edit(self, p: dict, uid: str, change: dict) -> dict:
        current = self.read_database(p, uid)
        if p['box_path'] is not None: raise Forbidden('Box-scoped preview tokens are read-only')
        if change.get('revision') != current['revision']: raise Conflict('Revision changed')
        doc = parse(current['source']); path = change.get('path', []); node = doc.node(path)
        operation = change.get('operation')
        if operation == 'remove':
            if not path: raise Forbidden('Cannot remove the required root')
            parent = doc.node(path[:-1]); assert isinstance(parent, Box)
            del parent.members[path[-1]]
        elif operation in ('add_box', 'add_packer'):
            if not isinstance(node, Box): raise StoreError('Add target must be a box')
            name = change.get('name')
            if not isinstance(name, str) or not 1 <= len(name) <= 256: raise StoreError('Name required')
            local, global_ = change.get('local_id'), change.get('global_id')
            for value in (local, global_):
                if value is not None and (type(value) not in (str, int) or value == ''):
                    raise StoreError('IDs must be integer, non-empty string, or null')
            if operation == 'add_box':
                node.members.append(Box(name, local, global_id=global_))
            else:
                node.members.append(Packer(name, local, read_literal(change.get('value_source', 'NELL')), global_))
        elif operation == 'set_value':
            if not isinstance(node, Packer): raise StoreError('Value target must be a Packer')
            node.value = read_literal(change['value_source'])
        elif operation in ('array_insert','array_remove','table_set','table_remove','set_metadata'):
            if not isinstance(node, Packer): raise StoreError('Collection target must be a Packer')
            value = node.value
            for step in change.get('value_path', []):
                if value.kind == 'array' and type(step) is int and 1 <= step <= len(value.data):
                    value = value.data[step - 1]
                elif value.kind == 'table':
                    key = read_literal(step) if isinstance(step, str) else None
                    found = [v for k, v in value.data if key and k.kind == key.kind and k.data == key.data]
                    if len(found) != 1: raise Missing('Nested table key not found')
                    value = found[0]
                elif value.kind == 'meta' and step in ('table','metadata'):
                    value = value.data[0 if step == 'table' else 1]
                else: raise Missing('Nested value path not found')
            if operation.startswith('array_'):
                if value.kind != 'array': raise StoreError('Target is not an array')
                index = change.get('index')
                upper = len(value.data) + (1 if operation == 'array_insert' else 0)
                if type(index) is not int or not 1 <= index <= upper: raise StoreError('Array indexes are one-based')
                if operation == 'array_insert': value.data.insert(index - 1, read_literal(change['value_source']))
                else: del value.data[index - 1]
            elif operation.startswith('table_'):
                if value.kind != 'table': raise StoreError('Target is not a table')
                key = read_literal(change['key_source'])
                if key.kind not in ('string','number'): raise StoreError('Invalid table key')
                def eq(k):
                    if k.kind != key.kind: return False
                    if k.kind == 'number':
                        from decimal import Decimal
                        return Decimal(k.data) == Decimal(key.data)
                    return k.data == key.data
                found = next((i for i,(k,_) in enumerate(value.data) if eq(k)), None)
                if operation == 'table_remove':
                    if found is None: raise Missing('Table key not found')
                    del value.data[found]
                elif found is None: value.data.append((key, read_literal(change['value_source'])))
                else: value.data[found] = (key, read_literal(change['value_source']))
            else:
                if value.kind != 'meta': raise StoreError('Target is not a metatable')
                metadata = read_literal(change['value_source'])
                if metadata.kind != 'table': raise StoreError('Metadata must be a table')
                value.data = (value.data[0], metadata)
        else:
            raise StoreError('Unsupported edit operation')
        return self.save_source(p, uid, format_document(doc), current['revision'])

    def mint_key(self, p: dict, uid: str, read_only=False, box_path=None, days=30) -> dict:
        self.session(p)
        if type(days) is not int or not 1 <= days <= 90: raise StoreError('Expiry must be 1–90 days')
        with self.transaction() as db:
            row = db.execute('SELECT * FROM databases WHERE id=?', (uid,)).fetchone()
            self.authorize(p, row, 'write')
            if box_path is not None:
                if not isinstance(parse(self._source(row)).node(box_path), Box): raise StoreError('Grant target must be a box')
                read_only = True
            secret, ref = token('rxaccess_' if box_path is not None else 'rxdb_'), ('tokenref:' if box_path is not None else 'keyref:') + uuid.uuid4().hex
            db.execute('INSERT INTO credentials VALUES(?,?,?,?,?,?,?,?,?,0)',
                       (digest(secret), p['user_id'], uid, 'access' if box_path is not None else 'api',
                        'read' if read_only else 'read,write', _json(box_path) if box_path is not None else None,
                        row['revision'] if box_path is not None else None, time.time()+days*86400, ref))
            self.audit(db, p['user_id'], 'key.created', uid, {'reference': ref, 'read_only': bool(read_only)})
        return {'key': secret, 'reference': ref, 'expires_in_days': days,
                'read_only': bool(read_only), 'invalidated_by_edits': box_path is not None}

    def revoke_key(self, p, reference):
        self.session(p)
        with self.transaction() as db:
            count = db.execute('UPDATE credentials SET revoked=1 WHERE key_ref=? AND user_id=?', (reference, p['user_id'])).rowcount
            if not count: raise Missing('Key reference not found')
            self.audit(db, p['user_id'], 'key.revoked', reference)
        return {'revoked': True}

    def history(self, p, uid):
        self.session(p)
        with self.connect() as db:
            self.authorize(p, db.execute('SELECT * FROM databases WHERE id=?', (uid,)).fetchone())
            return [dict(r) for r in db.execute('SELECT revision,created FROM history WHERE db_id=? ORDER BY revision DESC LIMIT 100', (uid,))]

    def restore(self, p, uid, target_revision: int, current_revision: int):
        self.session(p)
        with self.connect() as db:
            self.authorize(p, db.execute('SELECT * FROM databases WHERE id=?', (uid,)).fetchone(), 'write')
            row = db.execute('SELECT source FROM history WHERE db_id=? AND revision=?', (uid,target_revision)).fetchone()
            if not row: raise Missing('Snapshot not found')
            source = self.vault.decrypt(row[0], f'{uid}:{target_revision}')
        return self.save_source(p, uid, source, current_revision)

    def delete(self, p, uid, revision: int):
        self.session(p)
        with self.transaction() as db:
            row = db.execute('SELECT * FROM databases WHERE id=?', (uid,)).fetchone(); self.authorize(p, row, 'write')
            if row['revision'] != revision: raise Conflict('Revision changed')
            db.execute('UPDATE databases SET deleted=1 WHERE id=?',(uid,))
            db.execute('UPDATE credentials SET revoked=1 WHERE db_id=?',(uid,))
            self.audit(db, p['user_id'], 'database.soft_deleted', uid)
        return {'deleted': True, 'retained_history': True, 'permanent_erasure': False}

    def owner_dashboard(self, p):
        self.session(p)
        if p['role'] != 'owner': raise Forbidden('Local installation owner required')
        with self.connect() as db:
            return {'scope': 'this_local_installation_only',
                    'users': [dict(r) for r in db.execute('SELECT id,username,email,role,verified AS local_approved,0 AS email_verified,created FROM users ORDER BY created DESC')],
                    'databases': [dict(r) for r in db.execute('SELECT id,owner_id,project,name,revision,deleted,updated FROM databases ORDER BY updated DESC')],
                    'events': [dict(r) for r in db.execute('SELECT sequence,at,actor,action,target,detail,hash FROM audit ORDER BY sequence DESC LIMIT 100')],
                    'integrity': db.execute('PRAGMA quick_check').fetchone()[0],
                    'sqlite_bytes': self.path.stat().st_size}

    def approve_local_user(self, p, user_id: str):
        self.session(p)
        if p['role'] != 'owner': raise Forbidden('Owner approval required')
        with self.transaction() as db:
            count = db.execute("UPDATE users SET verified=1 WHERE id=? AND role='member'", (user_id,)).rowcount
            if not count: raise Missing('Pending local member not found')
            self.audit(db, p['user_id'], 'local_member.approved', user_id, {'email_verified': False})
        return {'approved_for_local_use': True, 'email_verified': False}
