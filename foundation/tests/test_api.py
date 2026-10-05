import base64
import json
import threading
import urllib.error
import urllib.request
import pytest
from redxai.client import Client,APIError
from redxai.security import OWNER_EMAIL
from redxai.service import LocalServer

@pytest.fixture
def api(tmp_path):
    server=LocalServer(0,tmp_path/'api');thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
    client=Client(f'http://127.0.0.1:{server.server_port}')
    yield server,client
    server.shutdown();server.server_close();thread.join(timeout=3)


def test_http_owner_database_and_missing_auth(api,sample):
    server,client=api
    assert client.request('/health')['setup_needed']
    with pytest.raises(APIError) as error:client.databases()
    assert error.value.status==401
    login=client.request('/v1/setup',{'setup_token':server.store.setup_file.read_text(),
        'registration':{'username':'OwnerTest','password':'SyntheticPass9!','confirm_password':'SyntheticPass9!',
                        'email':OWNER_EMAIL,'confirm_email':OWNER_EMAIL}})
    client.token=login['token'];db=client.create('Accounts')
    client.save(db['id'],sample,1)
    assert client.find(db['id'],name='PlayerName')[0]['value']['value']=='Player'
    assert len(client.find(db['id'],kind='packer',local_id=2))==5
    container=client.request('/v1/db/'+db['id']+'/export',{'passphrase':'SyntheticExport9!'})
    assert container['encrypted'] and not container['api_keys_included']
    assert b'Player' not in base64.b64decode(container['data_base64'])
    client.request('/v1/logout',{})
    with pytest.raises(APIError):client.databases()

def test_storage_mode_export_import_search_and_revision(api,sample):
    server,client=api
    login=client.request('/v1/setup',{'setup_token':server.store.setup_file.read_text(),
        'registration':{'username':'StorageOwner','password':'SyntheticPass9!','confirm_password':'SyntheticPass9!',
                        'email':OWNER_EMAIL,'confirm_email':OWNER_EMAIL}})
    client.token=login['token']
    database=client.create('Searchable',storage_mode='QS')
    client.save(database['id'],sample,1)
    found=client.request('/v1/db/'+database['id']+'/search',{'query':'PlayerName','limit':10})
    assert len(found)==1 and found[0]['name']=='PlayerName'
    with server.store.connect() as db:
        assert db.execute(
            'SELECT revision FROM quick_search_index WHERE db_id=?',
            (database['id'],),
        ).fetchone()['revision']==2
    exported=client.request('/v1/db/'+database['id']+'/mode-export',
        {'mode':'LSQS','passphrase':'SyntheticDatabase9!'})
    assert exported['extension']=='.Red-XAI-DB-LSQS'
    assert exported['encrypted'] and not exported['api_keys_included']
    assert b'PlayerName' not in base64.b64decode(exported['data_base64'])
    client.save(database['id'],sample,2)
    imported=client.request('/v1/db/'+database['id']+'/mode-import',
        {'data_base64':exported['data_base64'],'passphrase':'SyntheticDatabase9!','revision':3})
    assert imported['revision']==4 and imported['storage_mode']=='LSQS'
    assert client.read(database['id'])['storage_mode']=='LSQS'
    with pytest.raises(APIError):
        client.request('/v1/db/'+database['id']+'/mode-import',
            {'data_base64':exported['data_base64'],'passphrase':'WrongPassphrase9!','revision':4})


def test_http_rejects_cross_site_and_bad_requests(api):
    server,client=api
    url=client.base_url+'/v1/login'
    request=urllib.request.Request(url,data=b'{}',headers={'Origin':'https://attacker.example','Content-Type':'application/json'})
    with pytest.raises(urllib.error.HTTPError) as err:urllib.request.urlopen(request)
    assert err.value.code==403
    request=urllib.request.Request(url,data=b'{bad',headers={'X-Red-XAI':'1','Content-Type':'application/json'})
    with pytest.raises(urllib.error.HTTPError) as err:urllib.request.urlopen(request)
    assert err.value.code==400
    request=urllib.request.Request(url,data=b'{}',headers={'Host':'attacker.example','X-Red-XAI':'1'})
    with pytest.raises(urllib.error.HTTPError) as err:urllib.request.urlopen(request)
    assert err.value.code==403


def test_host_static_and_dotfile_denial(api,tmp_path):
    server,client=api
    website=tmp_path/'site';website.mkdir();(website/'index.html').write_text('<h1>Test site</h1>');(website/'.env').write_text('secret')
    project=server.host.add('Test site',str(website));running=server.host.start(project['id']);url=running[0]['url']
    with urllib.request.urlopen(url) as response:assert response.read()==b'<h1>Test site</h1>'
    with pytest.raises(urllib.error.HTTPError) as err:urllib.request.urlopen(url+'/.env')
    assert err.value.code==403
    server.host.stop(project['id']);assert server.host.list()[0]['running'] is False
