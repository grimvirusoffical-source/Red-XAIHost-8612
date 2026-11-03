import { h, link, external, field, empty, pageHead, alert, renderMarkdown, detectPlatform, copyText } from './dom.js';
import { assetJSON, assetText } from './api.js';

export const STORES = Object.freeze([
  ['software','Software'],['games','Games'],['bots','Bots & Automation'],['sdks','Software Development Kits'],
  ['libraries','Libraries & Frameworks'],['repositories','Source Repositories'],['cli','Command-Line Tools'],['subscriptions','Subscriptions'],
]);
export const productPath = product => '/products/'+product.category+'/'+product.slug;
export function priceValue(product) {
  if (product.price?.kind === 'free') return 0;
  return product.price?.kind === 'paid' && Number.isFinite(product.price.amount) && product.price.amount >= 0 ? product.price.amount : null;
}
export function priceLabel(product) {
  const value = priceValue(product); if (value === null) return 'Price to be announced';
  if (value === 0) return 'Free';
  try { return new Intl.NumberFormat(undefined,{style:'currency',currency:product.price.currency || 'USD'}).format(value); } catch { return String(value); }
}
export function productPlatforms(product){
  return [...new Set((Array.isArray(product.platforms)?product.platforms:[]).filter(x=>typeof x==='string'&&x.trim()).map(x=>x.trim().toLowerCase()).filter(x=>!['unknown','unspecified','to be announced','tba'].includes(x)))];
}
export function platformOptions(products){
  const labels=new Map();for(const product of products)for(const label of Array.isArray(product.platforms)?product.platforms:[]){if(typeof label==='string'&&productPlatforms(product).includes(label.trim().toLowerCase()))labels.set(label.trim().toLowerCase(),label.trim());}
  return [...labels].sort((a,b)=>a[1].localeCompare(b[1]));
}
export function storeEmptyState(category,availableCount){
  return availableCount?{title:'No matching products',text:'Try a different platform or pricing filter, or clear your search.'}:{title:'Nothing listed yet',text:category==='subscriptions'?'Subscriptions will be listed here when their plans and checkout are available.':'This store is ready for future listings. Explore the software documentation while listings are prepared.'};
}
export function filterProducts(products, {category,search='',price='all',platform='all',sort='featured',min='',max=''} = {}) {
  const term=search.trim().toLowerCase(),requestedPlatform=String(platform).trim().toLowerCase(); const low=min===''?null:Number(min),high=max===''?null:Number(max);
  const result=products.filter(p => (!category || p.category===category)
    && (!term || [p.name,p.summary,...(p.features||[])].join(' ').toLowerCase().includes(term))
    && (requestedPlatform==='all'||requestedPlatform==='unknown'&&!productPlatforms(p).length||productPlatforms(p).includes(requestedPlatform))
    && (price==='all' || price==='free'&&priceValue(p)===0 || price==='paid'&&priceValue(p)>0 || price==='unannounced'&&priceValue(p)===null)
    && (low===null || Number.isFinite(low)&&priceValue(p)!==null&&priceValue(p)>=low)
    && (high===null || Number.isFinite(high)&&priceValue(p)!==null&&priceValue(p)<=high));
  if (sort==='name') result.sort((a,b)=>a.name.localeCompare(b.name));
  if (sort==='price-low'||sort==='price-high') result.sort((a,b)=>{
    const x=priceValue(a),y=priceValue(b); if(x===null&&y!==null)return 1;if(y===null&&x!==null)return -1;
    return x===y?a.name.localeCompare(b.name):(x-y)*(sort==='price-high'?-1:1);
  });
  return result;
}
export function productCard(product) {
  return h('article',{class:'card product-card'},h('div',{class:'row between'},h('span',{class:'product-icon','aria-hidden':'true'},product.category==='cli'?'>_':product.name.split(/[ -]/u).filter(Boolean).map(x=>x[0]).slice(0,2).join('')),
    h('span',{class:'tag '+(product.category==='software'?'soon':priceValue(product)===0?'free':'')},product.category==='software'?'Coming soon':priceValue(product)===0?'Free':'Documentation')),
    h('h3',null,product.name),h('p',null,product.summary),h('div',{class:'card-bottom'},h('div',{class:'row between'},h('small',null,priceLabel(product)),link('View documentation →',productPath(product)))));
}
export function renderStore(category, catalog) {
  const title=STORES.find(s=>s[0]===category)?.[1] || 'Products';
  const list=h('div',{class:'grid three'}),meta=h('p',{class:'results-meta',role:'status'});
  const search=h('input',{type:'search',placeholder:'Search '+title.toLowerCase(),'aria-label':'Search products'});
  const price=h('select',{'aria-label':'Price filter'},...[['all','All pricing'],['free','Free'],['paid','Paid'],['unannounced','Price unannounced']].map(([v,t])=>h('option',{value:v},t)));
  const platform=h('select',{'aria-label':'Platform filter'},h('option',{value:'all'},'All platforms'),...platformOptions(catalog.products||[]).map(([value,label])=>h('option',{value},label)),h('option',{value:'unknown'},'Platform unspecified'));
  const sort=h('select',{'aria-label':'Sort products'},...[['featured','Featured'],['name','Name A–Z'],['price-low','Price: lowest first'],['price-high','Price: highest first']].map(([v,t])=>h('option',{value:v},t)));
  const min=h('input',{type:'number',min:0,step:'0.01',placeholder:'No minimum','aria-label':'Minimum price'});
  const max=h('input',{type:'number',min:0,step:'0.01',placeholder:'No maximum','aria-label':'Maximum price'});
  const draw=()=>{
    const products=filterProducts(catalog.products||[],{category,search:search.value,price:price.value,platform:platform.value,sort:sort.value,min:min.value,max:max.value});
    meta.textContent=products.length+' product'+(products.length===1?'':'s')+' · Unannounced prices are not treated as free or paid.';
    list.replaceChildren(...products.map(productCard));
    if (!products.length){const availableCount=(catalog.products||[]).filter(p=>p.category===category).length,message=storeEmptyState(category,availableCount);const reset=h('button',{type:'button',class:'button secondary',onClick:()=>{search.value='';price.value='all';platform.value='all';sort.value='featured';min.value='';max.value='';draw();}},'Clear filters');list.append(empty(message.title,message.text,availableCount?reset:link('Browse software','/products/software','button secondary')));}
  };
  for(const node of [search,price,platform,sort,min,max])node.addEventListener(node.tagName==='INPUT'?'input':'change',draw);
  draw();
  return h('div',null,pageHead('Products',title,category==='software'?'Explore the Red-XAI software lineup. Every product on this page is coming soon to this store.':category==='subscriptions'?'One place for subscriptions across Red-XAI products. Pricing and recurring terms appear before any purchase.':'Explore documentation, compatibility and availability before choosing a product.'),
    h('section',{class:'wrap section'},h('div',{class:'filter-bar'},h('div',{class:'search'},field('Search',search)),field('Platform',platform),field('Pricing',price),field('Sort',sort),field('Minimum price',min),field('Maximum price',max)),meta,list));
}

