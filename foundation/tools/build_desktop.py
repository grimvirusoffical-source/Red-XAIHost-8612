"""Build native preview bundles on the current OS, then smoke-test frozen services."""
from __future__ import annotations
import argparse, hashlib, json, os, platform, shutil, socket, subprocess, sys, tempfile, time, urllib.request, zipfile
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
PRODUCTS=('Database','Host','Installer','Updator')

def build(product: str, output: Path):
    name='Red-XAI-'+product
    work=ROOT/'build'/product
    seed=ROOT/'assets'/'red-xai-96.png'
    if seed.exists():
        from PIL import Image
        icon_image=Image.open(seed).resize((512,512),Image.Resampling.LANCZOS)
        if sys.platform=='darwin':icon_image.save(ROOT/'assets'/'red-xai.icns')
        elif os.name=='nt':icon_image.save(ROOT/'assets'/'red-xai.ico',sizes=[(16,16),(32,32),(48,48),(256,256)])
    command=[sys.executable,'-m','PyInstaller','--noconfirm','--clean','--onedir','--windowed',
             '--name',name,'--distpath',str(output),'--workpath',str(work),
             '--specpath',str(work),'--paths',str(ROOT),'--collect-all','argon2',
             '--hidden-import','_argon2_cffi_bindings','--hidden-import','psutil',
             '--hidden-import','redxai.service','--hidden-import','redxai.desktop']
    if sys.platform=='darwin':command+=['--osx-bundle-identifier','com.redxai.'+product.lower()]
    icon=ROOT/'assets'/('red-xai.icns' if sys.platform=='darwin' else 'red-xai.ico')
    if icon.exists() and sys.platform in ('win32','darwin'):command+=['--icon',str(icon)]
    command+=[str(ROOT/name/'main.py')]
    subprocess.run(command,cwd=ROOT,check=True)
    binary=(output/(name+'.app')/'Contents'/'MacOS'/name if sys.platform=='darwin' else
            output/name/(name+('.exe' if os.name=='nt' else '')))
    with socket.socket() as listener:
        listener.bind(('127.0.0.1',0));port=listener.getsockname()[1]
    with tempfile.TemporaryDirectory(prefix='redxai-frozen-check-') as data:
        process=subprocess.Popen([str(binary),'--service','--port',str(port),'--data-dir',data],
                                 stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        health=None
        try:
            for _ in range(150):
                if process.poll() is not None:raise RuntimeError(f'{name} service exited: {process.returncode}')
                try:
                    with urllib.request.urlopen(f'http://127.0.0.1:{port}/health',timeout=.4) as response:
                        health=json.load(response)
                    if health.get('ok'):break
                except Exception:time.sleep(.2)
            if not health or not health.get('ok'):raise RuntimeError(f'{name} frozen service failed readiness')
        finally:
            process.terminate()
            try:process.wait(timeout=10)
            except subprocess.TimeoutExpired:process.kill();process.wait()
    bundle=output/(name+'.app') if sys.platform=='darwin' else output/name
    archive=output/(name+'-'+platform.system()+'-'+platform.machine()+'.zip')
    if sys.platform=='darwin':
        # ditto preserves macOS app bundle symlinks and metadata.
        subprocess.run(['ditto','-c','-k','--sequesterRsrc','--keepParent',str(bundle),str(archive)],check=True)
    else:
        with zipfile.ZipFile(archive,'w',zipfile.ZIP_DEFLATED) as z:
            for file in sorted(bundle.rglob('*')):
                if file.is_file():z.write(file,file.relative_to(output))
    return {'product':product,'platform':platform.system(),'architecture':platform.machine(),
            'archive':archive.name,'sha256':hashlib.sha256(archive.read_bytes()).hexdigest(),
            'frozen_localhost_service_smoke':'passed','gui_interaction_test':'not run by this script',
            'publisher_signed':False,'notarized':False,'status':'engineering-preview'}

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--product',choices=PRODUCTS+('all',),default='all')
    parser.add_argument('--output',type=Path,default=ROOT/'dist')
    args=parser.parse_args();args.output.mkdir(parents=True,exist_ok=True)
    results=[build(product,args.output) for product in (PRODUCTS if args.product=='all' else [args.product])]
    (args.output/'build-evidence.json').write_text(json.dumps(results,indent=2))
    print(json.dumps(results,indent=2))
if __name__=='__main__':main()
