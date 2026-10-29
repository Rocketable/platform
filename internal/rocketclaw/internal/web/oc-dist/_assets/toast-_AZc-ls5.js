import{$ as e,B as t,G as n,H as r,O as i,S as a,U as o,X as s,_ as c,l,o as u,w as d,x as f,y as p}from"./solid-Zk_90kIj.js";import{F as m,G as h,R as g,S as _,T as v,X as y,at as b,it as ee,ot as x,t as S}from"./solid-web-7fRPiAhO.js";import{i as te,o as C,s as w}from"./store-Cm9XWJCd.js";import{r as T}from"./LP6E37CW-Cmfk3idP.js";import{i as E}from"./i18n-IWuPVMrT.js";/* empty css               */import{n as D}from"./reduced-BUzxU3r5.js";var O=b(`<div><div class=sonner-spinner>`),k=b(`<div class=sonner-loading-bar>`),A=b(`<svg xmlns=http://www.w3.org/2000/svg viewBox="0 0 20 20"fill=currentColor height=20 width=20><path fill-rule=evenodd d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.857-9.809a.75.75 0 00-1.214-.882l-3.483 4.79-1.88-1.88a.75.75 0 10-1.06 1.061l2.5 2.5a.75.75 0 001.137-.089l4-5.5z"clip-rule=evenodd>`),j=b(`<svg xmlns=http://www.w3.org/2000/svg viewBox="0 0 24 24"fill=currentColor height=20 width=20><path fill-rule=evenodd d="M9.401 3.003c1.155-2 4.043-2 5.197 0l7.355 12.748c1.154 2-.29 4.5-2.599 4.5H4.645c-2.309 0-3.752-2.5-2.598-4.5L9.4 3.003zM12 8.25a.75.75 0 01.75.75v3.75a.75.75 0 01-1.5 0V9a.75.75 0 01.75-.75zm0 8.25a.75.75 0 100-1.5.75.75 0 000 1.5z"clip-rule=evenodd>`),M=b(`<svg xmlns=http://www.w3.org/2000/svg viewBox="0 0 20 20"fill=currentColor height=20 width=20><path fill-rule=evenodd d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a.75.75 0 000 1.5h.253a.25.25 0 01.244.304l-.459 2.066A1.75 1.75 0 0010.747 15H11a.75.75 0 000-1.5h-.253a.25.25 0 01-.244-.304l.459-2.066A1.75 1.75 0 009.253 9H9z"clip-rule=evenodd>`),N=b(`<svg xmlns=http://www.w3.org/2000/svg viewBox="0 0 20 20"fill=currentColor height=20 width=20><path fill-rule=evenodd d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-8-5a.75.75 0 01.75.75v4.5a.75.75 0 01-1.5 0v-4.5A.75.75 0 0110 5zm0 10a1 1 0 100-2 1 1 0 000 2z"clip-rule=evenodd>`),P=b(`<svg xmlns=http://www.w3.org/2000/svg width=12 height=12 viewBox="0 0 24 24"fill=none stroke=currentColor stroke-width=1.5 stroke-linecap=round stroke-linejoin=round><line x1=18 y1=6 x2=6 y2=18></line><line x1=6 y1=6 x2=18 y2=18>`),F=b(`<div>`),ne=b(`<button data-close-button>`),re=b(`<div data-icon>`),I=b(`<div data-description>`),ie=b(`<li tabindex=0 data-sonner-toast><div data-content><div data-title>`),ae=b(`<button data-button data-cancel>`),oe=b(`<button data-button data-action>`),se=b(`<section tabindex=-1 aria-live=polite aria-relevant="additions text"aria-atomic=false data-react-aria-top-layer>`),L=b(`<ol tabindex=-1 data-sonner-toaster style=--width:356px>`);function R(e,{insertAt:t}={}){if(!e||typeof document>`u`)return;let n=document.head||document.getElementsByTagName(`head`)[0],r=document.createElement(`style`);r.type=`text/css`,t===`top`&&n.firstChild?n.insertBefore(r,n.firstChild):n.appendChild(r),r.styleSheet?r.styleSheet.cssText=e:r.appendChild(document.createTextNode(e))}R(`html[dir=ltr],
[data-sonner-toaster][dir=ltr] {
  --toast-icon-margin-start: -3px;
  --toast-icon-margin-end: 4px;
  --toast-svg-margin-start: -1px;
  --toast-svg-margin-end: 0px;
  --toast-button-margin-start: auto;
  --toast-button-margin-end: 0;
  --toast-close-button-start: 0;
  --toast-close-button-end: unset;
  --toast-close-button-transform: translate(-35%, -35%);
}
html[dir=rtl],
[data-sonner-toaster][dir=rtl] {
  --toast-icon-margin-start: 4px;
  --toast-icon-margin-end: -3px;
  --toast-svg-margin-start: 0px;
  --toast-svg-margin-end: -1px;
  --toast-button-margin-start: 0;
  --toast-button-margin-end: auto;
  --toast-close-button-start: unset;
  --toast-close-button-end: 0;
  --toast-close-button-transform: translate(35%, -35%);
}
[data-sonner-toaster] {
  position: fixed;
  width: var(--width);
  font-family:
    ui-sans-serif,
    system-ui,
    -apple-system,
    BlinkMacSystemFont,
    Segoe UI,
    Roboto,
    Helvetica Neue,
    Arial,
    Noto Sans,
    sans-serif,
    Apple Color Emoji,
    Segoe UI Emoji,
    Segoe UI Symbol,
    Noto Color Emoji;
  --gray1: hsl(0, 0%, 99%);
  --gray2: hsl(0, 0%, 97.3%);
  --gray3: hsl(0, 0%, 95.1%);
  --gray4: hsl(0, 0%, 93%);
  --gray5: hsl(0, 0%, 90.9%);
  --gray6: hsl(0, 0%, 88.7%);
  --gray7: hsl(0, 0%, 85.8%);
  --gray8: hsl(0, 0%, 78%);
  --gray9: hsl(0, 0%, 56.1%);
  --gray10: hsl(0, 0%, 52.3%);
  --gray11: hsl(0, 0%, 43.5%);
  --gray12: hsl(0, 0%, 9%);
  --border-radius: 8px;
  box-sizing: border-box;
  padding: 0;
  margin: 0;
  list-style: none;
  outline: none;
  z-index: 999999999;
  transition: transform 400ms ease;
}
@media (hover: none) and (pointer: coarse) {
  [data-sonner-toaster][data-lifted=true] {
    transform: none;
  }
}
[data-sonner-toaster][data-x-position=right] {
  right: var(--offset-right);
}
[data-sonner-toaster][data-x-position=left] {
  left: var(--offset-left);
}
[data-sonner-toaster][data-x-position=center] {
  left: 50%;
  transform: translateX(-50%);
}
[data-sonner-toaster][data-y-position=top] {
  top: var(--offset-top);
}
[data-sonner-toaster][data-y-position=bottom] {
  bottom: var(--offset-bottom);
}
[data-sonner-toast] {
  --y: translateY(100%);
  --lift-amount: calc(var(--lift) * var(--gap));
  z-index: var(--z-index);
  position: absolute;
  opacity: 0;
  transform: var(--y);
  touch-action: none;
  transition:
    transform 400ms,
    opacity 400ms,
    height 400ms,
    box-shadow 200ms;
  box-sizing: border-box;
  outline: none;
  overflow-wrap: anywhere;
}
[data-sonner-toast][data-styled=true] {
  padding: 16px;
  background: var(--normal-bg);
  border: 1px solid var(--normal-border);
  color: var(--normal-text);
  border-radius: var(--border-radius);
  box-shadow: 0px 4px 12px rgba(0, 0, 0, 0.1);
  width: var(--width);
  font-size: 13px;
  display: flex;
  align-items: center;
  gap: 6px;
}
[data-sonner-toast]:focus-visible {
  box-shadow: 0px 4px 12px rgba(0, 0, 0, 0.1), 0 0 0 2px rgba(0, 0, 0, 0.2);
}
[data-sonner-toast][data-y-position=top] {
  top: 0;
  --y: translateY(-100%);
  --lift: 1;
  --lift-amount: calc(1 * var(--gap));
}
[data-sonner-toast][data-y-position=bottom] {
  bottom: 0;
  --y: translateY(100%);
  --lift: -1;
  --lift-amount: calc(var(--lift) * var(--gap));
}
[data-sonner-toast][data-styled=true] [data-description] {
  font-weight: 400;
  line-height: 1.4;
  color: #3f3f3f;
}
[data-rich-colors=true][data-sonner-toast][data-styled=true] [data-description] {
  color: inherit;
}
[data-sonner-toaster][data-sonner-theme=dark] [data-description] {
  color: hsl(0, 0%, 91%);
}
[data-sonner-toast][data-styled=true] [data-title] {
  font-weight: 500;
  line-height: 1.5;
  color: inherit;
}
[data-sonner-toast][data-styled=true] [data-icon] {
  display: flex;
  height: 16px;
  width: 16px;
  position: relative;
  justify-content: flex-start;
  align-items: center;
  flex-shrink: 0;
  margin-left: var(--toast-icon-margin-start);
  margin-right: var(--toast-icon-margin-end);
}
[data-sonner-toast][data-promise=true] [data-icon] > svg {
  opacity: 0;
  transform: scale(0.8);
  transform-origin: center;
  animation: sonner-fade-in 300ms ease forwards;
}
[data-sonner-toast][data-styled=true] [data-icon] > * {
  flex-shrink: 0;
}
[data-sonner-toast][data-styled=true] [data-icon] svg {
  margin-left: var(--toast-svg-margin-start);
  margin-right: var(--toast-svg-margin-end);
}
[data-sonner-toast][data-styled=true] [data-content] {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
[data-sonner-toast][data-styled=true] [data-button] {
  border-radius: 4px;
  padding-left: 8px;
  padding-right: 8px;
  height: 24px;
  font-size: 12px;
  color: var(--normal-bg);
  background: var(--normal-text);
  margin-left: var(--toast-button-margin-start);
  margin-right: var(--toast-button-margin-end);
  border: none;
  font-weight: 500;
  cursor: pointer;
  outline: none;
  display: flex;
  align-items: center;
  flex-shrink: 0;
  transition: opacity 400ms, box-shadow 200ms;
}
[data-sonner-toast][data-styled=true] [data-button]:focus-visible {
  box-shadow: 0 0 0 2px rgba(0, 0, 0, 0.4);
}
[data-sonner-toast][data-styled=true] [data-button]:first-of-type {
  margin-left: var(--toast-button-margin-start);
  margin-right: var(--toast-button-margin-end);
}
[data-sonner-toast][data-styled=true] [data-cancel] {
  color: var(--normal-text);
  background: rgba(0, 0, 0, 0.08);
}
[data-sonner-toaster][data-sonner-theme=dark] [data-sonner-toast][data-styled=true] [data-cancel] {
  background: rgba(255, 255, 255, 0.3);
}
[data-sonner-toast][data-styled=true] [data-close-button] {
  position: absolute;
  left: var(--toast-close-button-start);
  right: var(--toast-close-button-end);
  top: 0;
  height: 20px;
  width: 20px;
  display: flex;
  justify-content: center;
  align-items: center;
  padding: 0;
  color: var(--normal-text);
  background: var(--normal-bg);
  border: 1px solid var(--normal-border);
  transform: var(--toast-close-button-transform);
  border-radius: 50%;
  cursor: pointer;
  z-index: 1;
  transition:
    opacity 100ms,
    background 200ms,
    border-color 200ms;
}
[data-sonner-toast][data-styled=true] [data-close-button]:focus-visible {
  box-shadow: 0px 4px 12px rgba(0, 0, 0, 0.1), 0 0 0 2px rgba(0, 0, 0, 0.2);
}
[data-sonner-toast][data-styled=true] [data-disabled=true] {
  cursor: not-allowed;
}
[data-sonner-toast][data-styled=true]:hover [data-close-button]:hover {
  background: var(--gray2);
  border-color: var(--gray5);
}
[data-sonner-toast][data-swiping=true]::before {
  content: "";
  position: absolute;
  left: -100%;
  right: -100%;
  height: 100%;
  z-index: -1;
}
[data-sonner-toast][data-y-position=top][data-swiping=true]::before {
  bottom: 50%;
  transform: scaleY(3) translateY(50%);
}
[data-sonner-toast][data-y-position=bottom][data-swiping=true]::before {
  top: 50%;
  transform: scaleY(3) translateY(-50%);
}
[data-sonner-toast][data-swiping=false][data-removed=true]::before {
  content: "";
  position: absolute;
  inset: 0;
  transform: scaleY(2);
}
[data-sonner-toast][data-expanded=true]::after {
  content: "";
  position: absolute;
  left: 0;
  height: calc(var(--gap) + 1px);
  bottom: 100%;
  width: 100%;
}
[data-sonner-toast][data-mounted=true] {
  --y: translateY(0);
  opacity: 1;
}
[data-sonner-toast][data-expanded=false][data-front=false] {
  --scale: var(--toasts-before) * 0.05 + 1;
  --y: translateY(calc(var(--lift-amount) * var(--toasts-before))) scale(calc(-1 * var(--scale)));
  height: var(--front-toast-height);
}
[data-sonner-toast] > * {
  transition: opacity 400ms;
}
[data-sonner-toast][data-x-position=right] {
  right: 0;
}
[data-sonner-toast][data-x-position=left] {
  left: 0;
}
[data-sonner-toast][data-expanded=false][data-front=false][data-styled=true] > * {
  opacity: 0;
}
[data-sonner-toast][data-visible=false] {
  opacity: 0;
  pointer-events: none;
}
[data-sonner-toast][data-mounted=true][data-expanded=true] {
  --y: translateY(calc(var(--lift) * var(--offset)));
  height: var(--initial-height);
}
[data-sonner-toast][data-removed=true][data-front=true][data-swipe-out=false] {
  --y: translateY(calc(var(--lift) * -100%));
  opacity: 0;
}
[data-sonner-toast][data-removed=true][data-front=false][data-swipe-out=false][data-expanded=true] {
  --y: translateY(calc(var(--lift) * var(--offset) + var(--lift) * -100%));
  opacity: 0;
}
[data-sonner-toast][data-removed=true][data-front=false][data-swipe-out=false][data-expanded=false] {
  --y: translateY(40%);
  opacity: 0;
  transition: transform 500ms, opacity 200ms;
}
[data-sonner-toast][data-removed=true][data-front=false]::before {
  height: calc(var(--initial-height) + 20%);
}
[data-sonner-toast][data-swiping=true] {
  transform: var(--y) translateY(var(--swipe-amount-y, 0px)) translateX(var(--swipe-amount-x, 0px));
  transition: none;
}
[data-sonner-toast][data-swiped=true] {
  -webkit-user-select: none;
  user-select: none;
}
[data-sonner-toast][data-swipe-out=true][data-y-position=bottom],
[data-sonner-toast][data-swipe-out=true][data-y-position=top] {
  animation-duration: 200ms;
  animation-timing-function: ease-out;
  animation-fill-mode: forwards;
}
[data-sonner-toast][data-swipe-out=true][data-swipe-direction=left] {
  animation-name: swipe-out-left;
}
[data-sonner-toast][data-swipe-out=true][data-swipe-direction=right] {
  animation-name: swipe-out-right;
}
[data-sonner-toast][data-swipe-out=true][data-swipe-direction=up] {
  animation-name: swipe-out-up;
}
[data-sonner-toast][data-swipe-out=true][data-swipe-direction=down] {
  animation-name: swipe-out-down;
}
@keyframes swipe-out-left {
  from {
    transform: var(--y) translateX(var(--swipe-amount-x));
    opacity: 1;
  }
  to {
    transform: var(--y) translateX(calc(var(--swipe-amount-x) - 100%));
    opacity: 0;
  }
}
@keyframes swipe-out-right {
  from {
    transform: var(--y) translateX(var(--swipe-amount-x));
    opacity: 1;
  }
  to {
    transform: var(--y) translateX(calc(var(--swipe-amount-x) + 100%));
    opacity: 0;
  }
}
@keyframes swipe-out-up {
  from {
    transform: var(--y) translateY(var(--swipe-amount-y));
    opacity: 1;
  }
  to {
    transform: var(--y) translateY(calc(var(--swipe-amount-y) - 100%));
    opacity: 0;
  }
}
@keyframes swipe-out-down {
  from {
    transform: var(--y) translateY(var(--swipe-amount-y));
    opacity: 1;
  }
  to {
    transform: var(--y) translateY(calc(var(--swipe-amount-y) + 100%));
    opacity: 0;
  }
}
@media (max-width: 600px) {
  [data-sonner-toaster] {
    position: fixed;
    right: var(--mobile-offset-right);
    left: var(--mobile-offset-left);
    width: 100%;
  }
  [data-sonner-toaster][dir=rtl] {
    left: calc(var(--mobile-offset-left) * -1);
  }
  [data-sonner-toaster] [data-sonner-toast] {
    left: 0;
    right: 0;
    width: calc(100% - var(--mobile-offset-left) * 2);
  }
  [data-sonner-toaster][data-x-position=left] {
    left: var(--mobile-offset-left);
  }
  [data-sonner-toaster][data-y-position=bottom] {
    bottom: var(--mobile-offset-bottom);
  }
  [data-sonner-toaster][data-y-position=top] {
    top: var(--mobile-offset-top);
  }
  [data-sonner-toaster][data-x-position=center] {
    left: var(--mobile-offset-left);
    right: var(--mobile-offset-right);
    transform: none;
  }
}
[data-sonner-toaster][data-sonner-theme=light] {
  --normal-bg: #fff;
  --normal-border: var(--gray4);
  --normal-text: var(--gray12);
  --success-bg: hsl(143, 85%, 96%);
  --success-border: hsl(145, 92%, 87%);
  --success-text: hsl(140, 100%, 27%);
  --info-bg: hsl(208, 100%, 97%);
  --info-border: hsl(221, 91%, 93%);
  --info-text: hsl(210, 92%, 45%);
  --warning-bg: hsl(49, 100%, 97%);
  --warning-border: hsl(49, 91%, 84%);
  --warning-text: hsl(31, 92%, 45%);
  --error-bg: hsl(359, 100%, 97%);
  --error-border: hsl(359, 100%, 94%);
  --error-text: hsl(360, 100%, 45%);
}
[data-sonner-toaster][data-sonner-theme=light] [data-sonner-toast][data-invert=true] {
  --normal-bg: #000;
  --normal-border: hsl(0, 0%, 20%);
  --normal-text: var(--gray1);
}
[data-sonner-toaster][data-sonner-theme=dark] [data-sonner-toast][data-invert=true] {
  --normal-bg: #fff;
  --normal-border: var(--gray3);
  --normal-text: var(--gray12);
}
[data-sonner-toaster][data-sonner-theme=dark] {
  --normal-bg: #000;
  --normal-bg-hover: hsl(0, 0%, 12%);
  --normal-border: hsl(0, 0%, 20%);
  --normal-border-hover: hsl(0, 0%, 25%);
  --normal-text: var(--gray1);
  --success-bg: hsl(150, 100%, 6%);
  --success-border: hsl(147, 100%, 12%);
  --success-text: hsl(150, 86%, 65%);
  --info-bg: hsl(215, 100%, 6%);
  --info-border: hsl(223, 43%, 17%);
  --info-text: hsl(216, 87%, 65%);
  --warning-bg: hsl(64, 100%, 6%);
  --warning-border: hsl(60, 100%, 9%);
  --warning-text: hsl(46, 87%, 65%);
  --error-bg: hsl(358, 76%, 10%);
  --error-border: hsl(357, 89%, 16%);
  --error-text: hsl(358, 100%, 81%);
}
[data-sonner-toaster][data-sonner-theme=dark] [data-sonner-toast] [data-close-button] {
  background: var(--normal-bg);
  border-color: var(--normal-border);
  color: var(--normal-text);
}
[data-sonner-toaster][data-sonner-theme=dark] [data-sonner-toast] [data-close-button]:hover {
  background: var(--normal-bg-hover);
  border-color: var(--normal-border-hover);
}
[data-rich-colors=true][data-sonner-toast][data-type=success] {
  background: var(--success-bg);
  border-color: var(--success-border);
  color: var(--success-text);
}
[data-rich-colors=true][data-sonner-toast][data-type=success] [data-close-button] {
  background: var(--success-bg);
  border-color: var(--success-border);
  color: var(--success-text);
}
[data-rich-colors=true][data-sonner-toast][data-type=info] {
  background: var(--info-bg);
  border-color: var(--info-border);
  color: var(--info-text);
}
[data-rich-colors=true][data-sonner-toast][data-type=info] [data-close-button] {
  background: var(--info-bg);
  border-color: var(--info-border);
  color: var(--info-text);
}
[data-rich-colors=true][data-sonner-toast][data-type=warning] {
  background: var(--warning-bg);
  border-color: var(--warning-border);
  color: var(--warning-text);
}
[data-rich-colors=true][data-sonner-toast][data-type=warning] [data-close-button] {
  background: var(--warning-bg);
  border-color: var(--warning-border);
  color: var(--warning-text);
}
[data-rich-colors=true][data-sonner-toast][data-type=error] {
  background: var(--error-bg);
  border-color: var(--error-border);
  color: var(--error-text);
}
[data-rich-colors=true][data-sonner-toast][data-type=error] [data-close-button] {
  background: var(--error-bg);
  border-color: var(--error-border);
  color: var(--error-text);
}
.sonner-loading-wrapper {
  --size: 16px;
  height: var(--size);
  width: var(--size);
  position: absolute;
  inset: 0;
  z-index: 10;
}
.sonner-loading-wrapper[data-visible=false] {
  transform-origin: center;
  animation: sonner-fade-out 0.2s ease forwards;
}
.sonner-spinner {
  position: relative;
  top: 50%;
  left: 50%;
  height: var(--size);
  width: var(--size);
}
.sonner-loading-bar {
  animation: sonner-spin 1.2s linear infinite;
  background: var(--gray11);
  border-radius: 6px;
  height: 8%;
  left: -10%;
  position: absolute;
  top: -3.9%;
  width: 24%;
}
.sonner-loading-bar:nth-child(1) {
  animation-delay: -1.2s;
  transform: rotate(0.0001deg) translate(146%);
}
.sonner-loading-bar:nth-child(2) {
  animation-delay: -1.1s;
  transform: rotate(30deg) translate(146%);
}
.sonner-loading-bar:nth-child(3) {
  animation-delay: -1s;
  transform: rotate(60deg) translate(146%);
}
.sonner-loading-bar:nth-child(4) {
  animation-delay: -0.9s;
  transform: rotate(90deg) translate(146%);
}
.sonner-loading-bar:nth-child(5) {
  animation-delay: -0.8s;
  transform: rotate(120deg) translate(146%);
}
.sonner-loading-bar:nth-child(6) {
  animation-delay: -0.7s;
  transform: rotate(150deg) translate(146%);
}
.sonner-loading-bar:nth-child(7) {
  animation-delay: -0.6s;
  transform: rotate(180deg) translate(146%);
}
.sonner-loading-bar:nth-child(8) {
  animation-delay: -0.5s;
  transform: rotate(210deg) translate(146%);
}
.sonner-loading-bar:nth-child(9) {
  animation-delay: -0.4s;
  transform: rotate(240deg) translate(146%);
}
.sonner-loading-bar:nth-child(10) {
  animation-delay: -0.3s;
  transform: rotate(270deg) translate(146%);
}
.sonner-loading-bar:nth-child(11) {
  animation-delay: -0.2s;
  transform: rotate(300deg) translate(146%);
}
.sonner-loading-bar:nth-child(12) {
  animation-delay: -0.1s;
  transform: rotate(330deg) translate(146%);
}
@keyframes sonner-fade-in {
  0% {
    opacity: 0;
    transform: scale(0.8);
  }
  100% {
    opacity: 1;
    transform: scale(1);
  }
}
@keyframes sonner-fade-out {
  0% {
    opacity: 1;
    transform: scale(1);
  }
  100% {
    opacity: 0;
    transform: scale(0.8);
  }
}
@keyframes sonner-spin {
  0% {
    opacity: 1;
  }
  100% {
    opacity: 0.15;
  }
}
@media (prefers-reduced-motion) {
  [data-sonner-toast],
  [data-sonner-toast] > *,
  .sonner-loading-bar {
    transition: none !important;
    animation: none !important;
  }
}
.sonner-loader {
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  transform-origin: center;
  transition: opacity 200ms, transform 200ms;
}
.sonner-loader[data-visible=false] {
  opacity: 0;
  transform: scale(0.8) translate(-50%, -50%);
}
`);var z=Array(12).fill(0);function ce(e){return(()=>{var t=O(),n=t.firstChild;return m(n,c(u,{each:z,children:(e,t)=>(()=>{var e=k();return d(()=>h(e,`data-index`,t())),e})()})),d(n=>{var r=[`sonner-loading-wrapper`,e.class??e.className].filter(Boolean).join(` `),i=e.visible;return r!==n.e&&_(t,n.e=r),i!==n.t&&h(t,`data-visible`,n.t=i),n},{e:void 0,t:void 0}),t})()}function le(){return A()}function ue(){return j()}function B(){return M()}function V(){return N()}function de(e){switch(e){case`success`:return c(le,{});case`info`:return c(B,{});case`warning`:return c(ue,{});case`error`:return c(V,{});default:return null}}function fe(){return P()}function pe(){let[e,t]=i(typeof document<`u`&&document.hidden);return n(()=>{let e=()=>{t(document.hidden)};document.addEventListener(`visibilitychange`,e),o(()=>{document.removeEventListener(`visibilitychange`,e)})}),e}var H=1,me=class{subscribers;toasts;dismissedToasts;constructor(){this.subscribers=[],this.toasts=[],this.dismissedToasts=new Set}subscribe=e=>(this.subscribers.push(e),()=>{let t=this.subscribers.indexOf(e);this.subscribers.splice(t,1)});publish=e=>{this.subscribers.forEach(t=>t(e))};addToast=e=>{this.publish(e),this.toasts=[...this.toasts,e]};create=e=>{let{message:t,...n}=e,r=typeof e.id==`string`&&e.id.length>0,i=typeof e.id==`number`||r?e.id:H++,a=this.toasts.find(e=>e.id===i),o=e.dismissible===void 0||e.dismissible;return this.dismissedToasts.has(i)&&this.dismissedToasts.delete(i),a?this.toasts=this.toasts.map(n=>n.id===i?(this.publish({...n,...e,id:i,title:t,dismissible:o}),{...n,...e,id:i,dismissible:o,title:t}):n):this.addToast({title:t,...n,dismissible:o,id:i}),i};dismiss=e=>(e?(this.dismissedToasts.add(e),requestAnimationFrame(()=>{this.subscribers.forEach(t=>t({id:e,dismiss:!0}))})):this.toasts.forEach(e=>{this.dismissedToasts.add(e.id),this.subscribers.forEach(t=>t({id:e.id,dismiss:!0}))}),e);message=(e,t)=>this.create({...t,message:e});error=(e,t)=>this.create({...t,message:e,type:`error`});success=(e,t)=>this.create({...t,type:`success`,message:e});info=(e,t)=>this.create({...t,type:`info`,message:e});warning=(e,t)=>this.create({...t,type:`warning`,message:e});loading=(e,t)=>this.create({...t,type:`loading`,message:e});promise=(e,t)=>{if(!t)return;let n;t.loading!==void 0&&(n=this.create({...t,promise:e,type:`loading`,message:t.loading,description:typeof t.description==`function`?void 0:t.description}));let r=Promise.resolve(typeof e==`function`?e():e),i=n!==void 0,a,o=r.then(async e=>{if(a=[`resolve`,e],ge(e))i=!1,this.create({id:n,type:`default`,message:e});else if(he(e)&&!e.ok){i=!1;let r=typeof t.error==`function`?await t.error(`HTTP error! status: ${e.status}`):t.error,a=typeof t.description==`function`?await t.description(`HTTP error! status: ${e.status}`):t.description,o=U(r)?r:{message:r};this.create({id:n,type:`error`,description:a,...o})}else if(e instanceof Error){i=!1;let r=typeof t.error==`function`?await t.error(e):t.error,a=typeof t.description==`function`?await t.description(e):t.description,o=U(r)?r:{message:r};this.create({id:n,type:`error`,description:a,...o})}else if(t.success!==void 0){i=!1;let r=typeof t.success==`function`?await t.success(e):t.success,a=typeof t.description==`function`?await t.description(e):t.description,o=U(r)?r:{message:r};this.create({id:n,type:`success`,description:a,...o})}}).catch(async e=>{if(a=[`reject`,e],t.error!==void 0){i=!1;let r=typeof t.error==`function`?await t.error(e):t.error,a=typeof t.description==`function`?await t.description(e):t.description,o=U(r)?r:{message:r};this.create({id:n,type:`error`,description:a,...o})}}).finally(()=>{i&&(this.dismiss(n),n=void 0),t.finally?.()}),s=()=>new Promise((e,t)=>{o.then(()=>{a[0]===`reject`?t(a[1]):e(a[1])}).catch(t)});return typeof n!=`string`&&typeof n!=`number`?{unwrap:s}:Object.assign(n,{unwrap:s})};custom=(e,t)=>{let n=t?.id||H++;return this.create({jsx:e(n),...t,id:n}),n};getActiveToasts=()=>this.toasts.filter(e=>!this.dismissedToasts.has(e.id))};function he(e){return e&&typeof e==`object`&&`ok`in e&&typeof e.ok==`boolean`&&`status`in e&&typeof e.status==`number`}function U(e){return typeof e==`object`&&!!e&&`message`in e}function ge(e){return typeof Node<`u`&&e instanceof Node}var W=new me;function _e(e,t){let n=t?.id||H++;return W.addToast({title:e,...t,id:n}),n}var G=Object.assign(_e,{success:W.success,info:W.info,warning:W.warning,error:W.error,custom:W.custom,message:W.message,promise:W.promise,dismiss:W.dismiss,loading:W.loading},{getHistory:()=>W.toasts,getToasts:()=>W.getActiveToasts()});function ve(e){return typeof e==`object`&&!!e&&`label`in e}var ye=3,K=`24px`,q=`16px`,be=4e3,xe=14,Se=45,Ce=200;function J(...e){return e.filter(Boolean).join(` `)}function Y(e){return typeof e==`function`?e():e}function we(e){let[t,n]=e.split(`-`),r=[];return t&&r.push(t),n&&r.push(n),r}function Te(){if(typeof window>`u`||typeof document>`u`)return`ltr`;let e=document.documentElement.getAttribute(`dir`);return e===`auto`||!e?window.getComputedStyle(document.documentElement).direction:e}function Ee(e,t){let n={};return[e,t].forEach((e,t)=>{let r=t===1,i=r?`--mobile-offset`:`--offset`,a=r?q:K,o=e=>{for(let t of[`top`,`right`,`bottom`,`left`])n[`${i}-${t}`]=typeof e==`number`?`${e}px`:e};if(typeof e==`number`||typeof e==`string`){o(e);return}if(typeof e==`object`&&e){for(let t of[`top`,`right`,`bottom`,`left`]){let r=e[t];n[`${i}-${t}`]=r===void 0?a:typeof r==`number`?`${r}px`:r}return}o(a)}),n}function De(e,t){return e??t}function Oe(e,t){return e??t??``}function ke(e,t){return e??t??``}function Ae(e){return e===`system`?typeof window<`u`&&window.matchMedia(`(prefers-color-scheme: dark)`).matches?`dark`:`light`:e}function je(e){let[t,s]=i(!1),[u,p]=i(!1),[v,y]=i(!1),[b,S]=i(!1),[te,C]=i(!1),[w,T]=i(null),[E,D]=i(null),[O,k]=i(0),[A,j]=i(0),M,N=null,P=0,se=0,L=be,[R,z]=i(null),le=()=>e.index===0,ue=()=>e.index+1<=e.visibleToasts,B=()=>e.toast.type,V=()=>e.toast.dismissible!==!1,H=()=>De(e.classNames,e.classes),me=()=>ke(e.className,e.class),he=()=>Oe(e.descriptionClassName,e.descriptionClass),U=()=>ke(e.toast.className,e.toast.class),ge=()=>Oe(e.toast.descriptionClassName,e.toast.descriptionClass),W=()=>De(e.toast.classNames,e.toast.classes),_e=()=>e.toast.closeButton??e.closeButton,G=()=>e.toast.duration??e.duration??be,ye=()=>e.toast.invert??e.invert,K=()=>B()===`loading`,q=()=>e.swipeDirections??we(e.position),Te=a(()=>e.position.split(`-`)[0]),Ee=a(()=>e.position.split(`-`)[1]),Ae=a(()=>Math.max(0,e.heights.findIndex(t=>t.toastId===e.toast.id))),je=a(()=>e.heights.reduce((e,t,n)=>n>=Ae()?e:e+t.height,0)),X=a(()=>Ae()*(e.gap??xe)+je()),Me=pe();f(()=>{L=G()});function Z(){p(!0),k(X()),e.setHeights(t=>t.filter(t=>t.toastId!==e.toast.id)),window.setTimeout(()=>{e.removeToast(e.toast)},Ce)}function Ne(){return e.icons?.loading?(()=>{var t=F();return m(t,()=>e.icons.loading),d(e=>{var n=J(H()?.loader,W()?.loader,`sonner-loader`),r=B()===`loading`;return n!==e.e&&_(t,e.e=n),r!==e.t&&h(t,`data-visible`,e.t=r),e},{e:void 0,t:void 0}),t})():c(ce,{get class(){return J(H()?.loader,W()?.loader)},get visible(){return B()===`loading`}})}let Pe=()=>e.toast.icon??e.icons?.[B()]??de(B());return n(()=>{if(s(!0),!M)return;let t=M.getBoundingClientRect().height;j(t),e.setHeights(n=>[{toastId:e.toast.id,toasterId:e.toast.toasterId,height:t,position:e.toast.position},...n]),o(()=>{e.setHeights(t=>t.filter(t=>t.toastId!==e.toast.id))})}),f(()=>{if(!t()||!M)return;Y(e.toast.title),Y(e.toast.description);let n=M.style.height;M.style.height=`auto`;let r=M.getBoundingClientRect().height;M.style.height=n,j(r),e.setHeights(t=>t.find(t=>t.toastId===e.toast.id)?t.map(t=>t.toastId===e.toast.id?{...t,height:r}:t):[{toastId:e.toast.id,toasterId:e.toast.toasterId,height:r,position:e.toast.position},...t])}),f(r(()=>[e.expanded,e.interacting,e.toast,B(),Me(),G()],([t,n,r,i])=>{if(r.promise&&i===`loading`||r.duration===1/0||i===`loading`)return;let a;t||n||(e.pauseWhenPageIsHidden??!0)&&Me()?(()=>{if(se<P){let e=new Date().getTime()-P;L-=e}se=new Date().getTime()})():L!==1/0&&(P=new Date().getTime(),a=setTimeout(()=>{r.onAutoClose?.(r),Z()},L)),o(()=>{a&&clearTimeout(a)})})),f(r(()=>e.toast.delete,t=>{t&&(Z(),e.toast.onDismiss?.(e.toast))})),(()=>{var n=ie(),r=n.firstChild,i=r.firstChild;n.$$pointermove=e=>{if(!R()||!V()||(window.getSelection()?.toString().length??0)>0)return;let t=e.clientY-R().y,n=e.clientX-R().x;!w()&&(Math.abs(n)>1||Math.abs(t)>1)&&T(Math.abs(n)>Math.abs(t)?`x`:`y`);let r={x:0,y:0},i=e=>1/(1.5+Math.abs(e)/20);w()===`y`?(q().includes(`top`)||q().includes(`bottom`))&&(r.y=q().includes(`top`)&&t<0||q().includes(`bottom`)&&t>0?t:t*i(t)):w()===`x`&&(q().includes(`left`)||q().includes(`right`))&&(r.x=q().includes(`left`)&&n<0||q().includes(`right`)&&n>0?n:n*i(n)),(Math.abs(r.x)>0||Math.abs(r.y)>0)&&C(!0),M?.style.setProperty(`--swipe-amount-x`,`${r.x}px`),M?.style.setProperty(`--swipe-amount-y`,`${r.y}px`)},n.$$pointerup=()=>{if(b()||!V())return;z(null);let t=Number(M?.style.getPropertyValue(`--swipe-amount-x`).replace(`px`,``)||0),n=Number(M?.style.getPropertyValue(`--swipe-amount-y`).replace(`px`,``)||0),r=Math.max(1,new Date().getTime()-(N?.getTime()??0)),i=w()===`x`?t:n,a=Math.abs(i)/r;if(Math.abs(i)>=Se||a>.11){k(X()),e.toast.onDismiss?.(e.toast),w()===`x`?D(t>0?`right`:`left`):D(n>0?`down`:`up`),Z(),S(!0);return}M?.style.setProperty(`--swipe-amount-x`,`0px`),M?.style.setProperty(`--swipe-amount-y`,`0px`),C(!1),y(!1),T(null)},n.$$pointerdown=e=>{e.button!==2&&(K()||!V()||e.target.closest(`button`)||(N=new Date,k(X()),e.currentTarget.setPointerCapture(e.pointerId),y(!0),z({x:e.clientX,y:e.clientY})))},n.addEventListener(`dragend`,()=>{y(!1),T(null),z(null)});var a=M;return typeof a==`function`?x(a,n):M=n,m(n,c(l,{get when(){return g(()=>!!(_e()&&!e.toast.jsx))()&&B()!==`loading`},get children(){var t=ne();return t.$$click=()=>{K()||!V()||(Z(),e.toast.onDismiss?.(e.toast))},t.$$pointerup=e=>e.stopPropagation(),t.$$pointerdown=e=>e.stopPropagation(),m(t,()=>e.icons?.close??c(fe,{})),d(n=>{var r=e.closeButtonAriaLabel??`Close toast`,i=K(),a=J(H()?.closeButton,W()?.closeButton);return r!==n.e&&h(t,`aria-label`,n.e=r),i!==n.t&&h(t,`data-disabled`,n.t=i),a!==n.a&&_(t,n.a=a),n},{e:void 0,t:void 0,a:void 0}),t}}),r),m(n,c(l,{get when(){return g(()=>!!((B()||e.toast.icon||e.toast.promise)&&e.toast.icon!==null))()&&(e.icons?.[B()]!==null||e.toast.icon)},get children(){var t=re();return m(t,(()=>{var t=g(()=>!!(e.toast.promise||e.toast.type===`loading`&&!e.toast.icon));return()=>t()?e.toast.icon||Ne():null})(),null),m(t,(()=>{var t=g(()=>e.toast.type!==`loading`);return()=>t()?Pe():null})(),null),d(()=>_(t,J(H()?.icon,W()?.icon))),t}}),r),m(i,(()=>{var t=g(()=>!!e.toast.jsx);return()=>t()?e.toast.jsx:Y(e.toast.title)})()),m(r,c(l,{get when(){return e.toast.description},get children(){var t=I();return m(t,()=>Y(e.toast.description)),d(()=>_(t,J(he(),ge(),H()?.description,W()?.description))),t}}),null),m(n,c(l,{get when(){return e.toast.cancel},children:t=>c(l,{get when(){return!ve(t())},get fallback(){return(()=>{var n=ae();return n.$$click=e=>{let n=t();V()&&(n.onClick?.(e),Z())},m(n,()=>t().label),d(t=>{var r=e.toast.cancelButtonStyle??e.cancelButtonStyle,i=J(H()?.cancelButton,W()?.cancelButton);return t.e=ee(n,r,t.e),i!==t.t&&_(n,t.t=i),t},{e:void 0,t:void 0}),n})()},get children(){return t()}})}),null),m(n,c(l,{get when(){return e.toast.action},children:t=>c(l,{get when(){return!ve(t())},get fallback(){return(()=>{var n=oe();return n.$$click=e=>{t().onClick?.(e),!e.defaultPrevented&&Z()},m(n,()=>t().label),d(t=>{var r=e.toast.actionButtonStyle??e.actionButtonStyle,i=J(H()?.actionButton,W()?.actionButton);return t.e=ee(n,r,t.e),i!==t.t&&_(n,t.t=i),t},{e:void 0,t:void 0}),n})()},get children(){return t()}})}),null),d(a=>{var o=J(me(),U(),H()?.toast,W()?.toast,H()?.default,H()?.[B()],W()?.[B()]),s=e.toast.richColors??e.defaultRichColors,c=!(e.toast.jsx||e.toast.unstyled||e.unstyled),l=t(),d=!!e.toast.promise,f=te(),p=u(),m=ue(),g=Te(),y=Ee(),x=e.index,S=le(),C=v(),w=V(),T=B(),D=ye(),k=b(),j=E(),M=!!(e.expanded||e.expandByDefault&&t()),N=e.toast.testId,P={"--index":e.index,"--toasts-before":e.index,"--z-index":e.toasts.length-e.index,"--offset":`${u()?O():X()}px`,"--initial-height":e.expandByDefault?`auto`:`${A()}px`,...e.style,...e.toast.style},F=J(H()?.content,W()?.content),ne=J(H()?.title,W()?.title);return o!==a.e&&_(n,a.e=o),s!==a.t&&h(n,`data-rich-colors`,a.t=s),c!==a.a&&h(n,`data-styled`,a.a=c),l!==a.o&&h(n,`data-mounted`,a.o=l),d!==a.i&&h(n,`data-promise`,a.i=d),f!==a.n&&h(n,`data-swiped`,a.n=f),p!==a.s&&h(n,`data-removed`,a.s=p),m!==a.h&&h(n,`data-visible`,a.h=m),g!==a.r&&h(n,`data-y-position`,a.r=g),y!==a.d&&h(n,`data-x-position`,a.d=y),x!==a.l&&h(n,`data-index`,a.l=x),S!==a.u&&h(n,`data-front`,a.u=S),C!==a.c&&h(n,`data-swiping`,a.c=C),w!==a.w&&h(n,`data-dismissible`,a.w=w),T!==a.m&&h(n,`data-type`,a.m=T),D!==a.f&&h(n,`data-invert`,a.f=D),k!==a.y&&h(n,`data-swipe-out`,a.y=k),j!==a.g&&h(n,`data-swipe-direction`,a.g=j),M!==a.p&&h(n,`data-expanded`,a.p=M),N!==a.b&&h(n,`data-testid`,a.b=N),a.T=ee(n,P,a.T),F!==a.A&&_(r,a.A=F),ne!==a.O&&_(i,a.O=ne),a},{e:void 0,t:void 0,a:void 0,o:void 0,i:void 0,n:void 0,s:void 0,h:void 0,r:void 0,d:void 0,l:void 0,u:void 0,c:void 0,w:void 0,m:void 0,f:void 0,y:void 0,g:void 0,p:void 0,b:void 0,T:void 0,A:void 0,O:void 0}),n})()}function X(e){let r=t({position:`bottom-right`,hotkey:[`altKey`,`KeyT`],theme:`light`,gap:xe,visibleToasts:ye,dir:Te(),containerAriaLabel:`Notifications`},e),s=Ae(r.theme),[p,g]=te({toasts:[]}),v=a(()=>{let e=p.toasts;return r.id?e.filter(e=>e.toasterId===r.id):e.filter(e=>!e.toasterId)}),y=a(()=>Array.from(new Set([r.position,...v().filter(e=>e.position).map(e=>e.position)]))),[b,S]=i([]),[T,E]=i(!1),[D,O]=i(!1),[k,A]=i(s),[j,M]=i(null),[N,P]=i(!1),F,ne=()=>r.hotkey.join(`+`).replace(/Key/g,``).replace(/Digit/g,``),re=()=>ke(r.className,r.class),I=()=>r.toastOptions,ie=()=>De(I()?.classNames,I()?.classes),ae=()=>Oe(I()?.descriptionClassName,I()?.descriptionClass),oe=()=>ke(I()?.className,I()?.class),R=e=>{g(`toasts`,t=>(t.find(t=>t.id===e.id)?.delete||W.dismiss(e.id),t.filter(({id:t})=>t!==e.id)))};return n(()=>{let e=W.subscribe(e=>{if(e.dismiss){requestAnimationFrame(()=>{g(`toasts`,C(t=>{t.forEach(t=>{t.id===e.id&&(t.delete=!0)})}))});return}let t=e,n=p.toasts.findIndex(e=>e.id===t.id);if(n!==-1){g(`toasts`,n,w(t));return}g(`toasts`,C(e=>{e.unshift(t)}))});o(e)}),f(()=>{let e=r.theme;if(e!==`system`){A(e);return}if(typeof window>`u`)return;let t=window.matchMedia(`(prefers-color-scheme: dark)`),n=({matches:e})=>{A(e?`dark`:`light`)};n(t);try{t.addEventListener(`change`,n),o(()=>t.removeEventListener(`change`,n))}catch{t.addListener(n),o(()=>t.removeListener(n))}}),f(()=>{v().length<=1&&E(!1)}),n(()=>{let e=e=>{r.hotkey.length>0&&r.hotkey.every(t=>e[t]||e.code===t)&&(E(!0),F?.focus()),e.code===`Escape`&&(document.activeElement===F||F?.contains(document.activeElement))&&E(!1)};document.addEventListener(`keydown`,e),o(()=>document.removeEventListener(`keydown`,e))}),o(()=>{j()&&(j()?.focus({preventScroll:!0}),M(null),P(!1))}),(()=>{var e=se();return m(e,c(u,{get each(){return y()},children:(e,t)=>{let[n,i]=e.split(`-`),o=a(()=>v().filter(n=>!n.position&&t()===0||n.position===e)),s=a(()=>b().filter(n=>t()===0&&!n.position||n.position===e));return c(l,{get when(){return v().length>0},get children(){var t=L();t.$$pointerup=()=>O(!1),t.$$pointerdown=e=>{e.target instanceof HTMLElement&&e.target.dataset.dismissible===`false`||O(!0)},t.addEventListener(`dragend`,()=>E(!1)),t.addEventListener(`mouseleave`,()=>{D()||E(!1)}),t.$$mousemove=()=>E(!0),t.addEventListener(`mouseenter`,()=>E(!0)),t.addEventListener(`focus`,e=>{e.target instanceof HTMLElement&&e.target.dataset.dismissible===`false`||N()||(P(!0),M(e.relatedTarget))}),t.addEventListener(`blur`,e=>{N()&&!e.currentTarget.contains(e.relatedTarget)&&(P(!1),j()&&(j()?.focus({preventScroll:!0}),M(null)))});var a=F;return typeof a==`function`?x(a,t):F=t,h(t,`data-y-position`,n),h(t,`data-x-position`,i),m(t,c(u,{get each(){return o()},children:(t,n)=>c(je,{get icons(){return r.icons},get index(){return n()},toast:t,get defaultRichColors(){return r.richColors},get duration(){return I()?.duration??r.duration},get class(){return oe()},get descriptionClassName(){return ae()},get invert(){return!!r.invert},get visibleToasts(){return r.visibleToasts},get closeButton(){return I()?.closeButton??r.closeButton??!1},get interacting(){return D()},position:e,get style(){return I()?.style},get unstyled(){return I()?.unstyled},get classNames(){return ie()},get cancelButtonStyle(){return I()?.cancelButtonStyle},get actionButtonStyle(){return I()?.actionButtonStyle},get closeButtonAriaLabel(){return I()?.closeButtonAriaLabel},removeToast:R,get toasts(){return o()},get heights(){return s()},setHeights:S,get expandByDefault(){return!!r.expand},get gap(){return r.gap},get expanded(){return T()},get swipeDirections(){return r.swipeDirections},get pauseWhenPageIsHidden(){return r.pauseWhenPageIsHidden}})})),d(e=>{var n=r.dir===`auto`?Te():r.dir,i=re(),a=k(),o={"--front-toast-height":`${s()[0]?.height??0}px`,"--gap":`${r.gap}px`,...r.style,...Ee(r.offset,r.mobileOffset)};return n!==e.e&&h(t,`dir`,e.e=n),i!==e.t&&_(t,e.t=i),a!==e.a&&h(t,`data-sonner-theme`,e.a=a),e.o=ee(t,o,e.o),e},{e:void 0,t:void 0,a:void 0,o:void 0}),t}})}})),d(()=>h(e,`aria-label`,r.customAriaLabel??`${r.containerAriaLabel} ${ne()}`)),e})()}v([`pointerdown`,`pointerup`,`pointermove`,`click`,`mousemove`]);var Me=2e3;function Z(e){return!e.description||e.description.length<=2e3?e:{...e,description:`${e.description.slice(0,Me)}…`}}var Ne=b(`<div data-slot=toast-v2-icon>`),Pe=b(`<div data-slot=toast-v2-content>`),Fe=b(`<div data-slot=toast-v2-title>`),Ie=b(`<div data-slot=toast-v2-description>`),Le=b(`<div data-slot=toast-v2-actions>`),Re=b(`<button type=button data-slot=toast-v2-close-button>`),ze=b(`<svg width=16 height=16 viewBox="0 0 16 16"fill=none xmlns=http://www.w3.org/2000/svg aria-hidden=true><path d="M4.25 11.75L11.75 4.25"stroke=currentColor></path><path d="M11.75 11.75L4.25 4.25"stroke=currentColor>`),Be=b(`<button type=button data-component=button-v2 data-size=small>`);function Ve(e){let r=E(),[i,a]=s(e,[`class`,`className`,`style`,`toastOptions`,`swipeDirections`]);return n(()=>{let e=()=>{document.querySelectorAll(`.toast-v2-region .toast-v2`).forEach(e=>{let t=e.dataset.visible===`false`;e.inert=t,e.tabIndex=t?-1:0})},t=!1,n=()=>{let n=document.querySelectorAll(`.toast-v2-region`);n.length&&(r.disconnect(),n.forEach(e=>{r.observe(e,{subtree:!0,childList:!0,attributes:!0,attributeFilter:[`data-visible`]})}),t=!0,e())},r=new MutationObserver(()=>{t?e():n()});r.observe(document.body,{subtree:!0,childList:!0}),queueMicrotask(n),o(()=>r.disconnect())}),c(S,{get children(){return c(X,t({get position(){return T(r.locale())?`bottom-left`:`bottom-right`},get offset(){return T(r.locale())?{left:32,bottom:48}:{right:32,bottom:48}},mobileOffset:16,gap:12,duration:5e3,swipeDirections:[`bottom`],get className(){return[`toast-v2-region`,i.className,i.class].filter(Boolean).join(` `)},get style(){return{"--width":`320px`,...i.style}},get toastOptions(){return{...i.toastOptions,unstyled:!0,closeButton:!0,closeButtonAriaLabel:i.toastOptions?.closeButtonAriaLabel??r.t(`ui.common.dismiss`)}}},a))}})}var He=p();function Ue(e){return c(He.Provider,{get value(){return e.toastId},get children(){return e.children}})}function We(e){return(()=>{var t=Ne();return y(t,e,!1,!1),t})()}function Ge(e){return(()=>{var t=Pe();return y(t,e,!1,!1),t})()}function Ke(e){return(()=>{var t=Fe();return y(t,e,!1,!1),t})()}function qe(e){return(()=>{var t=Ie();return y(t,e,!1,!1),t})()}function Je(e){return(()=>{var t=Le();return y(t,e,!1,!1),t})()}function Ye(n){let r=E(),i=e(He),[a,o]=s(n,[`children`,`onClick`]);return(()=>{var e=Re();return y(e,t({get"aria-label"(){return r.t(`ui.common.dismiss`)}},o,{onClick:e=>{a.onClick instanceof Function&&a.onClick(e),!e.defaultPrevented&&i!==void 0&&$e.dismiss(i)}}),!1,!0),m(e,()=>a.children??c(Xe,{})),e})()}function Xe(){return ze()}var Ze=Object.assign(Ue,{Region:Ve,Icon:We,Content:Ge,Title:Ke,Description:qe,Actions:Je,CloseButton:Ye}),Qe=0,$e={show(e,t){let n=--Qe;return G.custom(t=>e({toastId:Number(t)}),{id:n,className:`toast-v2`,duration:t?.persistent?1/0:t?.duration,unstyled:!0,description:t?.resize?()=>{t.resize?.()}:void 0}),n},dismiss(e){return e===void 0?(Q.clear(),$.clear()):it($.get(e)),G.dismiss(e)}},Q=new Map,$=new Map;function et(e){let t=Z(typeof e==`string`?{description:e}:e),n=JSON.stringify({title:t.title,description:t.description,variant:t.variant,duration:t.duration,persistent:t.persistent,actions:t.actions?.map(e=>[e.label,e.variant])}),r=Q.get(n),i=G.getToasts();if(r&&i.at(-1)?.id===r.id)return r.options=t,tt(r),rt(r.id),r.id;r&&i.some(e=>e.id===r.id)&&$e.dismiss(r.id),it(r);let a={id:--Qe,key:n,options:t};return a.actions=nt(a),Q.set(n,a),$.set(a.id,a),tt(a),a.id}function tt(e){let t=()=>it(e);G(e.options.title??``,{id:e.id,description:e.options.description,icon:e.options.icon,action:e.actions,closeButton:!0,duration:e.options.persistent?1/0:e.options.duration,className:`toast-v2 toast-v2--${e.options.variant??`default`}`,testId:`toast-v2-${e.id}`,unstyled:!0,onDismiss:t,onAutoClose:t})}function nt(e){if(e.options.actions?.length)return c(Ze.Actions,{get children(){return e.options.actions.map((t,n)=>(()=>{var r=Be();return r.$$click=()=>{let t=e.options.actions?.[n]?.onClick;t&&t!==`dismiss`&&t(),$e.dismiss(e.id)},m(r,()=>t.label),d(e=>{var n=t.variant===`secondary`?`ghost`:`neutral`,i=t.variant??`primary`;return n!==e.e&&h(r,`data-variant`,e.e=n),i!==e.t&&h(r,`data-action-variant`,e.t=i),e},{e:void 0,t:void 0}),r})())}})}function rt(e){typeof document>`u`||typeof requestAnimationFrame>`u`||requestAnimationFrame(()=>{let t=document.querySelector(`[data-testid="toast-v2-${e}"]`);!t||D()||t.animate([{scale:1},{scale:1.025},{scale:1}],{duration:160,easing:`ease-out`})})}function it(e){e&&(Q.get(e.key)===e&&Q.delete(e.key),$.get(e.id)===e&&$.delete(e.id))}v([`click`]);export{et as n,$e as r,Ze as t};