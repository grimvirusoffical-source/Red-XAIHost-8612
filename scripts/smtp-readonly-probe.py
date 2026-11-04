#!/usr/bin/env python3
"""Bounded SMTP greeting/EHLO only: no AUTH, MAIL, RCPT or DATA commands."""
import json
from pathlib import Path
import smtplib
import socket
import subprocess


def inspect(host, port):
    result = {'host': host, 'port': port, 'mailSent': False, 'credentialsUsed': False}
    try:
        address = socket.getaddrinfo(host, port, socket.AF_INET, socket.SOCK_STREAM)[0][4][0]
        with smtplib.SMTP(address, port, local_hostname='mail.red-xai.com', timeout=5) as client:
            result['connected'] = True
            result['sourceIpv4'] = client.sock.getsockname()[0]
            result['ehloCode'] = client.ehlo()[0]
            result['startTlsAdvertised'] = client.has_extn('starttls')
            result['authAdvertised'] = client.has_extn('auth')
    except Exception as exc:
        result.update(connected=False, errorKind=type(exc).__name__ if type(exc).__name__ in
                      ('TimeoutError', 'ConnectionRefusedError', 'SMTPConnectError',
                       'SMTPServerDisconnected', 'gaierror', 'OSError') else 'connection_failed')
    return result


result = {'localRelay': inspect('127.0.0.1', 587)}
try:
    raw = subprocess.check_output(['/usr/bin/node', '-e',
        "require('dns').resolveMx('gmail.com',(e,v)=>{if(e)process.exit(1);process.stdout.write(JSON.stringify(v));});"],
        env={'PATH': '/usr/bin:/bin'}, stderr=subprocess.DEVNULL, timeout=8)
    entries = json.loads(raw)
    chosen = min(entries, key=lambda x: x['priority'])['exchange']
    if not isinstance(chosen, str) or len(chosen) > 253 or not chosen.endswith('.google.com'):
        raise ValueError('MX refused')
    result['directGmailMx'] = inspect(chosen, 25)
except Exception:
    result['directGmailMx'] = {'connected': False, 'errorKind': 'mx_probe_unavailable', 'mailSent': False}
result['retainedMailRelayMainExists'] = Path('/root/gdg-signing-api-20261028/RedX/cmd/redx-mailrelay/main.go').is_file()
print(json.dumps(result, sort_keys=True))
