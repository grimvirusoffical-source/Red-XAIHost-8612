from concurrent.futures import ThreadPoolExecutor
import json
import pytest
from redxai.language import empty_source
from redxai.security import OWNER_EMAIL,validate_registration
from redxai.store import Store,Conflict,Forbidden,Unauthorized


def test_bootstrap_once(session):
    store,p,login=session
    assert not store.setup_needed()
    with pytest.raises(Unauthorized):store.login(OWNER_EMAIL,'WrongPassword9!')
    assert p['role']=='owner'
    assert login['token'] not in store.path.read_bytes().decode('latin-1')


def test_create_read_encrypted_history_restore(session,sample):
    store,p,_=session
    db=store.create_database(p,'Accounts.txt')
    assert db['name']=='Accounts.Red-XAI'
    api=store.principal(db['api_key']);assert store.read_database(api,db['id'])['revision']==1
    assert store.save_source(api,db['id'],sample,1)['revision']==2
    disk=store.path.read_bytes()
    wal=store.path.with_name(store.path.name+'-wal')
    if wal.exists():disk+=wal.read_bytes()
    assert b'PlayerVerified' not in disk and db['api_key'].encode() not in disk
    assert len(store.history(p,db['id']))==2
    assert store.restore(p,db['id'],1,2)['revision']==3
    assert store.read_database(p,db['id'])['source']==db['source']
    assert Store(store.root).read_database(p,db['id'])['revision']==3


def test_revision_conflict_parallel(session):
    store,p,_=session;db=store.create_database(p,'Concurrent')
    def save(_):
        try:store.save_source(p,db['id'],db['source'],1);return 'saved'
        except Conflict:return 'conflict'
    with ThreadPoolExecutor(max_workers=8) as pool:results=list(pool.map(save,range(24)))
    assert results.count('saved')==1 and results.count('conflict')==23
    assert store.read_database(p,db['id'])['revision']==2


def test_isolation_permissions_and_revoke(session):
    store,p,_=session;a=store.create_database(p,'A');b=store.create_database(p,'B')
    api=store.principal(a['api_key'])
    with pytest.raises(Forbidden):store.read_database(api,b['id'])
    key=store.mint_key(p,a['id'],read_only=True);reader=store.principal(key['key'])
    with pytest.raises(Forbidden):store.save_source(reader,a['id'],a['source'],1)
    store.revoke_key(p,key['reference'])
    with pytest.raises(Unauthorized):store.principal(key['key'])


def test_project_global_collisions(session,sample):
    store,p,_=session;a=store.create_database(p,'A');b=store.create_database(p,'B')
    store.save_source(p,a['id'],sample,1)
    with pytest.raises(Conflict):store.save_source(p,b['id'],sample,1)
    c=store.create_database(p,'C','OtherProject');store.save_source(p,c['id'],sample,1)


def test_crud_arrays_tables_and_scoped_grant(session,sample):
    store,p,_=session;db=store.create_database(p,'CRUD');uid=db['id']
    store.save_source(p,uid,sample,1)
    token=store.mint_key(p,uid,box_path=[0]);reader=store.principal(token['key'])
    subset=store.read_database(reader,uid)
    assert 'source' not in subset and all(row['path'][:1]==[0] for row in subset['objects'])
    store.edit(p,uid,{'revision':2,'operation':'array_insert','path':[0,4],'index':2,'value_source':'"new"'})
    with pytest.raises(Forbidden):store.read_database(reader,uid)
    objects=store.read_database(p,uid)['objects']
    array=next(r for r in objects if r['name']=='PlayerArray')['value']['value']
    assert array[1]['value']=='new' and len(array)==6
    store.edit(p,uid,{'revision':3,'operation':'table_set','path':[0,5],'key_source':'"coins"','value_source':'900'})
    store.edit(p,uid,{'revision':4,'operation':'add_box','path':[],'name':'New Box','local_id':99,'global_id':98})
    store.edit(p,uid,{'revision':5,'operation':'add_packer','path':[1],'name':'Added','value_source':'NELL'})
    store.edit(p,uid,{'revision':6,'operation':'remove','path':[1,0]})
    assert store.read_database(p,uid)['revision']==7


def test_two_users_are_isolated(session):
    store,p,_=session;owner_db=store.create_database(p,'Owner')
    member=store.register({'username':'alice5','password':'SyntheticTest9!','confirm_password':'SyntheticTest9!',
                         'email':'alice@custom.example','confirm_email':'alice@custom.example'})
    with pytest.raises(Forbidden):store.login('alice@custom.example','SyntheticTest9!')
    store.approve_local_user(p,member['id'])
    member_login=store.login('alice@custom.example','SyntheticTest9!');mp=store.principal(member_login['token'])
    assert store.list_databases(mp)==[]
    with pytest.raises(Forbidden):store.read_database(mp,owner_db['id'])
    with pytest.raises(Forbidden):store.owner_dashboard(mp)
    store.create_database(mp,'Member')
    assert len(store.owner_dashboard(p)['databases'])==2

@pytest.mark.parametrize('password',['short1!','lowercase9!','UPPERCASE9!','NoNumber!!','NoSymbol123','A'*601+'a1!'])
def test_password_rules(password):
    with pytest.raises(ValueError):validate_registration('userName',password,password,'a@custom.example','a@custom.example')


def test_username_letters():
    with pytest.raises(ValueError):validate_registration('ab123','GoodPassword9!','GoodPassword9!','a@custom.example','a@custom.example')


def test_long_password_not_truncated():
    password='Aa1!'+'x'*596
    assert validate_registration('userName',password,password,'a@custom.example','a@custom.example')=='a@custom.example'


def test_delete_revokes_and_audit_chain(session):
    from redxai.security import digest
    from redxai.store import _json
    store,p,_=session;db=store.create_database(p,'Delete')
    store.delete(p,db['id'],1)
    with pytest.raises(Unauthorized):store.principal(db['api_key'])
    assert store.owner_dashboard(p)['integrity']=='ok'
    with store.connect() as connection:
        previous='0'*64
        for event in connection.execute('SELECT * FROM audit ORDER BY sequence'):
            assert event['previous']==previous
            previous=digest(_json([event['at'],event['actor'],event['action'],event['target'],event['detail'],event['previous']]))
            assert previous==event['hash']
