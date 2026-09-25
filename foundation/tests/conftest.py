from pathlib import Path
import pytest
from redxai.security import OWNER_EMAIL
from redxai.store import Store

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
