"""Native Tk desktop clients. No Electron, WebView, or embedded browser."""
from __future__ import annotations
import argparse
import base64
import json
import os
import queue
import re
import sys
import tempfile
import threading
import tkinter as tk
import tkinter.font as tkfont
from pathlib import Path
from tkinter import filedialog,messagebox,simpledialog,ttk
from .client import Client,ensure_service
from .distribution import install,inspect_package,load_trust,download_https,rollback
from .language import LanguageError,format_document,parse
from .security import OWNER_EMAIL
from .store import data_root
from .themes import DARK,LIGHT,export_theme,import_theme


class ForgeWindow(tk.Tk):
    def __init__(self,product='Database',client:Client|None=None,user:dict|None=None):
        super().__init__()
        self.product=product;self.client=client;self.user=user
        self.theme=dict(DARK);self.events=queue.Queue();self.current=None;self.databases={}
        self.busy_count=0;self.spinner_angle=0;self.modified=False;self.project_rows={}
        self.title(f'Red-XAI {product} | 0.2.0 Engineering Preview')
        self.geometry('1280x860');self.minsize(900,650)
        self.style=ttk.Style(self);self.style.theme_use('clam')
        self._build();self.apply_theme(self.theme)
        self.after(50,self._poll);self.after(80,self._animate)
        self.protocol('WM_DELETE_WINDOW',self.close_window)
        if product in ('Installer','Updator'):
            self.show_distribution()
        elif client and user:
            self.signed_in()
        else:self.work(ensure_service,self.connected,'Starting local Host service…')

    def _build(self):
        self.columnconfigure(1,weight=1);self.rowconfigure(0,weight=1)
        self.sidebar=ttk.Frame(self,padding=20,style='Panel.TFrame');self.sidebar.grid(row=0,column=0,sticky='nsew')
        self.logo=tk.Canvas(self.sidebar,width=168,height=100,highlightthickness=0)
        self.logo.pack(pady=(6,16))
        ttk.Label(self.sidebar,text='RED-XAI',font=('Arial',21,'bold'),style='Panel.TLabel').pack(anchor='w')
        ttk.Label(self.sidebar,text=self.product.upper(),style='MutedPanel.TLabel').pack(anchor='w',pady=(4,24))
        for label,callback in [('Workspace',self.home),('Owner dashboard',self.owner),('Documentation',self.documentation),('Appearance',self.appearance)]:
            ttk.Button(self.sidebar,text=label,command=callback,width=21).pack(fill='x',pady=5)
        ttk.Separator(self.sidebar).pack(fill='x',pady=18)
        self.identity=ttk.Label(self.sidebar,text='Local-first preview\nNot a cloud account service',style='MutedPanel.TLabel',wraplength=170)
        self.identity.pack(anchor='w')
        ttk.Label(self.sidebar,text='No browser wrapper\nNo hidden device sharing',style='MutedPanel.TLabel').pack(side='bottom',anchor='w',pady=12)
        self.main=ttk.Frame(self,padding=24);self.main.grid(row=0,column=1,sticky='nsew')
        self.main.columnconfigure(0,weight=1);self.main.rowconfigure(2,weight=1)
        self.heading=ttk.Label(self.main,text=f'Red-XAI {self.product}',font=('Arial',24,'bold'))
        self.heading.grid(row=0,column=0,sticky='w',pady=(0,4))
        self.subtitle=ttk.Label(self.main,text='Your data. Your machine. Explicit access.',style='Muted.TLabel')
        self.subtitle.grid(row=1,column=0,sticky='w',pady=(0,18))
        self.content=ttk.Frame(self.main);self.content.grid(row=2,column=0,sticky='nsew')
        self.content.rowconfigure(0,weight=1);self.content.columnconfigure(0,weight=1)
        self.status=ttk.Label(self.main,text='Starting…',style='Muted.TLabel',wraplength=900)
        self.status.grid(row=3,column=0,sticky='ew',pady=(16,0))
        self.progress=ttk.Progressbar(self.main,mode='determinate',maximum=100)
        self.progress.grid(row=4,column=0,sticky='ew',pady=(8,0))

    def apply_theme(self,theme):
        self.theme=theme;self.configure(bg=theme['background'])
        self.style.configure('.',font=('Arial',11),background=theme['background'],foreground=theme['foreground'],bordercolor=theme['border'])
        self.style.configure('TFrame',background=theme['background'])
        self.style.configure('Panel.TFrame',background=theme['panel'])
        self.style.configure('TLabel',background=theme['background'],foreground=theme['foreground'])
        self.style.configure('Panel.TLabel',background=theme['panel'])
        self.style.configure('Muted.TLabel',foreground=theme['muted'])
        self.style.configure('MutedPanel.TLabel',background=theme['panel'],foreground=theme['muted'])
        self.style.configure('TButton',padding=(12,9),background=theme['panel'],foreground=theme['foreground'])
        self.style.map('TButton',background=[('active',theme['secondary'])])
        self.style.configure('Accent.TButton',background=theme['accent'])
        self.style.configure('TEntry',fieldbackground=theme['field'],foreground=theme['foreground'],insertcolor=theme['foreground'])
        self.style.configure('Treeview',background=theme['field'],fieldbackground=theme['field'],foreground=theme['foreground'],rowheight=29)
        self.style.configure('Treeview.Heading',background=theme['panel'],foreground=theme['foreground'],padding=7)
        self.style.map('Treeview',background=[('selected',theme['secondary'])])
        self.style.configure('Horizontal.TProgressbar',background=theme['accent'],troughcolor=theme['panel'])
        self.logo.configure(bg=theme['panel']);self.logo.delete('all')
        self.logo.create_line(57,29,109,80,fill=theme['accent'],width=15,capstyle='round')
        self.logo.create_line(109,29,57,80,fill=theme['accent'],width=15,capstyle='round')
        if hasattr(self,'editor') and self.editor.winfo_exists():
            self.editor.configure(bg=theme['field'],fg=theme['foreground'],insertbackground=theme['foreground'],selectbackground=theme['secondary'])
            self.line_numbers.configure(bg=theme['panel'],fg=theme['muted'])
            self.highlight()

    def _animate(self):
        self.logo.delete('spinner')
        if self.busy_count:
            self.spinner_angle=(self.spinner_angle+12)%360
            self.logo.create_arc(40,11,126,97,start=self.spinner_angle,extent=245,outline=self.theme['secondary'],width=3,style='arc',tags='spinner')
        self.after(80,self._animate)

    def _poll(self):
        while True:
            try:callback,result,error=self.events.get_nowait()
            except queue.Empty:break
            self.busy_count=max(0,self.busy_count-1)
            if error:
                self.status.configure(text=str(error));messagebox.showerror('Red-XAI',str(error),parent=self)
            else:
                self.status.configure(text='Ready')
                if callback:
                    try:callback(result)
                    except Exception as exc:self.status.configure(text=str(exc))
        self.after(50,self._poll)

    def work(self,function,callback=None,status='Working…'):
        self.busy_count+=1;self.status.configure(text=status)
        def run():
            try:self.events.put((callback,function(),None))
            except Exception as exc:self.events.put((None,None,exc))
        threading.Thread(target=run,daemon=True).start()

    def clear(self):
        for child in self.content.winfo_children():child.destroy()

    def connected(self,client):
        self.client=client
        self.work(lambda:client.request('/health'),self.sign_in,'Checking local identity…')

    def sign_in(self,health):
        self.clear();setup=health['setup_needed']
        frame=ttk.Frame(self.content,padding=28,style='Panel.TFrame');frame.pack(anchor='center',pady=16)
        ttk.Label(frame,text='Create your local owner account' if setup else 'Sign into this Host',font=('Arial',18,'bold'),style='Panel.TLabel').pack(anchor='w',pady=(0,8))
        ttk.Label(frame,text='The Host and Database share this account on this computer.\nThis preview does not verify email ownership or enable social sign-in.',style='MutedPanel.TLabel').pack(anchor='w',pady=(0,16))
        fields={}
        names=['username','password','confirm_password','email','confirm_email'] if setup else ['email','password']
        for name in names:
            ttk.Label(frame,text=name.replace('_',' ').title(),style='Panel.TLabel').pack(anchor='w')
            entry=ttk.Entry(frame,width=48,show='•' if 'password' in name else '')
            entry.pack(fill='x',pady=(3,9));fields[name]=entry
            if 'email' in name:entry.insert(0,OWNER_EMAIL)
        def submit():
            values={k:e.get() for k,e in fields.items()}
            if setup:
                args={'setup_token':(data_root()/'setup.token').read_text(),'registration':values}
                request=lambda:self.client.request('/v1/setup',args)
            else:request=lambda:self.client.request('/v1/login',values)
            self.work(request,self.accept_login,'Authenticating locally…')
        ttk.Button(frame,text='Create owner account' if setup else 'Sign in',style='Accent.TButton',command=submit).pack(fill='x',pady=10)
        if not setup:ttk.Button(frame,text='Register a local member',command=self.register_dialog).pack(fill='x')
        ttk.Label(frame,text='Username: 5+ characters, 3+ letters.\nPassword: 8–600 characters, upper/lowercase, number and symbol.',style='MutedPanel.TLabel').pack(anchor='w',pady=12)

    def register_dialog(self):
        dialog=tk.Toplevel(self);dialog.title('Local member registration');dialog.transient(self);dialog.grab_set()
        fields={}
        for i,name in enumerate(['username','password','confirm_password','email','confirm_email']):
            ttk.Label(dialog,text=name.replace('_',' ').title()).grid(row=i,column=0,padx=12,pady=7,sticky='w')
            entry=ttk.Entry(dialog,width=36,show='•' if 'password' in name else '');entry.grid(row=i,column=1,padx=12,pady=7);fields[name]=entry
        def submit():
            registration={k:v.get() for k,v in fields.items()}
            self.work(lambda:self.client.request('/v1/register',{'registration':registration}),lambda r:(dialog.destroy(),self.info('Registration saved',r)))
        ttk.Button(dialog,text='Register; await owner approval',command=submit).grid(row=6,column=0,columnspan=2,pady=16)

    def accept_login(self,result):
        self.client.token=result['token'];self.user=result['user'];self.signed_in()
    def signed_in(self):
        self.identity.configure(text=f"{self.user['username']}\n{self.user['role'].title()} · local installation")
        self.home()
    def home(self):
        if self.product in ('Installer','Updator'):return self.show_distribution()
        if not self.user:return
        if self.product=='Host':self.show_host()
        else:self.show_database()

    def show_database(self):
        self.clear();self.heading.configure(text='Database workspace');self.subtitle.configure(text='Boxes, Packers, explicit types, revision-safe saves. No real account data in this preview yet.')
        shell=ttk.Frame(self.content);shell.pack(fill='both',expand=True)
        toolbar=ttk.Frame(shell);toolbar.pack(fill='x',pady=(0,12))
        for label,callback in [('New database',self.new_database),('Refresh',self.refresh_databases),('Save',self.save),('Validate',self.validate),('Format',self.format_source),('Export',self.export_db),('Import',self.import_db)]:
            ttk.Button(toolbar,text=label,command=callback,style='Accent.TButton' if label in ('New database','Save') else 'TButton').pack(side='left',padx=(0,5))
        panes=ttk.Panedwindow(shell,orient='horizontal');panes.pack(fill='both',expand=True)
        left=ttk.Frame(panes,width=255);right=ttk.Frame(panes);panes.add(left,weight=1);panes.add(right,weight=4)
        left.columnconfigure(0,weight=1);left.rowconfigure(2,weight=1)
        self.db_tree=ttk.Treeview(left,columns=('revision',),show='tree headings',height=5)
        self.db_tree.heading('#0',text='DATABASES');self.db_tree.heading('revision',text='Rev')
        self.db_tree.column('#0',width=160,minwidth=130);self.db_tree.column('revision',width=40,minwidth=36,stretch=False)
        self.db_tree.grid(row=0,column=0,sticky='ew');self.db_tree.bind('<<TreeviewSelect>>',self.select_database)
        ttk.Label(left,text='BOX / PACKER EXPLORER',style='Muted.TLabel').grid(row=1,column=0,sticky='w',pady=(12,5))
        self.object_tree=ttk.Treeview(left,show='tree',height=6);self.object_tree.grid(row=2,column=0,sticky='nsew')
        options=ttk.Frame(left);options.grid(row=3,column=0,sticky='ew',pady=(8,0))
        options.columnconfigure(0,weight=1);options.columnconfigure(1,weight=1)
        for index,(label,callback) in enumerate([('Add Packer',self.add_packer),('API key',self.generate_key),('Box token',self.box_token),('History',self.show_history),('Delete',self.delete_db)]):
            ttk.Button(options,text=label,command=callback,width=9).grid(row=index//2,column=index%2,sticky='ew',padx=2,pady=2)
        editframe=ttk.Frame(right);editframe.pack(fill='both',expand=True)
        self.line_numbers=tk.Text(editframe,width=5,padx=8,wrap='none',borderwidth=0,takefocus=0,state='disabled',font=('Courier',11))
        self.line_numbers.pack(side='left',fill='y')
        fonts=set(tkfont.families());family=next((f for f in ['Cascadia Code','Consolas','Menlo','DejaVu Sans Mono'] if f in fonts),'Courier')
        self.editor=tk.Text(editframe,wrap='none',undo=True,maxundo=200,borderwidth=0,padx=16,pady=10,font=(family,11),tabs=('4c',))
        self.editor.pack(side='left',fill='both',expand=True)
        scroll=ttk.Scrollbar(editframe,orient='vertical')
        def yview(*args):self.editor.yview(*args);self.line_numbers.yview_moveto(self.editor.yview()[0])
        scroll.configure(command=yview);scroll.pack(side='right',fill='y')
        self.editor.configure(yscrollcommand=lambda a,b:(scroll.set(a,b),self.line_numbers.yview_moveto(a)))
        horizontal=ttk.Scrollbar(right,orient='horizontal',command=self.editor.xview);horizontal.pack(fill='x')
        self.editor.configure(xscrollcommand=horizontal.set)
        self.editor.bind('<<Modified>>',self.on_modified);self.editor.bind('<Control-s>',lambda _:self.save())
        self.editor.bind('<Command-s>',lambda _:self.save())
        self.editor.bind('<Tab>',lambda event:(self.editor.insert('insert','    '),'break')[-1])
        self.diagnostics=ttk.Label(right,text='Create or select a database to begin.',style='Muted.TLabel',wraplength=720)
        self.diagnostics.pack(fill='x',pady=(9,0));self.current=None;self.apply_theme(self.theme);self.refresh_databases()

    def refresh_databases(self):
        self.work(self.client.databases,self.show_databases,'Loading databases…')
    def show_databases(self,rows):
        if not hasattr(self,'db_tree') or not self.db_tree.winfo_exists():return
        self.db_tree.delete(*self.db_tree.get_children());self.databases={row['id']:row for row in rows}
        for row in rows:self.db_tree.insert('','end',iid=row['id'],text=row['name'],values=(row['revision'],))
        self.status.configure(text=f'{len(rows)} database(s) · connected to local Host')
    def select_database(self,_=None):
        selection=self.db_tree.selection()
        if not selection:return
        if self.modified and not messagebox.askyesno('Unsaved source','Discard unsaved edits and open another database?',parent=self):return
        uid=selection[0];self.work(lambda:self.client.read(uid),self.load_database,'Opening database…')
    def load_database(self,document):
        if not hasattr(self,'editor') or not self.editor.winfo_exists():return
        self.current=document;self.editor.delete('1.0','end');self.editor.insert('1.0',document['source'])
        self.editor.edit_reset();self.editor.edit_modified(False);self.modified=False;self.highlight()
        self.object_tree.delete(*self.object_tree.get_children())
        for row in document['objects']:
            path=row['path'];iid=json.dumps(path);parent=json.dumps(path[:-1]) if path else ''
            label=f"{row['name']}  [{row.get('local_id')}]"
            self.object_tree.insert(parent,'end',iid=iid,text=label,open=True)
        self.diagnostics.configure(text=f"{document['name']} · revision {document['revision']} · {len(document['objects'])} objects")
    def on_modified(self,_=None):
        if self.editor.edit_modified():
            self.modified=True;self.editor.edit_modified(False)
            if getattr(self,'highlight_job',None):self.after_cancel(self.highlight_job)
            self.highlight_job=self.after(160,self.highlight)
    def highlight(self):
        self.highlight_job=None
        if not hasattr(self,'editor') or not self.editor.winfo_exists():return
        text=self.editor.get('1.0','end-1c')
        for tag in ('box','packer','square','curly','symbol','number','boolean','string','nell','comment'):
            self.editor.tag_configure(tag,foreground=self.theme[tag]);self.editor.tag_remove(tag,'1.0','end')
        patterns=[('square',r'[\[\]]'),('curly',r'[{}]'),('symbol',r'[=,<>():]'),('number',r'\b\d+(?:\.\d+)?\b'),
                  ('boolean',r'\b(?:true|false)\b'),('nell',r'\bnell\b'),('string',r'"(?:\\.|[^"\\])*"'),
                  ('box',r'\{("[^"\n]+"|[\w -]+)\}(?:\[[^\]\n]*\])?\s*(?=\{)'),
                  ('packer',r'\{("[^"\n]+"|[\w -]+)\}(?:\[[^\]\n]*\])?\s*(?==)'),
                  ('comment',r'/-(?:[^\n]*?-\\|[^\n]+|\s*\n[\s\S]*?-\\)')]
        if len(text)<500_000:
            for tag,pattern in patterns:
                for match in re.finditer(pattern,text,re.I):
                    group=1 if tag in ('box','packer') else 0
                    self.editor.tag_add(tag,f'1.0+{match.start(group)}c',f'1.0+{match.end(group)}c')
        self.line_numbers.configure(state='normal');self.line_numbers.delete('1.0','end')
        self.line_numbers.insert('1.0','\n'.join(str(i) for i in range(1,text.count('\n')+2)));self.line_numbers.configure(state='disabled')
    def new_database(self):
        name=simpledialog.askstring('New database','Database name (extension is normalized):',initialvalue='Accounts',parent=self)
        if name:
            def created(result):
                self.info('Save this API key once',{'api_key':result['api_key'],'key_ref':result['key_ref'],
                          'notice':'The exported database never contains this secret. This key expires after 90 days.'})
                self.refresh_databases();self.work(lambda:self.client.read(result['id']),self.load_database)
            self.work(lambda:self.client.create(name),created,'Creating database and scoped key…')
    def save(self):
        if not self.current:return
        uid,revision,source=self.current['id'],self.current['revision'],self.editor.get('1.0','end-1c')
        def saved(result):
            self.modified=False;self.current['revision']=result['revision']
            self.work(lambda:self.client.read(uid),self.load_database);self.refresh_databases()
        self.work(lambda:self.client.save(uid,source,revision),saved,'Validating and committing transaction…')
    def validate(self):
        source=self.editor.get('1.0','end-1c')
        def done(result):self.diagnostics.configure(text=f"Valid syntax · {len(result['objects'])} objects · not yet saved")
        self.work(lambda:self.client.request('/v1/validate',{'source':source}),done,'Validating…')
    def format_source(self):
        try:
            text=format_document(parse(self.editor.get('1.0','end-1c')))
            if '/-' in self.editor.get('1.0','end-1c') and not messagebox.askyesno('Canonical format','Canonical formatting removes comments. Continue?',parent=self):return
            self.editor.delete('1.0','end');self.editor.insert('1.0',text)
        except Exception as exc:messagebox.showerror('Syntax diagnostic',str(exc),parent=self)
    def export_db(self):
        if not self.current:return
        path=filedialog.asksaveasfilename(parent=self,defaultextension='.Red-XAI',initialfile=self.current['name'],filetypes=[('Red-XAI database','*.Red-XAI')])
        if not path:return
        password=simpledialog.askstring('Export encryption','Snapshot passphrase (8+ characters). Leave blank for a readable export:',show='•',parent=self)
        if password is None:return
        if not password and not messagebox.askyesno('Unencrypted export','This export will contain readable database data. Continue?',parent=self):return
        uid=self.current['id']
        self.work(lambda:self.client.request('/v1/db/'+uid+'/export',{'passphrase':password or None}),
                  lambda r:(Path(path).write_bytes(base64.b64decode(r['data_base64'])),self.status.configure(text='Snapshot exported; API secrets excluded.')))
    def import_db(self):
        if not self.current:return
        path=filedialog.askopenfilename(parent=self,filetypes=[('Red-XAI database','*.Red-XAI')])
        if not path:return
        password=simpledialog.askstring('Import','Passphrase for encrypted export; otherwise blank:',show='•',parent=self)
        if password is None:return
        if not messagebox.askyesno('Import snapshot','Replace the current document with this snapshot? A new history revision will be retained.',parent=self):return
        data={'data_base64':base64.b64encode(Path(path).read_bytes()).decode(),'passphrase':password or None,'revision':self.current['revision']}
        uid=self.current['id'];self.work(lambda:self.client.request('/v1/db/'+uid+'/import',data),lambda _:self.work(lambda:self.client.read(uid),self.load_database))
    def selected_path(self):
        selected=self.object_tree.selection();return json.loads(selected[0]) if selected else []
    def add_packer(self):
        if not self.current:return
        name=simpledialog.askstring('New Packer','Packer name:',parent=self)
        if not name:return
        source=simpledialog.askstring('Value','Value as Red-XAI syntax (NELL, "text", 12, [1,2], etc.):',initialvalue='NELL',parent=self)
        if source is None:return
        uid,revision,path=self.current['id'],self.current['revision'],self.selected_path()
        self.work(lambda:self.client.edit(uid,revision,'add_packer',path,name=name,value_source=source),lambda _:self.work(lambda:self.client.read(uid),self.load_database))
    def generate_key(self):
        if self.current:
            uid=self.current['id'];self.work(lambda:self.client.request('/v1/db/'+uid+'/keys',{'days':30}),lambda r:self.info('New API key — shown once',r))
    def box_token(self):
        if self.current:
            uid,path=self.current['id'],self.selected_path();self.work(lambda:self.client.request('/v1/db/'+uid+'/keys',{'box_path':path,'read_only':True,'days':7}),lambda r:self.info('Read-only box grant — invalidated by edits',r))
    def show_history(self):
        if self.current:
            uid=self.current['id'];self.work(lambda:self.client.request('/v1/db/'+uid+'/history'),lambda r:self.info('Retained revisions — restore through API',r))
    def delete_db(self):
        if self.current and messagebox.askyesno('Delete database','Soft-delete this database and revoke its keys? History remains retained.',parent=self):
            uid,rev=self.current['id'],self.current['revision'];self.work(lambda:self.client.request('/v1/db/'+uid+'/delete',{'revision':rev}),lambda _:self.show_database())

    def show_host(self):
        self.clear();self.heading.configure(text='Host control center');self.subtitle.configure(text='Single-node preview · static websites and the Red-XAI database API · no shared customer workloads.')
        frame=ttk.Frame(self.content);frame.pack(fill='both',expand=True)
        self.stats_label=ttk.Label(frame,text='Reading system statistics…',font=('Arial',13));self.stats_label.pack(anchor='w',pady=(0,20))
        bar=ttk.Frame(frame);bar.pack(fill='x')
        for name,callback in [('Add static project',self.add_project),('Start',lambda:self.project_action('start')),('Stop',lambda:self.project_action('stop')),('Refresh stats',self.refresh_host),('Cloudflare domains',self.cloudflare)]:
            ttk.Button(bar,text=name,command=callback).pack(side='left',padx=(0,8))
        self.projects_tree=ttk.Treeview(frame,columns=('kind','state','url'),show='tree headings');self.projects_tree.pack(fill='both',expand=True,pady=18)
        self.projects_tree.heading('#0',text='PROJECT');self.projects_tree.heading('kind',text='TYPE');self.projects_tree.heading('state',text='STATE');self.projects_tree.heading('url',text='LOCAL ADDRESS')
        self.projects_tree.column('#0',width=180);self.projects_tree.column('kind',width=80);self.projects_tree.column('state',width=90);self.projects_tree.column('url',width=280)
        ttk.Label(frame,text='The local service remains running after this window closes. Projects require an explicit Start after service restart.\nRemote nodes, automatic failover, public DNS changes, billing, and mobile hosting are not enabled in this preview.',style='Muted.TLabel',wraplength=850).pack(anchor='w')
        self.refresh_host()
    def refresh_host(self):
        if not self.user:return
        def render(results):
            stats,projects=results
            if not hasattr(self,'projects_tree') or not self.projects_tree.winfo_exists():return
            self.stats_label.configure(text=f"CPU {stats['cpu_percent']}%  |  RAM {stats['memory_used_bytes']/1024**3:.1f}/{stats['memory_total_bytes']/1024**3:.1f} GiB  |  Disk free {stats['disk_free_bytes']/1024**3:.1f} GiB  |  1 local node")
            self.projects_tree.delete(*self.projects_tree.get_children())
            for p in projects:self.projects_tree.insert('','end',iid=p['id'],text=p['name'],values=(p['kind'],'Running' if p['running'] else 'Stopped',p['url'] or '—'))
        self.work(lambda:(self.client.request('/v1/host/stats'),self.client.request('/v1/host/projects')),render)
    def add_project(self):
        name=simpledialog.askstring('Static project','Project name:',parent=self)
        if not name:return
        directory=filedialog.askdirectory(parent=self,title='Select only the static website folder')
        if directory:self.work(lambda:self.client.request('/v1/host/projects',{'name':name,'directory':directory}),lambda _:self.refresh_host())
    def project_action(self,action):
        selected=self.projects_tree.selection()
        if selected:self.work(lambda:self.client.request('/v1/host/'+action,{'project_id':selected[0]}),lambda _:self.refresh_host())
    def cloudflare(self):
        key=simpledialog.askstring('Cloudflare — read only','Scoped API token with Zone Read. Used for this request only; not stored:',show='•',parent=self)
        if key:self.work(lambda:self.client.request('/v1/host/cloudflare/zones',{'api_token':key}),lambda r:self.info('Cloudflare domains — no DNS changes made',r),'Reading authorized Cloudflare domains…')

    def owner(self):
        if not self.client or not self.user:return
        self.work(lambda:self.client.request('/v1/owner'),lambda r:self.info('Local owner dashboard',r))
    def documentation(self):
        self.info('Red-XAI quick reference',{'scope':'Local-first engineering preview, not production-ready for real account data.',
            'local_api':'http://127.0.0.1:46321/v1','source':'/- comment; {Red-XAI}[1]{ ... <[True,1,false,"keyref:local"]>}',
            'packer':'{PlayerName}[2] = ["Player"][2],','find':'POST /v1/db/{id}/find with name/local_id/global_id',
            'save':'POST /v1/db/{id}/source with source and expected revision','arrays':'One-based indexes; [NELL] keeps its element.',
            'tokens':'Generated secrets are shown once; box grants expire on every document revision.',
            'updates':'Only independently trusted signed .RXAI packages are accepted. No update feed is published yet.',
            'providers':'Social sign-in, payments, remote-node scheduling, and email delivery are not connected.'})
    def appearance(self):
        dialog=tk.Toplevel(self);dialog.title('Appearance');dialog.transient(self)
        for text,theme in [('Blood Moon — dark',DARK),('Crimson Daylight — light',LIGHT)]:
            ttk.Button(dialog,text=text,command=lambda t=theme:self.apply_theme(dict(t))).pack(fill='x',padx=20,pady=8)
        def export():
            file=filedialog.asksaveasfilename(parent=dialog,defaultextension='.css',initialfile=self.theme['name']+'.css')
            if file:Path(file).write_text(export_theme(self.theme),encoding='utf-8')
        def import_():
            file=filedialog.askopenfilename(parent=dialog,filetypes=[('Red-XAI theme','*.css')])
            if file:
                try:self.apply_theme(import_theme(Path(file).read_text(encoding='utf-8')))
                except Exception as exc:messagebox.showerror('Invalid theme',str(exc),parent=dialog)
        ttk.Button(dialog,text='Export current template',command=export).pack(fill='x',padx=20,pady=8)
        ttk.Button(dialog,text='Import named CSS theme',command=import_).pack(fill='x',padx=20,pady=8)
    def info(self,title,value):
        window=tk.Toplevel(self);window.title(title);window.geometry('820x560');window.transient(self)
        text=tk.Text(window,wrap='word',bg=self.theme['field'],fg=self.theme['foreground'],padx=18,pady=18,font=('Courier',11))
        text.pack(fill='both',expand=True);text.insert('1.0',value if isinstance(value,str) else json.dumps(value,indent=2,ensure_ascii=False));text.configure(state='disabled')
        ttk.Button(window,text='Close',command=window.destroy).pack(pady=10)

    def show_distribution(self):
        self.clear();self.heading.configure(text=f'Red-XAI {self.product}');self.status.configure(text='Ready · signed packages only')
        self.subtitle.configure(text='Per-user installation · independent publisher trust · verified files · recoverable version selection')
        frame=ttk.Frame(self.content,padding=24,style='Panel.TFrame');frame.pack(fill='both',expand=True)
        entries={}
        defaults={'package':'','trust':'','destination':str(Path.home()/'Red-XAI-Apps')}
        for key,label in [('package','.RXAI package'),('trust','Independently trusted publisher-keys JSON'),('destination','Installation directory')]:
            row=ttk.Frame(frame,style='Panel.TFrame');row.pack(fill='x',pady=10)
            ttk.Label(row,text=label,style='Panel.TLabel',width=38).pack(side='left')
            entry=ttk.Entry(row);entry.insert(0,defaults[key]);entry.pack(side='left',fill='x',expand=True);entries[key]=entry
            def choose(k=key,e=entry):
                value=filedialog.askdirectory(parent=self) if k=='destination' else filedialog.askopenfilename(parent=self)
                if value:e.delete(0,'end');e.insert(0,value)
            ttk.Button(row,text='Choose',command=choose).pack(side='right',padx=(10,0))
        ttk.Label(frame,text='Recommended settings: current user only, no autorun, no automatic updates, no package scripts.\nCustom mode lets you select another installation directory. User data is never deleted by installation.',style='MutedPanel.TLabel',wraplength=830).pack(anchor='w',pady=20)
        def inspect():
            package,trust=Path(entries['package'].get()),Path(entries['trust'].get())
            self.work(lambda:inspect_package(package,load_trust(trust)),lambda r:self.info('Verified installation manifest',r),'Verifying publisher and package files…')
        def apply():
            package,trust,destination=(Path(entries[k].get()) for k in ('package','trust','destination'))
            if not messagebox.askyesno('Install verified package','Install this package using the selected publisher keys and destination?',parent=self):return
            self.progress['value']=0
            self.work(lambda:install(package,destination,load_trust(trust)),lambda r:(self.progress.configure(value=100),self.info('Package installed',r)),'Verifying, staging, and installing…')
        buttons=ttk.Frame(frame,style='Panel.TFrame');buttons.pack(fill='x',pady=12)
        ttk.Button(buttons,text='Inspect signature and manifest',command=inspect).pack(side='left',padx=(0,10))
        ttk.Button(buttons,text='Install / apply update',style='Accent.TButton',command=apply).pack(side='left')
        if self.product=='Updator':
            def fetch():
                url=simpledialog.askstring('Download update','Final HTTPS URL for a signed .RXAI package:',parent=self)
                if not url:return
                path=filedialog.asksaveasfilename(parent=self,defaultextension='.RXAI',initialfile='update.RXAI')
                if path:
                    if Path(path).exists():return messagebox.showerror('Download','Choose a new filename; existing files are never overwritten.',parent=self)
                    self.work(lambda:download_https(url,Path(path)),lambda _:(entries['package'].delete(0,'end'),entries['package'].insert(0,path)),'Downloading; signature verification still required…')
            ttk.Button(buttons,text='Download update',command=fetch).pack(side='left',padx=10)
            def previous():
                app=simpledialog.askstring('Rollback','Application ID:',parent=self)
                if app and messagebox.askyesno('Rollback','Switch to the previous installed program version? No database migration is reversed.',parent=self):
                    destination=Path(entries['destination'].get());self.work(lambda:rollback(destination,app),lambda r:self.info('Version pointer restored',r))
            ttk.Button(buttons,text='Previous version',command=previous).pack(side='left')
        ttk.Label(frame,text='Preview limitation: no production signing root or hosted update feed has been configured.\nInstaller and Updator do not yet register file associations, build MSI/PKG bootstrappers, or self-update.',style='MutedPanel.TLabel',wraplength=830).pack(anchor='w',pady=24)

    def close_window(self):
        if self.modified and not messagebox.askyesno('Unsaved edits','Close without saving?',parent=self):return
        self.destroy()


def main(product=None):
    if '--service' in sys.argv:
        sys.argv.remove('--service');from .service import main as serve;serve();return
    parser=argparse.ArgumentParser();parser.add_argument('--product',choices=['Database','Host','Installer','Updator'],default=product or 'Database')
    args=parser.parse_args()
    ForgeWindow(args.product).mainloop()

if __name__=='__main__':main()
