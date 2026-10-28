var e=`diffs-container`,t=/(?=^From [a-f0-9]+ .+$)/m,n=/(?=^diff --git)/gm,r=/(?<=\n)/,i=/^(---|\+\+\+)\s+([^\t\r\n]+)/,a=/^index ([0-9a-f]+)\.\.([0-9a-f]+)(?: (\d+))?$/i,o=`header-prefix`,s=`header-filename-suffix`,c=`header-metadata`,l=`header-custom`,u={dark:`pierre-dark`,light:`pierre-light`},d=`data-theme-css`,f=`data-unsafe-css`,p=`data-diffs-scrollbar-measure`,m=`--diffs-scrollbar-gutter-measured`,h=1e5,g={hunkLineCount:50,lineHeight:20,diffHeaderHeight:44,spacing:8};({...g});var ee=Object.freeze({fromStart:0,fromEnd:0}),te={startingLine:0,totalLines:1/0,bufferBefore:0,bufferAfter:0},_=Object.freeze([]),v={startingLine:0,totalLines:0,bufferBefore:0,bufferAfter:0};function y(e,t){return e-(t===0?0:1)}function b(e,t){return y(e,t)+t}function x(e){let t=e.length;return e.charCodeAt(t-1)===10&&(t--,e.charCodeAt(t-1)===13&&t--),e.slice(0,t)}var S=class{diff(e,t,n={}){let r;typeof n==`function`?(r=n,n={}):`callback`in n&&(r=n.callback);let i=this.castInput(e,n),a=this.castInput(t,n),o=this.removeEmpty(this.tokenize(i,n)),s=this.removeEmpty(this.tokenize(a,n));return this.diffWithOptionsObj(o,s,n,r)}diffWithOptionsObj(e,t,n,r){let i=e=>{if(e=this.postProcess(e,n),r){setTimeout(function(){r(e)},0);return}return e},a=t.length,o=e.length,s=1,c=a+o;n.maxEditLength!=null&&(c=Math.min(c,n.maxEditLength));let l=n.timeout??1/0,u=Date.now()+l,d=[{oldPos:-1,lastComponent:void 0}],f=this.extractCommon(d[0],t,e,0,n);if(d[0].oldPos+1>=o&&f+1>=a)return i(this.buildValues(d[0].lastComponent,t,e));let p=-1/0,m=1/0,h=()=>{for(let r=Math.max(p,-s);r<=Math.min(m,s);r+=2){let s,c=d[r-1],l=d[r+1];c&&(d[r-1]=void 0);let u=!1;if(l){let e=l.oldPos-r;u=l&&0<=e&&e<a}let h=c&&c.oldPos+1<o;if(!u&&!h){d[r]=void 0;continue}if(s=!h||u&&c.oldPos<l.oldPos?this.addToPath(l,!0,!1,0,n):this.addToPath(c,!1,!0,1,n),f=this.extractCommon(s,t,e,r,n),s.oldPos+1>=o&&f+1>=a)return i(this.buildValues(s.lastComponent,t,e))||!0;d[r]=s,s.oldPos+1>=o&&(m=Math.min(m,r-1)),f+1>=a&&(p=Math.max(p,r+1))}s++};if(r)(function e(){setTimeout(function(){if(s>c||Date.now()>u)return r(void 0);h()||e()},0)})();else for(;s<=c&&Date.now()<=u;){let e=h();if(e)return e}}addToPath(e,t,n,r,i){let a=e.lastComponent;return a&&!i.oneChangePerToken&&a.added===t&&a.removed===n?{oldPos:e.oldPos+r,lastComponent:{count:a.count+1,added:t,removed:n,previousComponent:a.previousComponent}}:{oldPos:e.oldPos+r,lastComponent:{count:1,added:t,removed:n,previousComponent:a}}}extractCommon(e,t,n,r,i){let a=t.length,o=n.length,s=e.oldPos,c=s-r,l=0;for(;c+1<a&&s+1<o&&this.equals(n[s+1],t[c+1],i);)c++,s++,l++,i.oneChangePerToken&&(e.lastComponent={count:1,previousComponent:e.lastComponent,added:!1,removed:!1});return l&&!i.oneChangePerToken&&(e.lastComponent={count:l,previousComponent:e.lastComponent,added:!1,removed:!1}),e.oldPos=s,c}equals(e,t,n){return n.comparator?n.comparator(e,t):e===t||!!n.ignoreCase&&e.toLowerCase()===t.toLowerCase()}removeEmpty(e){let t=[];for(let n=0;n<e.length;n++)e[n]&&t.push(e[n]);return t}castInput(e,t){return e}tokenize(e,t){return Array.from(e)}join(e){return e.join(``)}postProcess(e,t){return e}get useLongestToken(){return!1}buildValues(e,t,n){let r=[],i;for(;e;)r.push(e),i=e.previousComponent,delete e.previousComponent,e=i;r.reverse();let a=r.length,o=0,s=0,c=0;for(;o<a;o++){let e=r[o];if(e.removed)e.value=this.join(n.slice(c,c+e.count)),c+=e.count;else{if(!e.added&&this.useLongestToken){let r=t.slice(s,s+e.count);r=r.map(function(e,t){let r=n[c+t];return r.length>e.length?r:e}),e.value=this.join(r)}else e.value=this.join(t.slice(s,s+e.count));s+=e.count,e.added||(c+=e.count)}}return r}},C=new class extends S{};function w(e,t,n){return C.diff(e,t,n)}function T(e,t){let n;for(n=0;n<e.length&&n<t.length;n++)if(e[n]!=t[n])return e.slice(0,n);return e.slice(0,n)}function E(e,t){let n;if(!e||!t||e[e.length-1]!=t[t.length-1])return``;for(n=0;n<e.length&&n<t.length;n++)if(e[e.length-(n+1)]!=t[t.length-(n+1)])return e.slice(-n);return e.slice(-n)}function D(e,t,n){if(e.slice(0,t.length)!=t)throw Error(`string ${JSON.stringify(e)} doesn't start with prefix ${JSON.stringify(t)}; this is a bug`);return n+e.slice(t.length)}function O(e,t,n){if(!t)return e+n;if(e.slice(-t.length)!=t)throw Error(`string ${JSON.stringify(e)} doesn't end with suffix ${JSON.stringify(t)}; this is a bug`);return e.slice(0,-t.length)+n}function k(e,t){return D(e,t,``)}function A(e,t){return O(e,t,``)}function j(e,t){return t.slice(0,M(e,t))}function M(e,t){let n=0;e.length>t.length&&(n=e.length-t.length);let r=t.length;e.length<t.length&&(r=e.length);let i=Array(r),a=0;i[0]=0;for(let e=1;e<r;e++){for(t[e]==t[a]?i[e]=i[a]:i[e]=a;a>0&&t[e]!=t[a];)a=i[a];t[e]==t[a]&&a++}a=0;for(let r=n;r<e.length;r++){for(;a>0&&e[r]!=t[a];)a=i[a];e[r]==t[a]&&a++}return a}function N(e,t){let n=[];for(let r of Array.from(t.segment(e))){let e=r.segment;n.length&&/\s/.test(n[n.length-1])&&/\s/.test(e)?n[n.length-1]+=e:n.push(e)}return n}function P(e,t){if(t)return I(e,t)[1];let n;for(n=e.length-1;n>=0&&e[n].match(/\s/);n--);return e.substring(n+1)}function F(e,t){if(t)return I(e,t)[0];let n=e.match(/^\s*/);return n?n[0]:``}function I(e,t){if(!t)return[F(e),P(e)];if(t.resolvedOptions().granularity!=`word`)throw Error(`The segmenter passed must have a granularity of "word"`);let n=N(e,t),r=n[0],i=n[n.length-1];return[/\s/.test(r)?r:``,/\s/.test(i)?i:``]}var L=`a-zA-Z0-9_\\u{AD}\\u{C0}-\\u{D6}\\u{D8}-\\u{F6}\\u{F8}-\\u{2C6}\\u{2C8}-\\u{2D7}\\u{2DE}-\\u{2FF}\\u{1E00}-\\u{1EFF}`,R=RegExp(`[${L}]+|\\s+|[^${L}]`,`ug`);new class extends S{equals(e,t,n){return n.ignoreCase&&(e=e.toLowerCase(),t=t.toLowerCase()),e.trim()===t.trim()}tokenize(e,t={}){let n;if(t.intlSegmenter){let r=t.intlSegmenter;if(r.resolvedOptions().granularity!=`word`)throw Error(`The segmenter passed must have a granularity of "word"`);n=N(e,r)}else n=e.match(R)||[];let r=[],i=null;return n.forEach(e=>{/\s/.test(e)?i==null?r.push(e):r.push(r.pop()+e):i!=null&&/\s/.test(i)?r[r.length-1]==i?r.push(r.pop()+e):r.push(i+e):r.push(e),i=e}),r}join(e){return e.map((e,t)=>t==0?e:e.replace(/^\s+/,``)).join(``)}postProcess(e,t){if(!e||t.oneChangePerToken)return e;let n=null,r=null,i=null;return e.forEach(e=>{e.added?r=e:e.removed?i=e:((r||i)&&z(n,i,r,e,t.intlSegmenter),n=e,r=null,i=null)}),(r||i)&&z(n,i,r,null,t.intlSegmenter),e}};function z(e,t,n,r,i){if(t&&n){let[a,o]=I(t.value,i),[s,c]=I(n.value,i);if(e){let r=T(a,s);e.value=O(e.value,s,r),t.value=k(t.value,r),n.value=k(n.value,r)}if(r){let e=E(o,c);r.value=D(r.value,c,e),t.value=A(t.value,e),n.value=A(n.value,e)}}else if(n){if(e){let e=F(n.value,i);n.value=n.value.substring(e.length)}if(r){let e=F(r.value,i);r.value=r.value.substring(e.length)}}else if(e&&r){let n=F(r.value,i),[a,o]=I(t.value,i),s=T(n,a);t.value=k(t.value,s);let c=E(k(n,s),o);t.value=A(t.value,c),r.value=D(r.value,n,c),e.value=O(e.value,n,n.slice(0,n.length-c.length))}else if(r){let e=F(r.value,i),n=j(P(t.value,i),e);t.value=A(t.value,n)}else if(e){let n=j(P(e.value,i),F(t.value,i));t.value=k(t.value,n)}}var B=new class extends S{tokenize(e){let t=RegExp(`(\\r?\\n)|[${L}]+|[^\\S\\n\\r]+|[^${L}]`,`ug`);return e.match(t)||[]}};function V(e,t,n){return B.diff(e,t,n)}var H=new class extends S{constructor(){super(...arguments),this.tokenize=W}equals(e,t,n){return n.ignoreWhitespace?((!n.newlineIsToken||!e.includes(`
`))&&(e=e.trim()),(!n.newlineIsToken||!t.includes(`
`))&&(t=t.trim())):n.ignoreNewlineAtEof&&!n.newlineIsToken&&(e.endsWith(`
`)&&(e=e.slice(0,-1)),t.endsWith(`
`)&&(t=t.slice(0,-1))),super.equals(e,t,n)}};function U(e,t,n){return H.diff(e,t,n)}function W(e,t){t.stripTrailingCr&&(e=e.replace(/\r\n/g,`
`));let n=[],r=e.split(/(\n|\r\n)/);r[r.length-1]||r.pop();for(let e=0;e<r.length;e++){let i=r[e];e%2&&!t.newlineIsToken?n[n.length-1]+=i:n.push(i)}return n}function G(e){return e==`.`||e==`!`||e==`?`}new class extends S{tokenize(e){let t=[],n=0;for(let r=0;r<e.length;r++){if(r==e.length-1){t.push(e.slice(n));break}if(G(e[r])&&e[r+1].match(/\s/)){for(t.push(e.slice(n,r+1)),r=n=r+1;e[r+1]?.match(/\s/);)r++;t.push(e.slice(n,r+1)),n=r+1}}return t}},new class extends S{tokenize(e){return e.split(/([{}:;,]|\s+)/)}},new class extends S{constructor(){super(...arguments),this.tokenize=W}get useLongestToken(){return!0}castInput(e,t){let{undefinedReplacement:n,stringifyReplacer:r=(e,t)=>t===void 0?n:t}=t;return typeof e==`string`?e:JSON.stringify(K(e,null,null,r),null,`  `)}equals(e,t,n){return super.equals(e.replace(/,([\r\n])/g,`$1`),t.replace(/,([\r\n])/g,`$1`),n)}};function K(e,t,n,r,i){t||=[],n||=[],r&&(e=r(i===void 0?``:i,e));let a;for(a=0;a<t.length;a+=1)if(t[a]===e)return n[a];let o;if(Object.prototype.toString.call(e)===`[object Array]`){for(t.push(e),o=Array(e.length),n.push(o),a=0;a<e.length;a+=1)o[a]=K(e[a],t,n,r,String(a));return t.pop(),n.pop(),o}if(e&&e.toJSON&&(e=e.toJSON()),typeof e==`object`&&e){t.push(e),o={},n.push(o);let i=[],s;for(s in e)Object.prototype.hasOwnProperty.call(e,s)&&i.push(s);for(i.sort(),a=0;a<i.length;a+=1)s=i[a],o[s]=K(e[s],t,n,r,s);t.pop(),n.pop()}else o=e;return o}new class extends S{tokenize(e){return e.slice()}join(e){return e}removeEmpty(e){return e}};function q(e){for(let t=0;t<e.length;t++)if(e[t]<` `||e[t]>`~`||e[t]===`"`||e[t]===`\\`)return!0;return!1}function J(e){if(!q(e))return e;let t=`"`,n=new TextEncoder().encode(e),r=0;for(;r<n.length;){let e=n[r];t+=e===7?`\\a`:e===8?`\\b`:e===9?`\\t`:e===10?`\\n`:e===11?`\\v`:e===12?`\\f`:e===13?`\\r`:e===34?`\\"`:e===92?`\\\\`:e>=32&&e<=126?String.fromCharCode(e):`\\`+e.toString(8).padStart(3,`0`),r++}return t+=`"`,t}var Y={includeIndex:!0,includeUnderline:!0,includeFileHeaders:!0};function X(e,t,n,r,i,a,o){let s;s=o?typeof o==`function`?{callback:o}:o:{},s.context===void 0&&(s.context=4);let c=s.context;if(s.newlineIsToken)throw Error(`newlineIsToken may not be used with patch-generation functions, only with diffing functions`);if(s.callback){let{callback:e}=s;U(n,r,Object.assign(Object.assign({},s),{callback:t=>{let n=l(t);e(n)}}))}else return l(U(n,r,s));function l(n){if(!n)return;n.push({value:``,lines:[]});function r(e){return e.map(function(e){return` `+e})}let o=[],s=0,l=0,u=[],d=1,f=1;for(let e=0;e<n.length;e++){let t=n[e],i=t.lines||re(t.value);if(t.lines=i,t.added||t.removed){if(!s){let t=n[e-1];s=d,l=f,t&&(u=c>0?r(t.lines.slice(-c)):[],s-=u.length,l-=u.length)}for(let e of i)u.push((t.added?`+`:`-`)+e);t.added?f+=i.length:d+=i.length}else{if(s){if(i.length<=c*2&&e<n.length-2)for(let e of r(i))u.push(e);else{let e=Math.min(i.length,c);for(let t of r(i.slice(0,e)))u.push(t);let t={oldStart:s,oldLines:d-s+e,newStart:l,newLines:f-l+e,lines:u};o.push(t),s=0,l=0,u=[]}}d+=i.length,f+=i.length}}for(let e of o)for(let t=0;t<e.lines.length;t++)e.lines[t].endsWith(`
`)?e.lines[t]=e.lines[t].slice(0,-1):(e.lines.splice(t+1,0,`\\ No newline at end of file`),t++);return{oldFileName:e,newFileName:t,oldHeader:i,newHeader:a,hunks:o}}}function Z(e,t){if(t||=Y,Array.isArray(e)){if(e.length>1&&!t.includeFileHeaders&&!e.every(e=>e.isGit))throw Error(`Cannot omit file headers on a multi-file patch. (The result would be unparseable; how would a tool trying to apply the patch know which changes are to which file?)`);return e.map(e=>Z(e,t)).join(`
`)}let n=[];if(e.isGit){if(t=Y,!e.oldFileName)throw Error(`oldFileName must be specified for Git patches`);if(!e.newFileName)throw Error(`newFileName must be specified for Git patches`);let r=e.oldFileName,i=e.newFileName;e.isCreate&&r===`/dev/null`?r=i.replace(/^b\//,`a/`):e.isDelete&&i===`/dev/null`&&(i=r.replace(/^a\//,`b/`)),n.push(`diff --git `+J(r)+` `+J(i)),e.isDelete&&n.push(`deleted file mode `+(e.oldMode??`100644`)),e.isCreate&&n.push(`new file mode `+(e.newMode??`100644`)),e.oldMode&&e.newMode&&!e.isDelete&&!e.isCreate&&(n.push(`old mode `+e.oldMode),n.push(`new mode `+e.newMode)),e.isRename&&(n.push(`rename from `+J((e.oldFileName??``).replace(/^a\//,``))),n.push(`rename to `+J((e.newFileName??``).replace(/^b\//,``)))),e.isCopy&&(n.push(`copy from `+J((e.oldFileName??``).replace(/^a\//,``))),n.push(`copy to `+J((e.newFileName??``).replace(/^b\//,``))))}else t.includeIndex&&e.oldFileName==e.newFileName&&e.oldFileName!==void 0&&n.push(`Index: `+e.oldFileName),t.includeUnderline&&n.push(`===================================================================`);let r=e.hunks.length>0;t.includeFileHeaders&&e.oldFileName!==void 0&&e.newFileName!==void 0&&(!e.isGit||r)&&(n.push(`--- `+J(e.oldFileName)+(e.oldHeader?`	`+e.oldHeader:``)),n.push(`+++ `+J(e.newFileName)+(e.newHeader?`	`+e.newHeader:``)));for(let t=0;t<e.hunks.length;t++){let r=e.hunks[t],i=r.oldLines===0?r.oldStart-1:r.oldStart,a=r.newLines===0?r.newStart-1:r.newStart;n.push(`@@ -`+i+`,`+r.oldLines+` +`+a+`,`+r.newLines+` @@`);for(let e of r.lines)n.push(e)}return n.join(`
`)+`
`}function ne(e,t,n,r,i,a,o){if(typeof o==`function`&&(o={callback:o}),o?.callback){let{callback:s}=o;X(e,t,n,r,i,a,Object.assign(Object.assign({},o),{callback:e=>{s(e?Z(e,o.headerOptions):void 0)}}))}else{let s=X(e,t,n,r,i,a,o);return s?Z(s,o?.headerOptions):void 0}}function re(e){let t=e.endsWith(`
`),n=e.split(`
`).map(e=>e+`
`);return t?n.pop():n.push(n.pop().slice(0,-1)),n}var Q=`
[data-annotation-slot] {
  padding: 12px;
  box-sizing: border-box;
}

[data-component="line-comment"] {
  position: absolute;
  right: 24px;
  z-index: var(--line-comment-z, 30);
}

[data-component="line-comment"][data-inline] {
  position: relative;
  right: auto;
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: flex-start;
}

[data-component="line-comment"][data-open] {
  z-index: var(--line-comment-open-z, 100);
}

[data-component="line-comment"] [data-slot="line-comment-button"] {
  width: 20px;
  height: 20px;
  border-radius: var(--radius-md);
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--icon-interactive-base);
  box-shadow: var(--shadow-xs);
  cursor: default;
  border: none;
}

[data-component="line-comment"] [data-slot="icon-svg"] {
  color: var(--white);
}

[data-component="line-comment"] [data-slot="line-comment-icon"] {
  width: 12px;
  height: 12px;
  color: var(--white);
}

[data-component="line-comment"] [data-slot="line-comment-button"]:focus {
  outline: none;
}

[data-component="line-comment"] [data-slot="line-comment-button"]:focus-visible {
  box-shadow: var(--shadow-xs-border-focus);
}

[data-component="line-comment"] [data-slot="line-comment-popover"] {
  position: absolute;
  top: calc(100% + 4px);
  right: -8px;
  z-index: var(--line-comment-popover-z, 40);
  min-width: 200px;
  max-width: none;
  box-sizing: border-box;
  border-radius: 8px;
  background: var(--surface-raised-stronger-non-alpha);
  box-shadow: var(--shadow-xxs-border);
  padding: 12px;
}

[data-component="line-comment"][data-inline] [data-slot="line-comment-popover"] {
  position: relative;
  top: auto;
  right: auto;
  margin-left: 8px;
  flex: 1 1 0%;
  width: auto;
  max-width: 100%;
  min-width: 0;
}

[data-component="line-comment"][data-inline] [data-slot="line-comment-popover"][data-inline-body] {
  margin-left: 0;
}

[data-component="line-comment"][data-inline][data-variant="default"] [data-slot="line-comment-popover"][data-inline-body] {
  cursor: pointer;
}

[data-component="line-comment"][data-variant="editor"] [data-slot="line-comment-popover"] {
  width: 380px;
  max-width: none;
  padding: 8px;
  border-radius: 14px;
}

[data-component="line-comment"][data-inline][data-variant="editor"] [data-slot="line-comment-popover"] {
  width: 100%;
}

[data-component="line-comment"] [data-slot="line-comment-content"] {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
  min-width: 0;
}

[data-component="line-comment"] [data-slot="line-comment-head"] {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
}

[data-component="line-comment"] [data-slot="line-comment-text"] {
  flex: 1;
  min-width: 0;
  font-family: var(--font-family-sans);
  font-size: var(--font-size-base);
  font-weight: var(--font-weight-regular);
  line-height: var(--line-height-x-large);
  letter-spacing: var(--letter-spacing-normal);
  color: var(--v2-text-text-base);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

[data-component="line-comment"] [data-slot="line-comment-tools"] {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  min-width: 0;
}

[data-component="line-comment"] [data-slot="line-comment-label"],
[data-component="line-comment"] [data-slot="line-comment-editor-label"] {
  font-family: var(--font-family-sans);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-medium);
  line-height: var(--line-height-large);
  letter-spacing: var(--letter-spacing-normal);
  color: var(--v2-text-text-faint);
  min-width: 0;
  white-space: normal;
  overflow-wrap: anywhere;
}

[data-component="line-comment"] [data-slot="line-comment-editor"] {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
  min-width: 0;
}

[data-component="line-comment"] [data-slot="line-comment-textarea"] {
  width: 100%;
  box-sizing: border-box;
  resize: vertical;
  padding: 8px;
  border-radius: var(--radius-md);
  background: var(--surface-base);
  border: 1px solid var(--v2-border-border-base);
  color: var(--v2-text-text-base);
  font-family: var(--font-family-sans);
  font-size: var(--font-size-small);
  line-height: var(--line-height-large);
}

[data-component="line-comment"] [data-slot="line-comment-textarea"]:focus {
  outline: none;
  box-shadow: var(--shadow-xs-border-select);
}

[data-component="line-comment"] [data-slot="line-comment-mention-list"] {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 180px;
  overflow: auto;
  padding: 4px;
  border: 1px solid var(--v2-border-border-base);
  border-radius: var(--radius-md);
  background: var(--surface-base);
}

[data-component="line-comment"] [data-slot="line-comment-mention-item"] {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
  padding: 6px 8px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--v2-text-text-base);
  text-align: left;
}

[data-component="line-comment"] [data-slot="line-comment-mention-item"][data-active] {
  background: var(--surface-raised-base-hover);
}

[data-component="line-comment"] [data-slot="line-comment-mention-path"] {
  display: flex;
  align-items: center;
  min-width: 0;
  font-family: var(--font-family-sans);
  font-size: var(--font-size-small);
  line-height: var(--line-height-large);
}

[data-component="line-comment"] [data-slot="line-comment-mention-dir"] {
  min-width: 0;
  color: var(--v2-text-text-faint);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

[data-component="line-comment"] [data-slot="line-comment-mention-file"] {
  color: var(--v2-text-text-base);
  white-space: nowrap;
}

[data-component="line-comment"] [data-slot="line-comment-actions"] {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  padding-left: 8px;
  min-width: 0;
}

[data-component="line-comment"] [data-slot="line-comment-editor-label"] {
  flex: 1 1 220px;
  margin-right: auto;
}

[data-component="line-comment"] [data-slot="line-comment-action"] {
  border: 1px solid var(--v2-border-border-base);
  background: var(--surface-base);
  color: var(--v2-text-text-base);
  border-radius: var(--radius-md);
  height: 28px;
  padding: 0 10px;
  font-family: var(--font-family-sans);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-medium);
}

[data-component="line-comment"] [data-slot="line-comment-action"][data-variant="ghost"] {
  background: transparent;
}

[data-component="line-comment"] [data-slot="line-comment-action"][data-variant="primary"] {
  background: var(--v2-background-bg-contrast);
  border-color: var(--v2-background-bg-contrast);
  color: var(--v2-text-text-contrast);
}

[data-component="line-comment"] [data-slot="line-comment-action"]:disabled {
  opacity: 0.5;
  pointer-events: none;
}
`,$=!1;function ie(){if($||typeof document>`u`)return;let e=`opencode-line-comment-styles`;if(document.getElementById(e)){$=!0;return}let t=document.createElement(`style`);t.id=e,t.textContent=Q,document.head.appendChild(t),$=!0}export{s as C,r as D,a as E,d as O,n as S,o as T,p as _,w as a,v as b,y as c,ee as d,te as f,m as g,g as h,V as i,f as k,t as l,h as m,Q as n,x as o,u as p,ne as r,b as s,ie as t,l as u,e as v,c as w,i as x,_ as y};