import{G as e,O as t,S as n,U as r,_ as i,w as a}from"./solid-Zk_90kIj.js";import{G as o,T as s,at as c,ot as l,y as u}from"./solid-web-7fRPiAhO.js";import{l as ee,r as d}from"./gui-D49Ae_jN.js";import{r as f}from"./icon-th9d3537.js";import{a as p,o as m}from"./path-B76lC1Bb.js";import{t as h}from"./FileTree-DFGAPl4p.js";import{a as te,n as ne}from"./open-in-app-bxJ49AGM.js";import{i as re,n as g,o as _,r as v,t as y}from"./tree-model--Rm7PzY6.js";var b=e=>{let t=e.querySelector(`[data-component="file-icon"]`)??e.querySelector(`svg`),n=e.querySelector(`span`);if(!t||!n)return;let r=document.createElement(`div`);return r.className=`flex items-center gap-x-2 px-2 py-1 bg-surface-raised-base rounded-md border border-v2-border-border-base text-12-regular text-text-strong`,r.style.position=`absolute`,r.style.top=`-1000px`,r.innerHTML=t.outerHTML+n.outerHTML,r},x=e=>{let t=b(e.currentTarget);t&&(document.body.appendChild(t),e.dataTransfer?.setDragImage(t,0,12),setTimeout(()=>document.body.removeChild(t),0))};function ie(e,t){e.dataTransfer?.setData(`text/plain`,`file:${t}`),e.dataTransfer?.setData(`text/uri-list`,p(t)),e.dataTransfer&&(e.dataTransfer.effectAllowed=`copy`),x(e)}var S=c(`<div data-component=file-tree-v2 class=group/file-tree-v2>`),C=`
  :host {
    color-scheme: inherit;
    --trees-font-family: var(--font-family-sans); --trees-font-size: 12px;
    --trees-fg: var(--v2-text-text-muted); --trees-fg-muted: var(--v2-text-text-muted);
    --trees-bg: var(--v2-background-bg-base); --trees-bg-muted: var(--v2-overlay-simple-overlay-hover);
    --trees-border-color: color-mix(in srgb, var(--v2-border-border-muted) 50%, transparent);
    --trees-indent-guide-bg: color-mix(in srgb, var(--v2-border-border-muted) 50%, transparent);
    --trees-selected-bg: var(--v2-overlay-simple-overlay-pressed); --trees-selected-fg: var(--v2-text-text-base);
    --trees-selected-focused-border-color: transparent; --trees-focus-ring-color: transparent; --trees-focus-ring-width: 0px;
    --trees-git-modified-color: var(--v2-state-fg-info); --trees-git-renamed-color: var(--v2-state-fg-info);
    --trees-git-added-color: var(--v2-state-fg-success); --trees-git-untracked-color: var(--v2-state-fg-success);
    --trees-git-deleted-color: var(--v2-state-fg-danger); --trees-git-ignored-color: var(--v2-text-text-faint);
    --trees-padding-inline: 8px; --trees-item-margin-x: 0px;
  }
  [data-file-tree-virtualized-root="true"] { padding-block-start: 4px; }
  /* The patched scroll limit counts this padding and the horizontal scrollbar, so neither fights the wheel. */
  [data-file-tree-virtualized-scroll="true"] { padding-block: 0 8px; padding-inline: 8px; overflow-x: auto; }
  /* Long rows scroll sideways rather than clip; every row is as wide as the widest one seen in this row set. */
  [data-file-tree-virtualized-list="true"] { width: max-content; min-width: max(100%, var(--file-tree-min-width, 0px)); }
  [data-item-section="spacing"] {
    padding-left: 0;
    padding-inline-start: calc(var(--trees-icon-width) / 2 - 0.5px);
  }
  [data-item-section="spacing-item"] {
    border-left: none; margin-right: 0;
    border-inline-start: 1px solid var(--trees-indent-guide-bg);
    margin-inline-end: calc(var(--trees-level-gap) - 1px);
    transform: none;
  }
  [data-item-section="spacing-item"] + [data-item-section="spacing-item"] {
    margin-left: 0;
    margin-inline-start: calc(var(--trees-item-row-gap) + var(--trees-icon-width) / 2 - 0.5px);
  }
  [data-type="item"][aria-expanded="false"] [data-item-section="icon"] > svg:first-child {
    transform: translateY(var(--trees-icon-nudge));
  }
  [data-type="item"][aria-expanded="true"] [data-item-section="icon"] > svg:first-child {
    transform: translateY(var(--trees-icon-nudge)) rotate(90deg);
  }
  :host(:dir(rtl)) [data-type="item"][aria-expanded="false"] [data-item-section="icon"] > svg:first-child {
    transform: translateY(var(--trees-icon-nudge)) rotate(180deg);
  }
  button[data-type="item"] {
    --row-overlay: transparent;
    box-shadow: none !important; cursor: inherit; font-weight: 500;
  }
  button[data-type="item"]:hover, button[data-type="item"][data-item-context-hover="true"] {
    --row-overlay: var(--v2-overlay-simple-overlay-hover);
    background-color: var(--row-overlay) !important;
  }
  button[data-type="item"][data-item-selected="true"], button[data-type="item"][data-item-selected="true"]:hover {
    --row-overlay: var(--v2-overlay-simple-overlay-pressed);
    color: var(--v2-text-text-base) !important; background-color: var(--row-overlay) !important;
  }
  button[data-type="item"]:focus-visible, button[data-type="item"][data-item-focused="true"] {
    outline: none !important; box-shadow: none !important;
  }
  button[data-type="item"]::before,
  [data-item-section="decoration"]:empty { display: none !important; }
  /* A folder holding changes keeps its lane, so the row still ends in the sticky end, but shows no dot. */
  [data-item-contains-git-change="true"]:not([data-item-git-status]) > [data-item-section="git"] > * { visibility: hidden; }
  [data-type="item"]:not([data-item-selected="true"]) > [data-item-section="icon"] > * { color: var(--v2-icon-icon-muted) !important; }
  [data-item-git-status]:not([data-item-git-status="ignored"]) > [data-item-section="content"] { color: inherit; }
  [data-item-section="content"] { flex: 1 0 auto; max-width: none; overflow: visible; white-space: nowrap; }
  [data-item-section="decoration"] { flex: 0 0 auto; color: var(--v2-text-text-weak, var(--v2-text-text-muted)); font-size: 12px; font-weight: 400; }
  [data-item-section="git"] {
    font-size: 11px; font-weight: 530; line-height: 1; letter-spacing: -0.04px;
    font-variant-numeric: tabular-nums lining-nums; font-feature-settings: "tnum" on, "lnum" on;
  }
  /* Every row ends in its change letter or an empty lane that sticks inside the pane while long rows scroll. It fades
     the name out over 16px and redraws the row's rounded end, so a row that runs past the pane looks like one that
     fits. */
  [data-item-section="git"] {
    position: sticky; inset-inline-end: var(--trees-item-padding-x);
    z-index: 1; align-self: stretch;
  }
  [data-item-section="git"]::before, [data-item-section="git"]::after {
    content: ""; position: absolute; inset-block: 0; z-index: -1; pointer-events: none;
  }
  /* The pane from the lane to the scroller's 8px edge, under the rounded end's corners. It stays within the row, as rows
     touch and a taller cover would paint over their neighbours. */
  [data-item-section="git"]::before {
    inset-inline: 0 calc(-1 * (var(--trees-item-padding-x) + 8px));
    background: var(--trees-bg);
  }
  /* The row from the 16px fade over the name through its rounded end. */
  [data-item-section="git"]::after {
    inset-inline: -16px calc(-1 * var(--trees-item-padding-x));
    border-start-end-radius: var(--trees-border-radius); border-end-end-radius: var(--trees-border-radius);
    background:
      linear-gradient(to right, transparent, var(--row-overlay) 16px),
      linear-gradient(to right, transparent, var(--trees-bg) 16px);
  }
  :host(:dir(rtl)) [data-item-section="git"]::after {
    background:
      linear-gradient(to left, transparent, var(--row-overlay) 16px),
      linear-gradient(to left, transparent, var(--trees-bg) 16px);
  }
`;function w(e,t){if(e===t)return!0;let n=e?.size??0;if(n!==(t?.size??0))return!1;if(n===0)return!0;for(let[n,r]of e??[])if(t?.get(n)!==r)return!1;return!0}function T(e){return!!e?.isDirectory()}function E(e){for(let t of e.composedPath())if(t instanceof HTMLElement&&t.dataset.type===`item`&&t.dataset.itemPath)return t}function D(e){let t=E(e);return t?.dataset.itemType===`file`?t.dataset.itemPath:void 0}function O(s){let c=ee(),p=s.screen.file,b=c.desktop?te({session:s.session,path:()=>p.root}):void 0,x=()=>s.allowed===void 0,O=()=>s.draggable??!0,k=()=>_(s.active??``),A=e=>p.tree.state(e)?.expanded??!1,j=n(()=>x()?void 0:s.allowed??[],void 0,{equals:(e,t)=>e===t||e!==void 0&&t!==void 0&&e.length===t.length&&e.every((e,n)=>e===t[n])}),M=n(()=>{let e=j();return e?y(e):void 0}),N=n(()=>x()?v(e=>p.tree.list(e),A):void 0),[P,ae]=t(!1),F=new Set,I={active:``},L,R,z=!1,B=!1,V=new Map,H,U,W,G,K=p.root,q,J=0,Y=()=>{J=0,q?.style.removeProperty(`--file-tree-min-width`)},X=e=>{let t=_(e);return(x()?N()?.nodesByPath.get(t)?.path:M()?.originalByPath.get(t))??t},Z=e=>{if(!R||z)return;let t=e?R.getItem(e):null,n=t&&!t.isDirectory()?e:``,r=R.getSelectedPaths();if(!(n?r.length===1&&r[0]===n:r.length===0)){z=!0;for(let e of r)e!==n&&R.getItem(e)?.deselect();n&&R.getItem(n)?.select(),z=!1}},Q=e=>{if(R){B=!0;for(let t of e.directories){if(e.untrackedDirs.has(t)?p.tree.state(t)?.expanded===!0:p.tree.state(t)?.expanded!==!1)continue;let n=R.getItem(t);T(n)&&n.isExpanded()&&n.collapse()}B=!1}},$=e=>{let t=_(e),n=x()?N()?.nodesByPath.get(t):void 0;if(n)return n;let r=M()?.originalByPath.get(t)??t;return{name:t.split(`/`).at(-1)||t,path:r,absolute:r,type:`file`,ignored:!1}};return e(()=>{if(!L)return;let e=x(),t=M(),n=N(),i=e?n?.paths??[]:t?.paths??[],a=g(s.kinds,n?.ignoredPaths),o=Array.from(a,([e,t])=>({path:e,status:t})),c=k();if(e)for(let e of i)F.add(e);V=a,H=t,U=s.labels,W=s.kinds,G=n?.ignoredPaths,K=p.root,R=new h({paths:i,flattenEmptyDirectories:!1,initialExpansion:e?`closed`:`open`,initialExpandedPaths:e?n?.expandedDirs:void 0,initialSelectedPaths:c?[c]:void 0,sort:e?(e,t)=>{let n=N()?.orderByPath;return(n?.get(_(e.path))??0)-(n?.get(_(t.path))??0)}:`default`,icons:{set:`complete`,spriteSheet:`<svg data-icon-sprite aria-hidden="true" width="0" height="0" xmlns="http://www.w3.org/2000/svg"><symbol id="outline-chevron-right" viewBox="${f(`outline-chevron-right`).viewBox}" fill="none">${f(`outline-chevron-right`).body}</symbol></svg>`,remap:{"file-tree-icon-chevron":{name:`outline-chevron-right`,width:16,height:16,viewBox:`0 0 16 16`}}},itemHeight:28,middleTruncate:!1,stickyFolders:!1,gitStatus:o.length>0?o:void 0,dragAndDrop:O()?{canDrag:()=>!0,canDrop:()=>!1}:!1,unsafeCSS:C,renderRowDecoration:({row:e})=>{if(e.kind!==`directory`)return null;let t=s.labels?.get(_(e.path));return t?{text:t}:null},onExpansionChange(e){if(Y(),B)return;let t=X(e.path);e.expanded?p.tree.expand(t,x()?void 0:{list:!1}):p.tree.collapse(t)},onSelectionChange:()=>Z(k())}),!e&&t&&Q(t),R.render({containerWrapper:L}),R.getFileTreeContainer()?.classList.add(`opencode-pierre-file-tree`),q=R.getFileTreeContainer();let l=q?.shadowRoot?.querySelector(`[data-file-tree-virtualized-list]`),u=l?.closest(`[data-file-tree-virtualized-scroll]`);if(l&&u){let e=new ResizeObserver(()=>{if(u.scrollWidth<=u.clientWidth)return;let e=l.getBoundingClientRect().width;e<=J||(J=e,q?.style.setProperty(`--file-tree-min-width`,`${e}px`))});e.observe(l),r(()=>e.disconnect())}ae(!0),r(()=>{R?.cleanUp(),R=void 0})}),d(()=>{if(!x())return;let e=p.tree.state(``);return e?.loaded?void 0:{root:e}},()=>void p.tree.sync(``)),d(()=>P()&&!x()?M():void 0,e=>{!R||e===H||(H=e,Z(``),Y(),R.resetPaths(e.paths),Q(e),Z(k()))}),d(()=>P()&&!x()?{labels:s.labels}:void 0,e=>{!R||!L||w(U,e.labels)||(U=e.labels,R.render({containerWrapper:L}))},{equals:(e,t)=>e!==void 0&&t!==void 0&&w(e.labels,t.labels)}),d(()=>P()&&x()?{root:p.root,model:N()}:void 0,e=>{if(!R)return;e.root!==K&&(K=e.root,F.clear(),R.resetPaths([]));let t=new Set(e.model.paths),n=new Set(e.model.expandedDirs),r=[];for(let e of F){if(t.has(e))continue;let i=e.endsWith(`/`)?e.slice(0,-1):e,a=i.lastIndexOf(`/`),o=a===-1?``:`${i.slice(0,a+1)}`;(o===``||n.has(o))&&(r.push({type:`remove`,path:e,recursive:e.endsWith(`/`)}),F.delete(e))}for(let t of e.model.paths)F.has(t)||(F.add(t),r.push({type:`add`,path:t}));r.length>0&&(Y(),R.batch(r)),B=!0;for(let t of e.model.expandedDirs){let e=R.getItem(t);T(e)&&!e.isExpanded()&&e.expand()}B=!1,Z(k())},{equals:(e,t)=>e?.root===t?.root&&e?.model===t?.model}),d(()=>P()?{kinds:s.kinds,ignored:N()?.ignoredPaths}:void 0,e=>{if(!R||w(e.kinds,W)&&e.ignored===G)return;W=e.kinds,G=e.ignored;let t=g(e.kinds,e.ignored),n=re(V,t);V=t,(n.remove.length>0||n.set.length>0)&&R.applyGitStatusPatch(n)},{equals:(e,t)=>e!==void 0&&t!==void 0&&w(e.kinds,t.kinds)&&e.ignored===t.ignored}),d(()=>{if(!P())return{path:``,present:!1};let e=k();M(),N();let t=e?R?.getItem(e):null;return{path:e,present:!!(t&&!t.isDirectory())}},e=>{if(Z(e.path),!e.path){I.active=``;return}!e.present||I.active===e.path||(I.active=e.path,queueMicrotask(()=>{if(!R)return;let t=R.getFileTreeContainer()?.shadowRoot?.querySelector(`[data-file-tree-virtualized-scroll]`),n=t?.scrollTop??0,r=t?.clientHeight||600,i=Math.floor(n/28),a=Math.max(i,Math.floor((n+r-1)/28));R.getVisibleRows(i,a).some(t=>t.path===e.path)||R.scrollToPath(e.path,{focus:!1,offset:`nearest`})}))},{equals:(e,t)=>e.path===t.path&&e.present===t.present}),i(ne,{state:b,resolvePath:e=>{let t=D(e);if(!t)return;let n=$(t);return m(p.root,n.absolute||n.path)},fit:!0,get children(){var e=S();e.addEventListener(`dragstart`,e=>{if(!O())return;let t=E(e);t?.dataset.itemPath&&ie(e,X(t.dataset.itemPath))}),e.$$dblclick=e=>{let t=D(e);t&&s.onFileDoubleClick?.($(t))},e.$$click=e=>{let t=D(e);t&&s.onFileClick?.($(t))},u(e,`keydown`,{capture:!0,handleEvent(e){if(!R||!L?.matches(`:dir(rtl)`)||e.key!==`ArrowLeft`&&e.key!==`ArrowRight`||e.altKey||e.ctrlKey||e.metaKey||e.shiftKey)return;e.preventDefault(),e.stopPropagation();let t=R.getFocusedItem();if(!t){R.focusFirstItem();return}let n=T(t)?t:void 0;if(e.key===`ArrowLeft`){if(!n)return;n.isExpanded()?R.focusNextItem():n.expand();return}n?.isExpanded()?n.collapse():R.focusParentItem()}});var t=L;return typeof t==`function`?l(t,e):L=e,a(()=>o(e,`data-total-rows`,x()?N()?.paths.length??0:M()?.total??0)),e}})}s([`click`,`dblclick`]);export{O as t};