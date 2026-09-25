"""Development signing utility; never distribute the generated private key."""
import argparse,base64,json,sys
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from redxai.distribution import create_package,runtime_platform
from redxai.security import private_file

def main():
    parser=argparse.ArgumentParser(description=__doc__);sub=parser.add_subparsers(dest='action',required=True)
    keygen=sub.add_parser('keygen');keygen.add_argument('--private',type=Path,required=True);keygen.add_argument('--trust',type=Path,required=True);keygen.add_argument('--key-id',required=True)
    package=sub.add_parser('package');package.add_argument('--private',type=Path,required=True);package.add_argument('--key-id',required=True)
    for name in ['source','output']:package.add_argument('--'+name,type=Path,required=True)
    for name in ['app-id','version']:package.add_argument('--'+name,required=True)
    system,arch=runtime_platform();package.add_argument('--os',default=system);package.add_argument('--architecture',default=arch)
    args=parser.parse_args()
    if args.action=='keygen':
        if args.private.exists() or args.trust.exists():raise SystemExit('Refusing to overwrite signing/trust files')
        key=Ed25519PrivateKey.generate()
        private_file(args.private,key.private_bytes(serialization.Encoding.PEM,serialization.PrivateFormat.PKCS8,serialization.NoEncryption()))
        public=key.public_key().public_bytes(serialization.Encoding.Raw,serialization.PublicFormat.Raw)
        private_file(args.trust,json.dumps({args.key_id:base64.b64encode(public).decode()}).encode())
        print('Development keys written. Private key was not printed. Keep it out of source control.')
    else:
        if args.output.exists():raise SystemExit('Refusing to overwrite an existing package')
        key=serialization.load_pem_private_key(args.private.read_bytes(),password=None)
        if not isinstance(key,Ed25519PrivateKey):raise SystemExit('Expected Ed25519 signing key')
        create_package(args.source,args.output,args.app_id,args.version,key,args.key_id,args.os,args.architecture)
        print('Signed .RXAI package written. Publisher trust must be established independently.')
if __name__=='__main__':main()