export async function renderProduct(product, context) {
  const sidebar=h('aside',{class:'card sticky'},h('span',{class:'tag '+(product.category==='software'?'soon':'free')},product.category==='software'?'Coming soon to the store':priceLabel(product)),
    h('h2',null,product.name),h('p',null,product.summary),h('dl',{class:'facts'},h('div',null,h('dt',null,'Platforms'),h('dd',null,(product.platforms||[]).join(' · ')||'To be announced')),h('div',null,h('dt',null,'Price'),h('dd',null,priceLabel(product)))),
    h('nav',{class:'doc-nav','aria-label':'Product links'},external(product.repositoryVisibility==='private'?'Repository · access required':'Source repository',product.repositoryUrl),...(product.documentationUrls||[]).filter(u=>(product.repositoryVisibility!=='private'||u.includes('github.io'))&&!/\.(?:nsh|go|tsx?|jsx?|json|html)(?:[?#]|$)/iu.test(u)).map((u,i)=>external(i?'Additional documentation':'Public documentation',u))));
  const body=h('div',{class:'detail-body'});
  const section=(title,items)=>items?.length?h('section',null,h('h2',null,title),h('ul',null,...items.map(text=>h('li',null,text)))):null;
  body.append(section('What it does',product.features)||h('p',null,product.summary));
  for(const item of [section('Requirements',product.requirements),section('Getting started',product.gettingStarted),section('Availability and limitations',product.limitations)])if(item)body.append(item);
  if(product.category==='software')body.prepend(alert('Coming soon to the software store. Documentation describes the product; a listing does not promise an available download or a launch date.','warning'));
  if(product.slug==='redxai-cli') {
    const platform=detectPlatform(navigator);
    const platformPicker=h('select',{'aria-label':'Installation platform'},...['windows','macos','linux'].map(x=>h('option',{value:x,selected:x===platform},x==='macos'?'macOS':x==='windows'?'Windows':'Linux')));
    const instructions=h('div');
    const version=context.config.downloads?.cli?.version||'0.3.0';
    const url=context.config.downloads?.cli?.url||'https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/download/cli-v0.3.0/redxai-cli-0.3.0.tgz';
    sidebar.append(h('div',{class:'separator'}),h('h3',null,'Install REDXAI CLI'),h('p',null,'Free Node.js package · '+version),field('Your platform',platformPicker),h('a',{class:'button',href:url,download:''},'Download CLI package (.tgz)'),h('p',{class:'small muted'},'The verified package is the same on Windows, macOS and Linux. Native graphical installers are coming soon. Node.js 20+ and npm are required.'));
    if(platform==='mobile')sidebar.append(alert('Install this CLI on a supported Windows, macOS or Linux computer. An iPhone or Android node installer is not available from this listing.','warning'));
    const hash=context.config.downloads?.cli?.sha256||'7e04e8171a4542a9ee8cb8c7fd5fda214313b326453cb28c054b6c4ee3efd4f9';
    sidebar.append(h('small',null,'SHA-256'),h('pre',{class:'checksum'},hash),h('button',{type:'button',class:'button secondary small',onClick:()=>copyText(hash)},'Copy checksum'));
    const docs=await assetText('cli-installation.md').catch(()=>null);
    if(!context.current())return {node:h('div')};
    const show=()=>{
      const chosen=platformPicker.value;
      instructions.replaceChildren(h('h2',null,(chosen==='windows'?'Windows':chosen==='macos'?'macOS':'Linux')+' installation'),h('ol',{class:'install-steps'},h('li',null,'Install Node.js 20 or newer and npm from the official Node.js distribution.'),h('li',null,'Download the package, verify its checksum, then install to your user profile.'),h('li',null,'Run redxai version and redxai help.')),
        docs?renderMarkdown(docs):alert('Installation documentation could not be loaded. Use the verified release documentation and retry.','warning'));
    }; platformPicker.addEventListener('change',show);show();body.append(h('section',null,instructions));
  }
  if(product.slug==='red-secretsxai') {
    const docs=await assetJSON('public-secrets-docs.json').catch(()=>null);
    if(!context.current())return {node:h('div')};
    const pages=Array.isArray(docs?.pages)?docs.pages:[];
    if(pages.length){
      const selected=new URL(location.href).searchParams.get('doc')||'Home';
      const picker=h('select',{'aria-label':'Red-SecretsXAI documentation page'},...pages.map(p=>h('option',{value:p.id,selected:p.id===selected},p.title||p.id)));
      picker.addEventListener('change',()=>context.navigate(productPath(product)+'?doc='+encodeURIComponent(picker.value)));
      const page=pages.find(p=>p.id===selected)||pages[0];picker.value=page.id;
      body.append(h('section',{class:'product-documentation'},h('h2',null,'Full documentation'),field('Guide',picker),renderMarkdown(page.markdown,{documentLinks:new Set(pages.map(p=>p.id)),path:productPath(product)})));
    }else body.append(alert('The documentation mirror is temporarily unavailable. The public documentation remains available from the product links.','warning'));
  }
  return {node:h('div',null,h('div',{class:'breadcrumbs wrap'},link('Products','/products/'+product.category),' / ',product.name),pageHead(STORES.find(s=>s[0]===product.category)?.[1]||'Product',product.name,product.summary),h('section',{class:'wrap section detail-layout'},body,sidebar))};
}
