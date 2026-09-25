import pytest
from redxai.language import Box,LanguageError,Parser,empty_source,format_document,parse
from redxai.store import normalize_filename,read_literal


def document(member):return '{Red-XAI}[1]{'+member+'<[True,1,false,"keyref:test"]>}'


def test_sample_and_roundtrip(sample):
    doc=parse(sample)
    assert len(doc.index())==9
    assert parse(format_document(doc)).index()==doc.index()
    assert doc.root.members[0].grants==[('ProfileReader','tokenref:generate-in-app')]

@pytest.mark.parametrize('literal,kind,value',[
    ('1','number','1'),('-1.23','number','-1.23'),('1e12','number','1e12'),
    ('"Player"','string','Player'),('"a\\n\\"b"','string','a\n"b'),
    ('True','boolean',True),('FALSE','boolean',False),('tRuE','boolean',True),
    ('NELL','nell',None),('nell','nell',None),('NeLl','nell',None),
])
def test_types(literal,kind,value):
    parsed=read_literal(literal);assert parsed.kind==kind and parsed.data==value


def test_arrays_tables_meta():
    v=read_literal('[1,NELL,[],{[1]="one",["1"]="string"},meta({a=1},{schema="x"})]')
    assert len(v.data)==5 and v.data[1].kind=='nell'
    assert v.data[3].data[0][0].kind=='number'
    assert v.data[3].data[1][0].kind=='string'
    assert v.data[4].kind=='meta'

@pytest.mark.parametrize('lid,gid',[('',''),('[]','[]'),('[False]','[false]'),('[2]','[2]'),('["abc"]','["def"]')])
def test_optional_ids(lid,gid):
    parse(document('{P}'+lid+'=[False]'+gid+','))


def test_distinct_packers_same_id():
    doc=parse(document('{A}[2]=[1][2],{B}[2]=[2][2],'))
    assert len(doc.root.members)==2

@pytest.mark.parametrize('body',[
    '{A}[2]=[1][2],{A}[2]=[2][3],',
    '{A}[2]=[1][2],{A}[3]=[2][2],',
    '{B}[2]{<[False,9,false]>}{C}[3]{<[False,9,false]>}',
    '{B}[2]{<[False,9,false]>}{C}[2]{<[False,10,false]>}',
    '{B}[1]{<[False,9,false]>}',
    '{B}[2]{<[True,False,false]>}',
])
def test_collisions(body):
    with pytest.raises(LanguageError):parse(document(body))

@pytest.mark.parametrize('source',[
    '{Red-XAI}[2]{<[True,1,false,"keyref:x"]>}',
    '{Red-XAI}[1]{<[True,1,false]>}',
    '{Red-XAI}[1]{<[True,1,false,"a-secret-value"]>}',
    document('{Name}[2]=["Player][2],'),
    document('{Name}[2]=[True][2]'),
    document('{Name}[2]=[unknown][2],'),
    document('{Name}[2]=[{a=1,a=2}][2],'),
    document('{Name}[2]=[{[1]=1,[1.0]=2}][2],'),
    document('{Name}[2]=[meta(1,{})][2],'),
    '/-\nUnclosed comment\n'+document(''),
    document('')+'junk',
])
def test_invalid(source):
    with pytest.raises((LanguageError,ValueError)):parse(source)


def test_comments():
    src='/- one line\n/-\nmultiline\n-\\\n'+document('/- inline -\\ {X}=[1],')
    assert parse(src).root.members[0].value.data=='1'


def test_bound_depth():
    with pytest.raises(LanguageError):read_literal('['*100+'1'+']'*100)


def test_bound_size():
    with pytest.raises(LanguageError):parse(' '*(2*1024*1024+1))


def test_precise_diagnostic():
    with pytest.raises(LanguageError) as error:parse('\n\nBOGUS')
    assert error.value.line==3

@pytest.mark.parametrize('name,expected',[('Accounts','Accounts.Red-XAI'),('Accounts.txt','Accounts.Red-XAI'),('Accounts.RedXAI','Accounts.Red-XAI'),('Accounts.Red-XAI','Accounts.Red-XAI')])
def test_filename(name,expected):assert normalize_filename(name)==expected

@pytest.mark.parametrize('name',['../../file','/etc/shadow','x\\y','CON','NUL','..','', '.'])
def test_bad_filename(name):
    with pytest.raises(ValueError):normalize_filename(name)
