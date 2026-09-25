"""Exercise actual native widgets against a temporary real local API, then capture.
Run on Linux: xvfb-run -a python tools/smoke_desktop.py --output ./screenshots
"""
import argparse
import json
from pathlib import Path
import tempfile
import threading
import time
from PIL import ImageGrab
from redxai.client import Client
from redxai.desktop import ForgeWindow
from redxai.security import OWNER_EMAIL
from redxai.service import LocalServer

parser=argparse.ArgumentParser();parser.add_argument('--output',type=Path,required=True);args=parser.parse_args();args.output.mkdir(parents=True,exist_ok=True)
with tempfile.TemporaryDirectory(prefix='redxai-ui-') as directory:
    server=LocalServer(0,Path(directory)/'data');thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
    client=Client(f'http://127.0.0.1:{server.server_port}')
    registration={'username':'LocalOwner','password':'SyntheticUITest9!','confirm_password':'SyntheticUITest9!',
                  'email':OWNER_EMAIL,'confirm_email':OWNER_EMAIL}
    login=client.request('/v1/setup',{'setup_token':server.store.setup_file.read_text(),'registration':registration});client.token=login['token']
    db=client.create('Accounts');sample=(Path(__file__).parents[1]/'examples'/'Accounts.source.rxai').read_text();client.save(db['id'],sample,1)
    site=Path(directory)/'site';site.mkdir();(site/'index.html').write_text('<h1>Synthetic test site</h1>')
    p=client.request('/v1/host/projects',{'name':'Local test site','directory':str(site)})
    client.request('/v1/host/start',{'project_id':p['id']})
    results=[]
    try:
        for product in ['Database','Host','Installer','Updator']:
            app=ForgeWindow(product,client,login['user']);app.geometry('1280x860+0+0')
            for _ in range(50):app.update();time.sleep(.02)
            if product=='Database':app.load_database(client.read(db['id']))
            for _ in range(15):app.update();time.sleep(.02)
            assert app.winfo_width()==1280
            screenshot=args.output/f'{product.lower()}.png'
            ImageGrab.grab(bbox=(0,0,1280,860)).save(screenshot)
            results.append({'product':product,'opened':True,'width':1280,'height':860,'screenshot':screenshot.name})
            for callback in app.tk.call('after','info'):app.after_cancel(callback)
            app.destroy()
        (args.output/'native-smoke.json').write_text(json.dumps({'platform':'Linux Xvfb','results':results,'windows_macos_tested':False},indent=2))
    finally:server.shutdown();server.server_close();thread.join(3)
print('Four native windows opened and rendered against a temporary local API. No production data used.')
