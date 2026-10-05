from pathlib import Path
import pytest
import keyring
from redxai.security import OWNER_EMAIL
from redxai.store import Store

class MemoryCredentialStore:
    __module__ = 'keyring.backends.Windows'

    def __init__(self):
        self.items = {}

    def get_password(self, service, account):
        return self.items.get((service, account))

    def set_password(self, service, account, value):
        self.items[(service, account)] = value

@pytest.fixture(autouse=True)
def isolated_os_credentials(monkeypatch):
    backend = MemoryCredentialStore()
    monkeypatch.setattr(keyring, 'get_keyring', lambda: backend)
    monkeypatch.setattr('redxai.security.platform.system', lambda: 'Windows')
    return backend

@pytest.fixture
def sample():
    return (Path(__file__).parents[1]/'examples'/'Accounts.source.rxai').read_text()

@pytest.fixture
def session(tmp_path):
    store=Store(tmp_path/'local')
    registration={'username':'ownerTest','password':'SyntheticPass9!','confirm_password':'SyntheticPass9!',
                  'email':OWNER_EMAIL,'confirm_email':OWNER_EMAIL}
    login=store.bootstrap(store.setup_file.read_text(),registration)
    return store,store.principal(login['token']),login
