import{createRequire} from"node:module";const require=createRequire(import.meta.url);
import{a as h}from"./chunk-FOCWZZDE.js";import{a as m,b as g,c as p}from"./chunk-QAYAQNCG.js";var y={name:"ln",summary:"make links between files",usage:"ln [OPTIONS] TARGET LINK_NAME",options:["-s      create a symbolic link instead of a hard link","-f      remove existing destination files","-n      treat LINK_NAME as a normal file if it is a symbolic link to a directory","-v      print name of each linked file","    --help display this help and exit"]},v={name:"ln",async execute(n,s){if(g(n))return m(y);let i=!1,f=!1,d=!1,t=0;for(;t<n.length&&n[t].startsWith("-");){let e=n[t];if(e==="-s"||e==="--symbolic")i=!0,t++;else if(e==="-f"||e==="--force")f=!0,t++;else if(e==="-v"||e==="--verbose")d=!0,t++;else if(e==="-n"||e==="--no-dereference")t++;else if(/^-[sfvn]+$/.test(e))e.includes("s")&&(i=!0),e.includes("f")&&(f=!0),e.includes("v")&&(d=!0),t++;else if(e==="--"){t++;break}else return p("ln",e)}let r=n.slice(t);if(r.length<2)return{stdout:"",stderr:`ln: missing file operand
`,exitCode:1};if(r.length>2)return{stdout:"",stderr:`ln: extra operand '${r[2]}'
`,exitCode:1};let l=r[0],o=r[1],a=s.fs.resolvePath(s.cwd,o);if(await s.fs.exists(a))if(f)try{await s.fs.rm(a,{force:!0})}catch{return{stdout:"",stderr:`ln: cannot remove '${o}': Permission denied
`,exitCode:1}}else return{stdout:"",stderr:`ln: failed to create ${i?"symbolic ":""}link '${o}': File exists
`,exitCode:1};try{if(i)await s.fs.symlink(l,a);else{let e=s.fs.resolvePath(s.cwd,l);if(!await s.fs.exists(e))return{stdout:"",stderr:`ln: failed to access '${l}': No such file or directory
`,exitCode:1};await s.fs.link(e,a)}}catch(e){let u=e;return u.message.includes("EPERM")?{stdout:"",stderr:i?`ln: failed to create symbolic link '${o}': Operation not permitted
`:`ln: '${l}': hard link not allowed for directory
`,exitCode:1}:{stdout:"",stderr:`ln: ${h(u.message)}
`,exitCode:1}}let c="";return d&&(c=`'${o}' -> '${l}'
`),{stdout:c,stderr:"",exitCode:0}}},$={name:"ln",flags:[{flag:"-s",type:"boolean"},{flag:"-f",type:"boolean"},{flag:"-n",type:"boolean"},{flag:"-v",type:"boolean"}],needsArgs:!0,minArgs:2};export{v as a,$ as b};
