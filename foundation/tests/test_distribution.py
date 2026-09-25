import base64
import json
from pathlib import Path
import zipfile
import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from redxai.distribution import canonical,create_package,inspect_package,install,rollback,runtime_platform,valid_path
from redxai.format import export_snapshot,import_snapshot
from redxai.themes import DARK,LIGHT,export_theme,import_theme


def test_snapshot_plain_and_encrypted(sample):
    plain=export_snapshot(sample,'Accounts.Red-XAI')
    assert import_snapshot(plain)['source']==sample
    encrypted=export_snapshot(sample,'Accounts.Red-XAI','SyntheticPhrase9!')
    assert b'PlayerVerified' not in encrypted
    assert import_snapshot(encrypted,'SyntheticPhrase9!')['source']==sample
    for raw,password in [(encrypted,'WrongPassword9!'),(plain[:-1]+b'?',None),(b'invalid',None),(encrypted[:-1]+b'?', 'SyntheticPhrase9!')]:
        with pytest.raises(ValueError):import_snapshot(raw,password)

@pytest.mark.parametrize('path',['../evil','/etc/passwd','x/../a','x\\evil','C:/x','a//b','CON','test./evil','a/.env/../z'])
def test_package_path_rejection(path):assert not valid_path(path)


def test_install_upgrade_rollback_and_signature(tmp_path):
    key=Ed25519PrivateKey.generate();trust={'test-key':key.public_key()};source=tmp_path/'source';source.mkdir()
    (source/'app.txt').write_text('version one')
    system,arch=runtime_platform();one=tmp_path/'one.RXAI';two=tmp_path/'two.RXAI'
    create_package(source,one,'red-xai-test','1.0.0',key,'test-key',system,arch)
    assert inspect_package(one,trust)['version']=='1.0.0'
    destination=tmp_path/'apps';a=install(one,destination,trust)
    assert Path(a['release'],'app.txt').read_text()=='version one'
    (source/'app.txt').write_text('version two')
    create_package(source,two,'red-xai-test','2.0.0',key,'test-key',system,arch)
    install(two,destination,trust)
    assert rollback(destination,'red-xai-test')['version']=='1.0.0'
    with pytest.raises(ValueError):install(one,destination,trust)
    with pytest.raises(ValueError):inspect_package(one,{'test-key':Ed25519PrivateKey.generate().public_key()})
    with pytest.raises(ValueError):inspect_package(one,{})
    # Unexpected archive entries must be rejected even when the manifest signature is valid.
    with zipfile.ZipFile(two,'a') as archive:archive.writestr('../escape','evil')
    with pytest.raises(ValueError):inspect_package(two,trust)


def test_signed_traversal_is_rejected(tmp_path):
    key=Ed25519PrivateKey.generate();source=tmp_path/'source';source.mkdir();(source/'ok').write_text('ok')
    package=tmp_path/'pkg.RXAI';system,arch=runtime_platform()
    create_package(source,package,'red-xai-test','1.0.0',key,'test',system,arch)
    with zipfile.ZipFile(package) as archive:manifest=json.loads(archive.read('manifest.json'))
    manifest['files'][0]['path']='../../evil'
    raw=canonical(manifest)
    with zipfile.ZipFile(package,'w') as archive:
        archive.writestr('manifest.json',raw);archive.writestr('manifest.sig',base64.b64encode(key.sign(raw)))
        archive.writestr('payload/../../evil','ok')
    with pytest.raises(ValueError):inspect_package(package,{'test':key.public_key()})

@pytest.mark.parametrize('theme',[DARK,LIGHT])
def test_theme_roundtrip(theme):assert import_theme(export_theme(theme))==theme

@pytest.mark.parametrize('theme',['@import "https://example.com";','body { background: red; }',':root {--rx-accent: url(https://example.com);}',':root {--rx-accent: #123456;--rx-accent:#ff0000;}'])
def test_theme_rejects_active_content(theme):
    with pytest.raises(ValueError):import_theme(theme)

@pytest.mark.parametrize('name',['a<b','a>b','a|b','a?b','a*b','a"b','a\nb'])
def test_windows_special_paths_rejected(name):
    assert not valid_path(name)


def test_cancelled_install_preserves_previous_version(tmp_path):
    key=Ed25519PrivateKey.generate();trust={'test':key.public_key()}
    source=tmp_path/'source';source.mkdir();(source/'a').write_text('first')
    system,arch=runtime_platform();pkg=tmp_path/'pkg.RXAI';dest=tmp_path/'apps'
    create_package(source,pkg,'red-xai-test','1.0.0',key,'test',system,arch)
    install(pkg,dest,trust)
    old=(dest/'red-xai-test/current.json').read_bytes()
    (source/'a').write_text('next')
    create_package(source,pkg,'red-xai-test','2.0.0',key,'test',system,arch)
    def cancel(_):raise RuntimeError('Synthetic cancellation')
    with pytest.raises(RuntimeError,match='Synthetic'):install(pkg,dest,trust,progress=cancel)
    assert (dest/'red-xai-test/current.json').read_bytes()==old
    assert not list((dest/'red-xai-test').glob('.stage-*'))
    assert not (dest/'red-xai-test/.install.lock').exists()


def test_lock_prevents_concurrent_install(tmp_path):
    key=Ed25519PrivateKey.generate();trust={'test':key.public_key()}
    source=tmp_path/'source';source.mkdir();(source/'a').write_text('first')
    system,arch=runtime_platform();pkg=tmp_path/'pkg.RXAI';dest=tmp_path/'apps'
    create_package(source,pkg,'red-xai-test','1.0.0',key,'test',system,arch)
    (dest/'red-xai-test').mkdir(parents=True);(dest/'red-xai-test/.install.lock').touch()
    with pytest.raises(ValueError,match='Another installation'):install(pkg,dest,trust)


def test_payload_reverified_during_extraction(tmp_path,monkeypatch):
    import redxai.distribution as dist
    key=Ed25519PrivateKey.generate();trust={'test':key.public_key()}
    source=tmp_path/'source';source.mkdir();(source/'a').write_text('first')
    system,arch=runtime_platform();pkg=tmp_path/'pkg.RXAI';dest=tmp_path/'apps'
    create_package(source,pkg,'red-xai-test','1.0.0',key,'test',system,arch)
    inspect=dist.inspect_package
    def replace_after_inspection(path,keys):
        manifest=inspect(path,keys)
        with zipfile.ZipFile(path) as z:raw=z.read('manifest.json');sig=z.read('manifest.sig')
        with zipfile.ZipFile(path,'w') as z:
            z.writestr('manifest.json',raw);z.writestr('manifest.sig',sig);z.writestr('payload/a','EVIL!')
        return manifest
    monkeypatch.setattr(dist,'inspect_package',replace_after_inspection)
    with pytest.raises(ValueError,match='Payload changed'):dist.install(pkg,dest,trust)
    assert not (dest/'red-xai-test/current.json').exists()
