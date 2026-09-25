"""Restricted CSS variable themes, translated into native widget colors."""
from __future__ import annotations
import re

DARK={'name':'Blood Moon','background':'#100c10','panel':'#1c121d','field':'#110d14',
      'foreground':'#f2edf4','muted':'#aaa0ad','accent':'#b51f38','secondary':'#7950a8',
      'border':'#3c2b40','box':'#d87924','packer':'#ffb566','square':'#eacb78',
      'curly':'#b79036','symbol':'#58bc78','number':'#6ca8ff','boolean':'#48d8cb',
      'string':'#b178ff','nell':'#b8e65b','comment':'#85818c'}
LIGHT={'name':'Crimson Daylight','background':'#f5f1f5','panel':'#ffffff','field':'#ffffff',
       'foreground':'#221527','muted':'#635667','accent':'#a50f2d','secondary':'#74439a',
       'border':'#d6c8dc','box':'#93420b','packer':'#9b4d16','square':'#79620a',
       'curly':'#745207','symbol':'#216534','number':'#174fa2','boolean':'#08665f',
       'string':'#7429a5','nell':'#4c6907','comment':'#746d78'}


def export_theme(theme:dict) -> str:
    lines=['/* Red-XAI native UI/editor theme. Colors only; no scripts or remote imports. */',':root {',
           f'  --rx-theme-name: "{theme["name"]}";']
    lines += [f'  --rx-{key}: {value};' for key,value in theme.items() if key!='name']
    return '\n'.join(lines+['}',''])


def import_theme(text:str) -> dict:
    if not isinstance(text,str) or len(text)>16384:raise ValueError('Theme exceeds 16 KiB')
    text=re.sub(r'/\*.*?\*/','',text,flags=re.S).strip()
    match=re.fullmatch(r':root\s*\{([^{}]*)\}',text,re.S)
    if not match:raise ValueError('Use the exported :root template; arbitrary CSS is not supported')
    theme=dict(DARK);seen=set()
    for statement in match.group(1).split(';'):
        if not statement.strip():continue
        entry=re.fullmatch(r'\s*--rx-([a-z-]+)\s*:\s*(.*?)\s*',statement,re.S)
        if not entry:raise ValueError('Invalid theme declaration')
        name,value=entry.groups()
        if name in seen:raise ValueError('Duplicate theme token')
        seen.add(name)
        if name=='theme-name':
            if not re.fullmatch(r'"[A-Za-z0-9 _.-]{1,50}"',value):raise ValueError('Theme name must contain 1–50 simple characters')
            theme['name']=value[1:-1]
        elif name in DARK and name!='name':
            if not re.fullmatch(r'#[0-9a-fA-F]{6}',value):raise ValueError('Colors must be six-digit hexadecimal values')
            theme[name]=value
        else:raise ValueError(f'Unsupported theme token: {name}')
    return theme
