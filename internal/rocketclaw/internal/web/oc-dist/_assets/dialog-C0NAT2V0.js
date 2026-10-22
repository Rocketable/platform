import{r as e}from"./rolldown-runtime-hePW80VL.js";import{$ as t,C as n,E as r,I as i,L as a,N as o,O as s,V as c,W as l,X as u,dt as d,et as f,ft as p,gt as m,it as h,j as g,k as _,lt as v,nt as y,tt as b}from"./dist-CAogbCvn.js";import{n as x}from"./runtime-Bx_UZ2mY.js";import{n as S}from"./dialog-CJmdvKPa.js";import{r as C}from"./language-MZa-xXp7.js";import{a as ee}from"./path-m0dFCAHb.js";import{t as w}from"./button-Ct-LpRMR.js";import{a as T,i as E,n as te,r as ne,t as re}from"./dialog-OJUnaAln.js";import{t as D}from"./fuzzysort-ukaRp9PC.js";import{t as O}from"./text-input-ogLcuncf.js";var ie=`file-tree-container`,k=`data-file-tree-style`,A=`data-file-tree-unsafe-css`,ae=`data-file-tree-scrollbar-measure`,oe=`data-file-tree-scrollbar-gutter-measured`,se=`--trees-scrollbar-gutter-measured`,ce=`header`,le=`context-menu`,ue=`context-menu-trigger`,de=`@layer base, theme, unsafe;

@layer base {
  :host {
    /*
      CSS variables use a fallback stack to ensure user and theme colors slot
      in with ease. User colors take precedence over theme colors, which take
      precedence over defaults.

      Fallback order:

      1. --trees-*-override (explicit)
      2. --trees-theme-* (e.g. Shiki/VS Code tokens)
      3. defaults

      Theme variable names mirror Shiki/VS Code theme file JSON tokens.

      // Available CSS Color Overrides
      --trees-fg-override
      --trees-fg-muted-override
      --trees-bg-override
      --trees-bg-muted-override
      --trees-accent-override
      --trees-border-color-override

      --trees-focus-ring-color-override
      --trees-focus-ring-width-override
      --trees-focus-ring-offset-override

      --trees-search-fg-override
      --trees-search-font-weight-override
      --trees-search-bg-override

      --trees-selected-fg-override
      --trees-selected-bg-override
      --trees-selected-focused-border-color-override

      // Git Status Color Overrides
      --trees-status-added-override
      --trees-status-ignored-override
      --trees-status-modified-override
      --trees-status-renamed-override
      --trees-status-untracked-override
      --trees-status-deleted-override
      --trees-git-added-color-override
      --trees-git-ignored-color-override
      --trees-git-modified-color-override
      --trees-git-renamed-color-override
      --trees-git-untracked-color-override
      --trees-git-deleted-color-override

      // Built-in File Icon Color Overrides
      --trees-file-icon-color
      --trees-file-icon-color-astro
      --trees-file-icon-color-babel
      --trees-file-icon-color-bash
      --trees-file-icon-color-biome
      --trees-file-icon-color-bootstrap
      --trees-file-icon-color-browserslist
      --trees-file-icon-color-bun
      --trees-file-icon-color-c
      --trees-file-icon-color-cpp
      --trees-file-icon-color-claude
      --trees-file-icon-color-css
      --trees-file-icon-color-database
      --trees-file-icon-color-default
      --trees-file-icon-color-docker
      --trees-file-icon-color-eslint
      --trees-file-icon-color-git
      --trees-file-icon-color-go
      --trees-file-icon-color-graphql
      --trees-file-icon-color-html
      --trees-file-icon-color-image
      --trees-file-icon-color-javascript
      --trees-file-icon-color-json
      --trees-file-icon-color-markdown
      --trees-file-icon-color-mcp
      --trees-file-icon-color-npm
      --trees-file-icon-color-oxc
      --trees-file-icon-color-postcss
      --trees-file-icon-color-prettier
      --trees-file-icon-color-python
      --trees-file-icon-color-react
      --trees-file-icon-color-ruby
      --trees-file-icon-color-rust
      --trees-file-icon-color-sass
      --trees-file-icon-color-svg
      --trees-file-icon-color-svelte
      --trees-file-icon-color-svgo
      --trees-file-icon-color-swift
      --trees-file-icon-color-table
      --trees-file-icon-color-text
      --trees-file-icon-color-tailwind
      --trees-file-icon-color-terraform
      --trees-file-icon-color-typescript
      --trees-file-icon-color-vite
      --trees-file-icon-color-vscode
      --trees-file-icon-color-vue
      --trees-file-icon-color-wasm
      --trees-file-icon-color-webpack
      --trees-file-icon-color-yml
      --trees-file-icon-color-zig
      --trees-file-icon-color-zip

      // Density
      //
      // A unitless scale factor for padding, gaps, and indentation. Usually
      // set via \`density\` on useFileTree. Individual overrides take precedence.
      //
      //   Compact: 0.8
      //   Default: 1
      //   Relaxed: 1.2
      //
      --trees-density-override

      // Available CSS Layout Overrides
      --trees-gap-override
      --trees-border-radius-override
      --trees-font-family-override
      --trees-font-size-override
      --trees-font-weight-regular-override
      --trees-font-weight-semibold-override
      --trees-level-gap-override
      --trees-item-padding-x-override
      --trees-item-margin-x-override
      --trees-item-row-gap-override
      --trees-icon-width-override
      --trees-icon-nudge-override
      --trees-scrollbar-gutter-override
      --trees-padding-inline-override
    */

    --trees-accent: var(--trees-accent-override, #009fff);
    --trees-fg: var(
      --trees-fg-override,
      var(--trees-theme-sidebar-fg, light-dark(#6c6c71, #adadb1))
    );
    --trees-fg-muted: var(
      --trees-fg-muted-override,
      var(--trees-theme-sidebar-header-fg, light-dark(#84848a, #84848a))
    );
    --trees-bg: var(
      --trees-bg-override,
      var(--trees-theme-sidebar-bg, light-dark(#f8f8f8, #141415))
    );
    /* var(--trees-theme-list-hover-bg, light-dark(#dfebff59, #19283c59)) */
    --trees-bg-muted: var(
      --trees-bg-muted-override,
      var(
        --trees-theme-list-hover-bg,
        light-dark(
          color-mix(
            in lab,
            var(--trees-accent) var(--trees-bg-alpha-light, 8%),
            var(--trees-bg)
          ),
          color-mix(
            in lab,
            var(--trees-accent) var(--trees-bg-alpha-dark, 10%),
            var(--trees-bg)
          )
        )
      )
    );
    --trees-input-bg: var(
      --trees-input-bg-override,
      light-dark(#f8f8f8, #070707)
    );

    --trees-added-light: #16a994;
    --trees-added-dark: #00cab1;
    --trees-ignored-light: #adadb1;
    --trees-ignored-dark: #4a4a4e;
    --trees-modified-light: #1ca1c7;
    --trees-modified-dark: #08c0ef;
    --trees-renamed-light: #d5a910;
    --trees-renamed-dark: #ffd452;
    --trees-untracked-light: #16a994;
    --trees-untracked-dark: #00cab1;
    --trees-deleted-light: #ff2e3f;
    --trees-deleted-dark: #ff6762;

    --trees-border-color: var(
      --trees-border-color-override,
      var(--trees-theme-sidebar-border, light-dark(#eeeeef, #070707))
    );
    --trees-indent-guide-bg: var(
      --trees-indent-guide-bg-override,
      color-mix(in lab, var(--trees-fg-muted) 25%, transparent)
    );
    --trees-density: var(--trees-density-override, 1);
    --trees-border-radius: var(
      --trees-border-radius-override,
      calc(6px * var(--trees-density))
    );

    --trees-font-family: var(--trees-font-family-override, system-ui);
    --trees-font-size: var(--trees-font-size-override, 13px);
    --trees-font-weight-regular: var(--trees-font-weight-regular-override, 400);
    --trees-font-weight-semibold: var(
      --trees-font-weight-semibold-override,
      600
    );

    --trees-focus-ring-color: var(
      --trees-focus-ring-color-override,
      var(--trees-theme-focus-ring, var(--trees-accent))
    );
    --trees-focus-ring-width: var(--trees-focus-ring-width-override, 1px);
    --trees-focus-ring-offset: var(--trees-focus-ring-offset-override, -1px);

    --trees-search-fg: var(
      --trees-search-fg-override,
      var(--trees-theme-input-fg, var(--trees-fg))
    );
    --trees-search-font-weight: var(--trees-search-font-weight-override, 600);
    --trees-search-bg: var(
      --trees-search-bg-override,
      var(--trees-theme-input-bg, var(--trees-input-bg))
    );

    --trees-scrollbar-thumb: var(
      --trees-scrollbar-thumb-override,
      var(
        --trees-theme-scrollbar-thumb,
        color-mix(in lab, var(--trees-fg) 25%, var(--trees-bg))
      )
    );

    --trees-selected-fg: var(
      --trees-selected-fg-override,
      var(--trees-theme-list-active-selection-fg, var(--trees-fg))
    );
    --trees-selected-bg: var(
      --trees-selected-bg-override,
      var(
        --trees-theme-list-active-selection-bg,
        light-dark(
          color-mix(in lab, var(--trees-accent) 12%, var(--trees-bg)),
          color-mix(in lab, var(--trees-accent) 15%, var(--trees-bg))
        )
      )
    );
    --trees-selected-focused-border-color: var(
      --trees-selected-focused-border-color-override,
      var(--trees-theme-focus-ring, var(--trees-accent))
    );

    /* Git status (e.g. from Shiki theme gitDecoration.*) */
    --trees-status-added: var(
      --trees-status-added-override,
      var(
        --trees-theme-git-added-fg,
        light-dark(var(--trees-added-light), var(--trees-added-dark))
      )
    );
    --trees-status-ignored: var(
      --trees-status-ignored-override,
      var(
        --trees-theme-git-ignored-fg,
        light-dark(var(--trees-ignored-light), var(--trees-ignored-dark))
      )
    );
    --trees-status-modified: var(
      --trees-status-modified-override,
      var(
        --trees-theme-git-modified-fg,
        light-dark(var(--trees-modified-light), var(--trees-modified-dark))
      )
    );
    --trees-status-renamed: var(
      --trees-status-renamed-override,
      var(
        --trees-theme-git-renamed-fg,
        light-dark(var(--trees-renamed-light), var(--trees-renamed-dark))
      )
    );
    --trees-status-untracked: var(
      --trees-status-untracked-override,
      var(
        --trees-theme-git-untracked-fg,
        light-dark(var(--trees-untracked-light), var(--trees-untracked-dark))
      )
    );
    --trees-status-deleted: var(
      --trees-status-deleted-override,
      var(
        --trees-theme-git-deleted-fg,
        light-dark(var(--trees-deleted-light), var(--trees-deleted-dark))
      )
    );
    --trees-git-modified-color: var(
      --trees-git-modified-color-override,
      var(--trees-status-modified)
    );
    --trees-git-added-color: var(
      --trees-git-added-color-override,
      var(--trees-status-added)
    );
    --trees-git-ignored-color: var(
      --trees-git-ignored-color-override,
      var(--trees-status-ignored)
    );
    --trees-git-deleted-color: var(
      --trees-git-deleted-color-override,
      var(--trees-status-deleted)
    );
    --trees-git-renamed-color: var(
      --trees-git-renamed-color-override,
      var(--trees-status-renamed)
    );
    --trees-git-untracked-color: var(
      --trees-git-untracked-color-override,
      var(--trees-status-untracked)
    );

    --trees-icon-gray: light-dark(#84848a, #adadb1);
    --trees-icon-red: light-dark(#d52c36, #ff6762);
    --trees-icon-vermilion: light-dark(#ff8c5b, #d5512f);
    --trees-icon-orange: light-dark(#d47628, #ffa359);
    --trees-icon-yellow: light-dark(#d5a910, #ffd452);
    --trees-icon-green: light-dark(#199f43, #5ecc71);
    --trees-icon-teal: light-dark(#17a5af, #64d1db);
    --trees-icon-cyan: light-dark(#1ca1c7, #68cdf2);
    --trees-icon-blue: light-dark(#1a85d4, #69b1ff);
    --trees-icon-indigo: light-dark(#693acf, #9d6afb);
    --trees-icon-purple: light-dark(#a631be, #d568ea);
    --trees-icon-pink: light-dark(#d32a61, #ff678d);
    --trees-icon-mauve: light-dark(#594c5b, #79697b);

    --trees-file-icon-color-default: var(
      --trees-file-icon-color,
      var(--trees-icon-gray)
    );
    --trees-file-icon-color-astro: var(
      --trees-file-icon-color,
      var(--trees-icon-purple)
    );
    --trees-file-icon-color-babel: var(
      --trees-file-icon-color,
      var(--trees-icon-yellow)
    );
    --trees-file-icon-color-bash: var(
      --trees-file-icon-color,
      var(--trees-icon-green)
    );
    --trees-file-icon-color-biome: var(
      --trees-file-icon-color,
      var(--trees-icon-blue)
    );
    --trees-file-icon-color-bootstrap: var(
      --trees-file-icon-color,
      var(--trees-icon-indigo)
    );
    --trees-file-icon-color-browserslist: var(
      --trees-file-icon-color,
      var(--trees-icon-yellow)
    );
    --trees-file-icon-color-bun: var(
      --trees-file-icon-color,
      var(--trees-icon-mauve)
    );
    --trees-file-icon-color-c: var(
      --trees-file-icon-color,
      var(--trees-icon-blue)
    );
    --trees-file-icon-color-cpp: var(
      --trees-file-icon-color,
      var(--trees-icon-blue)
    );
    --trees-file-icon-color-claude: var(
      --trees-file-icon-color,
      var(--trees-icon-orange)
    );
    --trees-file-icon-color-css: var(
      --trees-file-icon-color,
      var(--trees-icon-indigo)
    );
    --trees-file-icon-color-database: var(
      --trees-file-icon-color,
      var(--trees-icon-purple)
    );
    --trees-file-icon-color-docker: var(
      --trees-file-icon-color,
      var(--trees-icon-blue)
    );
    --trees-file-icon-color-eslint: var(
      --trees-file-icon-color,
      var(--trees-icon-indigo)
    );
    --trees-file-icon-color-git: var(
      --trees-file-icon-vermilion,
      var(--trees-icon-vermilion)
    );
    --trees-file-icon-color-go: var(
      --trees-file-icon-color,
      var(--trees-icon-cyan)
    );
    --trees-file-icon-color-graphql: var(
      --trees-file-icon-color,
      var(--trees-icon-pink)
    );
    --trees-file-icon-color-html: var(
      --trees-file-icon-color,
      var(--trees-icon-orange)
    );
    --trees-file-icon-color-image: var(
      --trees-file-icon-color,
      var(--trees-icon-pink)
    );
    --trees-file-icon-color-javascript: var(
      --trees-file-icon-color,
      var(--trees-icon-yellow)
    );
    --trees-file-icon-color-json: var(
      --trees-file-icon-color,
      var(--trees-icon-orange)
    );
    --trees-file-icon-color-markdown: var(
      --trees-file-icon-color,
      var(--trees-icon-green)
    );
    --trees-file-icon-color-mcp: var(
      --trees-file-icon-color,
      var(--trees-icon-teal)
    );
    --trees-file-icon-color-npm: var(
      --trees-file-icon-color,
      var(--trees-icon-red)
    );
    --trees-file-icon-color-oxc: var(
      --trees-file-icon-cyan,
      var(--trees-icon-cyan)
    );
    --trees-file-icon-color-postcss: var(
      --trees-file-icon-color,
      var(--trees-icon-red)
    );
    --trees-file-icon-color-prettier: var(
      --trees-file-icon-color,
      var(--trees-icon-teal)
    );
    --trees-file-icon-color-python: var(
      --trees-file-icon-color,
      var(--trees-icon-blue)
    );
    --trees-file-icon-color-react: var(
      --trees-file-icon-color,
      var(--trees-icon-cyan)
    );
    --trees-file-icon-color-ruby: var(
      --trees-file-icon-color,
      var(--trees-icon-red)
    );
    --trees-file-icon-color-rust: var(
      --trees-file-icon-color,
      var(--trees-icon-orange)
    );
    --trees-file-icon-color-sass: var(
      --trees-file-icon-color,
      var(--trees-icon-pink)
    );
    --trees-file-icon-color-svg: var(
      --trees-file-icon-color,
      var(--trees-icon-orange)
    );
    --trees-file-icon-color-svelte: var(
      --trees-file-icon-color,
      var(--trees-icon-red)
    );
    --trees-file-icon-color-svgo: var(
      --trees-file-icon-color,
      var(--trees-icon-green)
    );
    --trees-file-icon-color-swift: var(
      --trees-file-icon-color,
      var(--trees-icon-orange)
    );
    --trees-file-icon-color-table: var(
      --trees-file-icon-color,
      var(--trees-icon-teal)
    );
    --trees-file-icon-color-text: var(
      --trees-file-icon-color,
      var(--trees-icon-gray)
    );
    --trees-file-icon-color-tailwind: var(
      --trees-file-icon-color,
      var(--trees-icon-cyan)
    );
    --trees-file-icon-color-terraform: var(
      --trees-file-icon-color,
      var(--trees-icon-indigo)
    );
    --trees-file-icon-color-typescript: var(
      --trees-file-icon-color,
      var(--trees-icon-blue)
    );
    --trees-file-icon-color-vite: var(
      --trees-file-icon-color,
      var(--trees-icon-purple)
    );
    --trees-file-icon-color-vscode: var(
      --trees-file-icon-color,
      var(--trees-icon-blue)
    );
    --trees-file-icon-color-vue: var(
      --trees-file-icon-color,
      var(--trees-icon-green)
    );
    --trees-file-icon-color-wasm: var(
      --trees-file-icon-color,
      var(--trees-icon-indigo)
    );
    --trees-file-icon-color-webpack: var(
      --trees-file-icon-color,
      var(--trees-icon-blue)
    );
    --trees-file-icon-color-yml: var(
      --trees-file-icon-color,
      var(--trees-icon-red)
    );
    --trees-file-icon-color-zig: var(
      --trees-file-icon-color,
      var(--trees-icon-orange)
    );
    --trees-file-icon-color-zip: var(
      --trees-file-icon-color,
      var(--trees-icon-orange)
    );

    --trees-level-gap: var(
      --trees-level-gap-override,
      calc(8px * var(--trees-density))
    );
    --trees-item-padding-x: var(
      --trees-item-padding-x-override,
      calc(8px * var(--trees-density))
    );
    --trees-item-margin-x: var(
      --trees-item-margin-x-override,
      calc(2px * var(--trees-density))
    );
    --trees-item-row-gap: var(
      --trees-item-row-gap-override,
      calc(6px * var(--trees-density))
    );
    --trees-icon-width: var(--trees-icon-width-override, 16px);
    --trees-icon-nudge: var(
      --trees-icon-nudge-override,
      calc(1px * var(--trees-density))
    );
    --trees-row-height: var(--trees-item-height, 30px);
    --trees-git-lane-width: var(--trees-git-lane-width-override, 12px);
    --trees-action-lane-width: var(
      --trees-action-lane-width-override,
      calc(var(--trees-icon-width) + 2px)
    );
    /* Keep the floating trigger aligned with the row's action lane. Going in
       from the root's right edge: the scroll container reserves
       \`--trees-padding-inline\` of effective inset on each side (its asymmetric
       padding formula cancels the scrollbar gutter on the right), the row
       sits inside that inset, and its trailing \`--trees-item-padding-x\` is the
       action lane itself. The trigger's own focus-ring margin then trims one
       pixel back so the button's visible right edge lines up with the lane. */
    --trees-context-menu-trigger-inline-offset: calc(
      var(--trees-padding-inline) + var(--trees-item-padding-x) -
        var(--trees-focus-ring-width)
    );

    --trees-scrollbar-gutter: var(--trees-scrollbar-gutter-override, 6px);
    --trees-padding-inline: var(--trees-padding-inline-override, 16px);

    color-scheme: light dark;
    display: flex;
    flex-direction: column;
    font-size: var(--trees-font-size);
    color: var(--trees-fg);
    background-color: var(--trees-bg);
    --truncate-marker-background-color: var(--trees-bg);
    --truncate-marker-background-overlay-color: transparent;
    font-family: var(--trees-font-family);
    font-weight: var(--trees-font-weight-regular);
  }

  :host([data-file-tree-virtualized='true']) {
    height: 100%;
    overflow: hidden;
  }

  [data-file-tree-virtualized-wrapper='true'] {
    height: 100%;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  [data-file-tree-virtualized-root='true'] {
    height: 100%;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  [data-file-tree-virtualized-scroll='true'],
  [data-file-tree-scrollbar-measure='true'] {
    --trees-scrollbar-thumb-current: transparent;
    overflow-y: auto;
    scrollbar-gutter: stable;

    &:hover {
      --trees-scrollbar-thumb-current: var(--trees-scrollbar-thumb);
    }

    &::-webkit-scrollbar {
      width: var(--trees-scrollbar-gutter);
      height: var(--trees-scrollbar-gutter);
    }

    &::-webkit-scrollbar-track {
      background: transparent;
    }

    &::-webkit-scrollbar-thumb {
      background-color: var(--trees-scrollbar-thumb-current);
      border: 1px solid transparent;
      background-clip: content-box;
      border-radius: calc(var(--trees-scrollbar-gutter) / 2);
    }

    &::-webkit-scrollbar-corner {
      background-color: transparent;
    }
  }

  /* These are styles for a temporarily generated element to measure the size
   * of the scrollbar.  It's intended to be somewhat similar in scrollbar style
   * scope to the scrollable tree so \`--trees-scrollbar-gutter-measured\` is an
   * accurate reflection of the size the scrollbar gutter takes up. */
  [data-file-tree-scrollbar-measure='true'] {
    position: absolute;
    top: 0;
    left: 0;
    visibility: hidden;
    pointer-events: none;
    width: 100px;
    height: 100px;
  }

  @supports (-moz-appearance: none) {
    [data-file-tree-virtualized-scroll='true'],
    [data-file-tree-scrollbar-measure='true'] {
      scrollbar-width: thin;
      scrollbar-color: var(--trees-scrollbar-thumb-current) transparent;
    }
  }

  [data-file-tree-virtualized-scroll='true'] {
    position: relative;
    overflow-y: auto;
    flex: 1 1 0;
    min-height: 0;
    padding-inline: max(
        calc(var(--trees-padding-inline) - var(--trees-item-margin-x)),
        0px
      )
      /* NOTE(amadeus): We can assume that all Webkit based browser gutters
       * will align to the value of '--trees-scrollbar-gutter', however if not, then
       * \`--trees-scrollbar-gutter-measured\` should correct it. Mostly we are
       * hoping to avoid SSR alignment jumps if possible. In non-SSR'd environments
       * \`--trees-scrollbar-gutter-measured\` should always be immediately available.
       */
      max(
        calc(
          var(--trees-padding-inline) - var(--trees-item-margin-x) -
            var(
              --trees-scrollbar-gutter-measured,
              var(--trees-scrollbar-gutter)
            )
        ),
        0px
      );
  }

  @supports (-moz-appearance: none) {
    [data-file-tree-virtualized-scroll='true'] {
      padding-inline: max(
          calc(var(--trees-padding-inline) - var(--trees-item-margin-x)),
          0px
        )
        /* NOTE(amadeus): However on Firefox it can vary a little bit, but most
         * likely the majority of cases will default to a 0px width scrollbar lets
         * inherit that first to avoid SSR jumps. In non-SSR'd environments
         * \`--trees-scrollbar-gutter-measured\` should always be immediately available.
         */
        max(
          calc(
            var(--trees-padding-inline) - var(--trees-item-margin-x) -
              var(--trees-scrollbar-gutter-measured, 0px)
          ),
          0px
        );
    }
  }

  [data-file-tree-sticky-overlay='true'] {
    position: sticky;
    top: 0;
    height: 0;
    z-index: 4;
    overflow: visible;
    pointer-events: none;
  }

  /* The overlay DOM is kept populated even at scrollTop=0 so the browser has
   * the rendered rows on hand the moment scrolling begins — otherwise the
   * compositor paints a scrolled frame before React can mount the overlay,
   * and the topmost sticky folder jumps up by a couple of pixels before it
   * "snaps" into its pinned position. We hide it via CSS whenever the scroll
   * is at the top and no scroll is in progress, so the preview doesn't leak
   * through at rest. \`data-overlay-reveal\` is stamped on the root only when
   * the user initiates a scroll while already at the top — exactly the case
   * where we need the pre-mounted overlay to be visible through the first
   * compositor frame. It is deliberately distinct from the general
   * \`data-is-scrolling\` flag so a scroll that ends at the top (e.g. ArrowUp
   * navigation) re-hides the overlay the instant the scroll lands, rather
   * than waiting for the hover-suppression timer to elapse. */
  [data-file-tree-virtualized-root='true'][data-scroll-at-top='true']:not(
      [data-overlay-reveal]
    )
    [data-file-tree-sticky-overlay='true'] {
    visibility: hidden;
  }

  [data-file-tree-sticky-overlay-content='true'] {
    background-color: var(--trees-bg);
    position: relative;
    pointer-events: none;
  }

  [data-file-tree-virtualized-list='true'] {
    background-color: var(--trees-bg);
    position: relative;
    min-height: 100%;
    width: 100%;
    overflow-anchor: none;

    &[data-is-scrolling] {
      pointer-events: none;
    }
  }

  [data-file-tree-virtualized-sticky-offset='true'] {
    contain: layout size;
  }

  [data-file-tree-virtualized-sticky='true'] {
    position: sticky;
    top: 0;
    width: 100%;
    display: flex;
    flex-direction: column;
    isolation: isolate;
    /* Promote to its own compositor layer so text inside the window is
     * rasterized once and GPU-translated during scroll. Without this, the
     * browser re-paints the window (and its text) at every scroll frame,
     * which produces visible 1px shake / character tearing. */
    will-change: transform;
  }

  [data-file-tree-search-container] {
    display: flex;
    padding: 0;
    padding-inline: var(--trees-padding-inline);
    margin-bottom: var(--trees-item-row-gap);
  }

  [data-file-tree-search-input] {
    --trees-focus-ring-width: 2px;
    font-family: var(--trees-font-family);
    font-size: var(--trees-font-size);
    flex: 1;
    height: var(--trees-row-height);
    /* 1px breathing room so the focus-visible outline isn't clipped when the
     * input sits flush against the top of the scroll container. */
    margin-block: 1px;
    padding-inline: var(--trees-item-padding-x);
    line-height: var(--trees-row-height);
    color: var(--trees-search-fg);
    background-color: var(--trees-search-bg);
    border: 1px solid var(--trees-border-color);
    border-radius: var(--trees-border-radius);
    outline: none;

    &::placeholder {
      color: color-mix(
        in lab,
        var(--trees-search-fg) 65%,
        var(--trees-search-bg)
      );
    }

    &:focus-visible,
    &[data-file-tree-search-input-fake-focus='true'] {
      outline: var(--trees-focus-ring-width) solid var(--trees-focus-ring-color);
      outline-offset: var(--trees-focus-ring-offset);
    }
  }

  /* The wrapper for the tree items */
  [role='tree'] {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: var(--trees-gap-override, 0);
  }

  /* LIST ITEM */
  [data-type='item'] {
    color: inherit;
    font-family: var(--trees-font-family);
    font-size: var(--trees-font-size);
    text-align: start;
    outline: none;
    background-color: var(--trees-bg);
    border: none;
    position: relative;

    padding: 0 var(--trees-item-padding-x);
    margin: 0 var(--trees-item-margin-x);
    cursor: pointer;
    -webkit-user-select: none;
            user-select: none;
    -webkit-touch-callout: none;
    touch-action: manipulation;
    display: flex;
    flex: 0 0 var(--trees-row-height);
    align-items: center;
    height: var(--trees-row-height);
    line-height: var(--trees-row-height);
    gap: var(--trees-item-row-gap);
    border-radius: var(--trees-border-radius);
    /* Row states may be translucent, so markers paint the tree background first
     * and then the state color on top to avoid compositing the same alpha twice. */
    --truncate-marker-background-color: var(--trees-bg);
    --truncate-marker-background-overlay-color: transparent;
    --truncate-marker-block-inset: 0px;

    &:hover,
    &[data-item-context-hover='true'] {
      background-color: var(--trees-bg-muted);
      --truncate-marker-background-overlay-color: var(--trees-bg-muted);
    }

    &[data-item-focused='true'],
    &:focus-visible {
      z-index: 2;

      /* Flattened segment markers sit high enough to cover the row outline unless
       * their painted background is inset by the focus ring width. */
      [data-item-flattened-subitems] {
        --truncate-marker-block-inset: var(--trees-focus-ring-width);
      }

      &::before {
        position: absolute;
        inset: 0;
        content: '';
        display: block;
        border-radius: var(--trees-border-radius);
        outline: var(--trees-focus-ring-width) solid
          var(--trees-focus-ring-color);
        outline-offset: var(--trees-focus-ring-offset);
        pointer-events: none;
      }

      &[data-item-selected='true']::before {
        outline-color: var(--trees-selected-focused-border-color);
      }
    }

    &[data-item-selected='true'] {
      color: var(--trees-selected-fg);
      background-color: var(--trees-selected-bg);
      --truncate-marker-background-overlay-color: var(--trees-selected-bg);
      z-index: 3;

      [data-item-section='icon'] {
        color: var(--trees-selected-fg);
      }
    }

    &[data-item-search-match='true'] {
      font-weight: var(--trees-search-font-weight);
    }
  }

  [data-type='item'][data-file-tree-sticky-row='true'] {
    pointer-events: auto;
  }

  /* Sticky rows opt back into pointer events because the overlay wrapper is
   * inert. During scroll, put them back under the same hover suppression as
   * the virtualized list so translucent hover states and menu triggers do not
   * paint over rows moving beneath the sticky stack. */
  [data-file-tree-virtualized-root='true'][data-is-scrolling]
    [data-type='item'][data-file-tree-sticky-row='true'] {
    pointer-events: none;
  }

  [data-file-tree-virtualized-root='true'][data-is-scrolling]
    [data-type='item'][data-file-tree-sticky-row='true']:hover:not(
      [data-item-selected='true']
    ),
  [data-file-tree-virtualized-root='true'][data-is-scrolling]
    [data-type='item'][data-file-tree-sticky-row='true'][data-item-context-hover='true']:not(
      [data-item-selected='true']
    ) {
    background-color: var(--trees-bg);
    --truncate-marker-background-overlay-color: transparent;
  }

  [data-item-selected='true']:has(+ [data-item-selected='true']) {
    border-bottom-left-radius: 0;
    border-bottom-right-radius: 0;
  }

  [data-item-selected='true'] + [data-item-selected='true'] {
    border-top-left-radius: 0;
    border-top-right-radius: 0;
  }

  /* Flattened Directory Parts */
  [data-item-flattened-subitems] {
    display: inline-flex;
    align-items: center;
    gap: 2px;
  }
  [data-item-flattened-subitem]:hover,
  [data-item-flattened-subitem-drag-target='true'] {
    text-decoration: underline;
  }

  /* Icon for each item */
  [data-item-section='icon'] {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--trees-fg-muted);
    fill: currentColor;
    width: var(--trees-icon-width);
  }

  :where([data-item-section='icon'] > [data-icon-token]) {
    color: var(--trees-fg-muted);
  }

  [data-file-tree-colored-icons='true'] {
    [data-icon-token='astro'] {
      color: var(--trees-file-icon-color-astro);
    }
    [data-icon-token='babel'] {
      color: var(--trees-file-icon-color-babel);
    }
    [data-icon-token='bash'] {
      color: var(--trees-file-icon-color-bash);
    }
    [data-icon-token='biome'] {
      color: var(--trees-file-icon-color-biome);
    }
    [data-icon-token='bootstrap'] {
      color: var(--trees-file-icon-color-bootstrap);
    }
    [data-icon-token='browserslist'] {
      color: var(--trees-file-icon-color-browserslist);
    }
    [data-icon-token='bun'] {
      color: var(--trees-file-icon-color-bun);
    }
    [data-icon-token='c'] {
      color: var(--trees-file-icon-color-c);
    }
    [data-icon-token='cpp'] {
      color: var(--trees-file-icon-color-cpp);
    }
    [data-icon-token='claude'] {
      color: var(--trees-file-icon-color-claude);
    }
    [data-icon-token='css'] {
      color: var(--trees-file-icon-color-css);
    }
    [data-icon-token='database'] {
      color: var(--trees-file-icon-color-database);
    }
    [data-icon-token='default'] {
      color: var(--trees-file-icon-color-default);
    }
    [data-icon-token='docker'] {
      color: var(--trees-file-icon-color-docker);
    }
    [data-icon-token='eslint'] {
      color: var(--trees-file-icon-color-eslint);
    }
    [data-icon-token='git'] {
      color: var(--trees-file-icon-color-git);
    }
    [data-icon-token='go'] {
      color: var(--trees-file-icon-color-go);
    }
    [data-icon-token='graphql'] {
      color: var(--trees-file-icon-color-graphql);
    }
    [data-icon-token='html'] {
      color: var(--trees-file-icon-color-html);
    }
    [data-icon-token='image'] {
      color: var(--trees-file-icon-color-image);
    }
    [data-icon-token='javascript'] {
      color: var(--trees-file-icon-color-javascript);
    }
    [data-icon-token='json'] {
      color: var(--trees-file-icon-color-json);
    }
    [data-icon-token='markdown'] {
      color: var(--trees-file-icon-color-markdown);
    }
    [data-icon-token='mcp'] {
      color: var(--trees-file-icon-color-mcp);
    }
    [data-icon-token='npm'] {
      color: var(--trees-file-icon-color-npm);
    }
    [data-icon-token='oxc'] {
      color: var(--trees-file-icon-color-oxc);
    }
    [data-icon-token='postcss'] {
      color: var(--trees-file-icon-color-postcss);
    }
    [data-icon-token='prettier'] {
      color: var(--trees-file-icon-color-prettier);
    }
    [data-icon-token='python'] {
      color: var(--trees-file-icon-color-python);
    }
    [data-icon-token='react'] {
      color: var(--trees-file-icon-color-react);
    }
    [data-icon-token='ruby'] {
      color: var(--trees-file-icon-color-ruby);
    }
    [data-icon-token='rust'] {
      color: var(--trees-file-icon-color-rust);
    }
    [data-icon-token='sass'] {
      color: var(--trees-file-icon-color-sass);
    }
    [data-icon-token='svg'] {
      color: var(--trees-file-icon-color-svg);
    }
    [data-icon-token='svelte'] {
      color: var(--trees-file-icon-color-svelte);
    }
    [data-icon-token='svgo'] {
      color: var(--trees-file-icon-color-svgo);
    }
    [data-icon-token='swift'] {
      color: var(--trees-file-icon-color-swift);
    }
    [data-icon-token='table'] {
      color: var(--trees-file-icon-color-table);
    }
    [data-icon-token='text'] {
      color: var(--trees-file-icon-color-text);
    }
    [data-icon-token='tailwind'] {
      color: var(--trees-file-icon-color-tailwind);
    }
    [data-icon-token='terraform'] {
      color: var(--trees-file-icon-color-terraform);
    }
    [data-icon-token='typescript'] {
      color: var(--trees-file-icon-color-typescript);
    }
    [data-icon-token='vite'] {
      color: var(--trees-file-icon-color-vite);
    }
    [data-icon-token='vscode'] {
      color: var(--trees-file-icon-color-vscode);
    }
    [data-icon-token='vue'] {
      color: var(--trees-file-icon-color-vue);
    }
    [data-icon-token='wasm'] {
      color: var(--trees-file-icon-color-wasm);
    }
    [data-icon-token='webpack'] {
      color: var(--trees-file-icon-color-webpack);
    }
    [data-icon-token='yml'] {
      color: var(--trees-file-icon-color-yml);
    }
    [data-icon-token='zig'] {
      color: var(--trees-file-icon-color-zig);
    }
    [data-icon-token='zip'] {
      color: var(--trees-file-icon-color-zip);
    }
  }

  /* Chevron rotation and visual alignment */
  /* Chevron pointing down */
  [data-icon-name='file-tree-icon-chevron'] {
    &[data-align-capitals='false'] {
      transform: translate(0, var(--trees-icon-nudge));
    }
    &[data-align-capitals='true'] {
      transform: translate(0, 0);
    }
  }

  [data-item-section='content'] {
    flex: 0 1 auto;
    text-align: start;
    min-width: 0;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    /* Breaks middle truncate component to also set this */
    /* white-space: nowrap; */
  }

  [data-item-section='decoration'] {
    flex: 1 1 0;
    min-width: 0;
    display: flex;
    justify-content: flex-end;
    text-align: end;
    overflow: hidden;
    color: var(--trees-fg-muted);
  }

  [data-item-section='decoration'] > span {
    min-width: 0;
    max-width: 100%;
    display: inline-flex;
    align-items: center;
    justify-content: flex-end;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  [data-item-section='git'],
  [data-item-section='action'] {
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  [data-item-section='git'] {
    width: var(--trees-git-lane-width);
  }

  [data-item-section='action'] {
    width: var(--trees-action-lane-width);
    color: var(--trees-fg-muted);
    fill: currentColor;
    pointer-events: none;
  }

  [data-item-section='git'] > span,
  [data-item-section='action'] > span {
    width: 100%;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }

  [data-item-action-affordance='decorative'] {
    opacity: 0.85;
  }

  [data-item-rename-input] {
    appearance: none;
    width: 100%;
    min-width: 0;
    height: calc(var(--trees-row-height) - 4px);
    font-family: inherit;
    font-size: inherit;
    /* line-height: calc(var(--trees-row-height) - 8px); */
    color: inherit;
    background-color: transparent;
    border: 0;
    padding-inline: 6px;
    outline: none;
    box-sizing: border-box;
  }

  [data-item-section='content']:has([data-item-rename-input])
    ~ [data-item-section='action'],
  [data-item-section='content']:has([data-item-rename-input])
    ~ [data-item-section='decoration'] {
    display: none;
  }

  /* Chevron pointing right */
  [aria-expanded='false'][data-item-type='folder']
    > [data-item-section='icon']
    > [data-icon-name='file-tree-icon-chevron'] {
    &[data-align-capitals='true'] {
      transform: rotate(-90deg)
        translate(
          calc(var(--trees-icon-nudge) / 2),
          calc(var(--trees-icon-nudge) / 2)
        );
    }
    &[data-align-capitals='false'] {
      transform: rotate(-90deg)
        translate(
          calc(var(--trees-icon-nudge) / 2 * -1),
          calc(var(--trees-icon-nudge) / 2)
        );
    }
  }

  /* LIST IDENTATION */
  /* Spacing container */
  [data-item-section='spacing'] {
    display: flex;
    flex-direction: row;
    align-items: center;
    justify-content: center;
    height: var(--trees-row-height);
    padding-left: calc(calc(var(--trees-icon-width) / 2) - 0.5px);

    &:empty {
      padding-left: 0;
    }
  }

  /* Spacing per level */
  [data-item-section='spacing-item'] {
    transform: translateX(-0.25px);
    display: inline-block;
    border-left: 1px solid var(--trees-indent-guide-bg);
    height: 100%;
    margin-right: calc(var(--trees-level-gap) - 1px);
    opacity: 0;
    transition: opacity 150ms ease;

    & + & {
      margin-left: calc(
        var(--trees-item-row-gap) + calc(var(--trees-icon-width) / 2) - 0.5px
      );
    }
  }

  :host(:hover) [data-item-section='spacing-item'] {
    opacity: 0.75;
  }

  /* Git status indicator */

  /* This is a folder that contains a git change */
  [data-item-contains-git-change='true'] > [data-item-section='git'] {
    color: var(--trees-git-modified-color);
    opacity: 0.5;
    fill: currentColor;
  }

  /* These are files that have a git change */
  [data-item-git-status] {
    &
      > :where([data-item-section='icon'])
      > :where(:not([data-icon-name='file-tree-icon-chevron'])) {
      color: var(--trees-item-git-status-color);
    }
    & > [data-item-section='content'] {
      color: var(--trees-item-git-status-color);
    }
    & > [data-item-section='git'] {
      color: var(--trees-item-git-status-color);
      font-weight: var(--trees-font-weight-semibold);
    }
  }

  [data-item-git-status='added'] {
    --trees-item-git-status-color: var(--trees-git-added-color);
  }

  [data-item-git-status='deleted'] {
    --trees-item-git-status-color: var(--trees-git-deleted-color);
  }

  [data-item-git-status='ignored'] {
    --trees-item-git-status-color: var(--trees-git-ignored-color);

    & > [data-item-section='icon'] {
      opacity: 0.5;
    }
  }

  [data-item-section='git'] [data-icon-name='file-tree-icon-dot'] {
    /* this is a nudge to align the dot with the likely lowercase text. it's slightly
    generalizable, but other fonts are gonna need other nudges i assume */
    transform: translateY(calc(0.65ex - 50%));
  }

  [data-item-git-status='modified'] {
    --trees-item-git-status-color: var(--trees-git-modified-color);
  }

  [data-item-git-status='renamed'] {
    --trees-item-git-status-color: var(--trees-git-renamed-color);
  }

  [data-item-git-status='untracked'] {
    --trees-item-git-status-color: var(--trees-git-untracked-color);
  }

  /* Drag and drop */
  [data-item-drag-target='true'] {
    background-color: var(--trees-selected-bg);
  }

  [data-item-dragging='true'] {
    opacity: 0.5;
  }

  /* Lock icon for locked paths (sibling of content) */
  [data-item-section='lock'] {
    flex: 0 0 auto;
    margin-left: auto;
    display: flex;
    align-items: center;
    color: var(--trees-fg-muted);
  }
  [data-item-section='lock'] svg {
    display: block;
  }

  [data-type='header-slot'] {
    display: block;
    flex: 0 0 auto;
  }

  [data-type='context-menu-wash'] {
    position: absolute;
    inset: 0;
    z-index: 3;
    background-color: transparent;
    touch-action: none;
  }

  [data-type='context-menu-anchor'] {
    position: absolute;
    top: 0;
    right: var(--trees-context-menu-trigger-inline-offset);
    z-index: 4;
    display: none;
    align-items: center;

    &[data-visible='true'] {
      display: flex;
    }
  }

  /* Hide the floating trigger while the scroll container is actively moving.
   * The anchor is positioned against the root, not the scroll content, so its
   * \`top\` follows the row via a React state update — one frame behind the
   * compositor. That delay is visible as the trigger hovering over the wrong
   * row during the first frame of a scroll. The \`data-is-scrolling\` flag on
   * the root is flipped synchronously on \`wheel\`/\`touchmove\`/\`keydown\` before
   * the compositor commits the next paint, so this selector hides the anchor
   * in the same frame the scroll begins. */
  [data-file-tree-virtualized-root='true'][data-is-scrolling]
    [data-type='context-menu-anchor'] {
    display: none;
  }

  [data-type='context-menu-anchor'] > slot[name='context-menu'] {
    display: block;
    width: 0;
    min-width: 0;
    flex: 0 0 0;
    overflow: visible;
  }

  /* Single floating context menu trigger */
  [data-type='context-menu-trigger'] {
    all: unset;
    align-items: center;
    justify-content: center;
    width: var(--trees-action-lane-width);
    color: var(--trees-fg-muted);
    fill: currentColor;
    cursor: pointer;
    font-family: var(--trees-font-family);
    font-size: var(--trees-font-size);
    border-top-right-radius: var(--trees-border-radius);
    border-bottom-right-radius: var(--trees-border-radius);
    margin: var(--trees-focus-ring-width);
    height: calc(var(--trees-row-height) - var(--trees-focus-ring-width) * 2);
    border-width: 0;
    transition: color 120ms ease;

    display: flex;
  }

  [data-type='context-menu-trigger']:hover,
  [data-type='context-menu-trigger'][aria-expanded='true'] {
    color: var(--trees-fg);
  }

  /** @pierre/truncate css here, manually copy pasted for now */
  [data-truncate-container] {
    /* CUSTOM TO TREES, TO SUPPORT THE OUTLINE */
    margin-top: -1px;
    margin-bottom: -1px;

    /* Width of the fade from default marker to text */
    --truncate-internal-marker-fade-width: var(
      --truncate-marker-fade-width,
      2px
    );
    /* Width of the solid color between the fade from the default marker to the text */
    --truncate-internal-marker-gap: var(--truncate-marker-gap, 0px);
    /* Opacity of the marker 'color' property, not of the element itself */
    --truncate-internal-marker-opacity: var(--truncate-marker-opacity, 50%);
    /* Opacity of the marker 'color' property specifically for the middle truncate, not opacity of the element itself */
    --truncate-internal-middle-marker-opacity: var(
      --truncate-middle-marker-opacity,
      80%
    );
    /* Background color of the default marker */
    --truncate-internal-marker-background-color: var(
      --truncate-marker-background-color,
      light-dark(white, black)
    );
    --truncate-internal-marker-background-overlay-color: var(
      --truncate-marker-background-overlay-color,
      transparent
    );
    --truncate-internal-marker-block-inset: var(
      --truncate-marker-block-inset,
      0px
    );
    /* Duration of the fade out animation for the marker */
    --truncate-internal-marker-fade-out-duration: var(
      --truncate-marker-fade-out-duration,
      0ms
    );
    /* Duration of the fade in animation for the marker */
    --truncate-internal-marker-fade-in-duration: var(
      --truncate-marker-fade-in-duration,
      100ms
    );

    /* FADE Variant specifics */
    --truncate-internal-fade-marker-color: var(
      --truncate-fade-marker-color,
      #000
    );
    --truncate-internal-fade-marker-width: var(
      --truncate-fade-marker-width,
      0.2lh
    );

    /*
    In some special cases people might be adding spacing in other ways
    that would benefit from being able to override this, however the container
    query below can't use this and would need to be redeclared with the overridden
    value. It's a bad time, but better than nothing.
    */
    --truncate-internal-single-line-height: 1lh;

    height: var(--truncate-internal-single-line-height);
    min-width: 0;
    overflow: hidden;
  }

  [data-truncate-marker] {
    display: flex;
    position: absolute;
    height: var(--truncate-internal-single-line-height);
    padding-block: var(--truncate-internal-marker-block-inset);
    box-sizing: border-box;
    align-items: center;
    background-clip: content-box;
    z-index: 2;
    color: color-mix(
      in srgb,
      currentColor var(--truncate-internal-marker-opacity),
      transparent
    );

    /* Core trick for hiding the marker until overflow occurs */
    opacity: 0;
    transition: opacity var(--truncate-internal-marker-fade-out-duration)
      ease-in-out;
  }

  @container measure (height > 1lh) {
    [data-truncate-marker] {
      opacity: 1;
      transition: opacity var(--truncate-internal-marker-fade-in-duration)
        ease-in-out;
    }
  }

  [data-truncate-grid] {
    display: grid;
    position: relative;
  }

  [data-truncate-content='visible'] {
    white-space: nowrap;
  }

  [data-truncate-content='overflow'] {
    opacity: 0;
    pointer-events: none;
    -webkit-user-select: none;
            user-select: none;
    word-break: break-all;
    margin-top: calc(-1 * var(--truncate-internal-single-line-height));
  }

  [data-truncate-marker-cell] {
    container: measure / size;
    overflow: visible;
    -webkit-user-select: none;
            user-select: none;
    pointer-events: none;
  }

  [data-truncate-container='truncate'] {
    & [data-truncate-grid] {
      grid-template-columns: minmax(0, max-content) 0;
    }
    & [data-truncate-marker] {
      right: 0;
    }
    & [data-truncate-fade] {
      margin-right: calc(-2 * var(--truncate-internal-fade-marker-width));
    }
  }

  [data-truncate-container='fruncate'] {
    & [data-truncate-grid] {
      grid-template-columns: 0 minmax(0, max-content) auto;
    }
    & [data-truncate-content] {
      direction: rtl;
    }
    & [data-truncate-content] > span {
      unicode-bidi: plaintext;
    }
    & [data-truncate-fade] {
      margin-left: calc(-2 * var(--truncate-internal-fade-marker-width));
    }
  }

  [data-truncate-variant='default'] {
    & [data-truncate-marker] {
      background-color: var(--truncate-internal-marker-background-color);
      background-image: linear-gradient(
        var(--truncate-internal-marker-background-overlay-color),
        var(--truncate-internal-marker-background-overlay-color)
      );
    }
    & [data-truncate-marker]::after,
    & [data-truncate-marker]::before {
      content: '';
      position: absolute;
      width: calc(
        var(--truncate-internal-marker-fade-width) +
          var(--truncate-internal-marker-gap)
      );
      inset-block-start: var(--truncate-internal-marker-block-inset);
      height: max(
        0px,
        calc(
          var(--truncate-internal-single-line-height) -
            var(--truncate-internal-marker-block-inset) * 2
        )
      );
      background-color: var(--truncate-internal-marker-background-color);
      background-image: linear-gradient(
        var(--truncate-internal-marker-background-overlay-color),
        var(--truncate-internal-marker-background-overlay-color)
      );
      mask-image: linear-gradient(
        var(--truncate-internal-fade-dir),
        #000 0%,
        #000 var(--truncate-internal-marker-gap),
        transparent 100%
      );
    }
    & [data-truncate-marker]::after {
      --truncate-internal-fade-dir: to right;
      right: calc(
        -1 *
          (
            var(--truncate-internal-marker-fade-width) +
              var(--truncate-internal-marker-gap)
          )
      );
    }
    & [data-truncate-marker]::before {
      --truncate-internal-fade-dir: to left;
      left: calc(
        -1 *
          (
            var(--truncate-internal-marker-fade-width) +
              var(--truncate-internal-marker-gap)
          )
      );
    }
  }

  [data-truncate-variant='fade'] {
    & [data-truncate-marker] {
      background: transparent;
    }
  }

  [data-truncate-fade] {
    box-shadow:
      0 0 calc(var(--truncate-internal-fade-marker-width) / 2)
        var(--truncate-internal-fade-marker-color),
      0 0 var(--truncate-internal-fade-marker-width)
        var(--truncate-internal-fade-marker-color);
    width: calc(var(--truncate-internal-fade-marker-width) * 2);
    height: calc(
      var(--truncate-internal-single-line-height) -
        (var(--truncate-internal-fade-marker-width) * 2)
    );
    margin: var(--truncate-internal-fade-marker-width) 0;
  }

  [data-truncate-group-container='middle'] {
    & [data-truncate-container] {
      --truncate-marker-opacity: var(--truncate-internal-middle-marker-opacity);
    }

    display: flex;
    min-width: 0;

    & > div {
      min-width: 0;
    }

    & > div[data-truncate-segment-priority='1'] {
      flex: 0 1 max-content;
    }
    & > div[data-truncate-segment-priority='2'] {
      flex: 0 999999 max-content;
    }
  }
}
`,fe=`@layer base, unsafe;`;function pe(e){return`${fe}
@layer base {
  ${e}
}`}function me(e){return`${fe}
@layer unsafe {
  ${e}
}`}var he=new WeakMap;function ge(e){let t=he.get(e);if(t!=null)return t;let n=document.createElement(`div`);n.setAttribute(ae,`true`);let r=document.createElement(`div`);r.style.position=`relative`,r.style.height=`200%`,n.appendChild(r),e.appendChild(n);let i=Math.max(n.offsetWidth-n.clientWidth,0);return n.remove(),he.set(e,i),i}function _e(e,t){if(!e.isConnected)return;let n=ge(t);if(n==null)return;let r=t.querySelector(`style[${oe}]`),i=r instanceof HTMLStyleElement?r:document.createElement(`style`);r instanceof HTMLStyleElement||(i.setAttribute(oe,``),t.appendChild(i)),i.textContent=`:host { ${se}: ${n}px; }`}var j;function ve(e){if(typeof CSSStyleSheet<`u`&&typeof CSSStyleSheet.prototype.replaceSync==`function`&&`adoptedStyleSheets`in e){j??(j=new CSSStyleSheet,j.replaceSync(pe(de)));let t=!1;try{e.adoptedStyleSheets=[j],t=!0}catch{}if(t){e.querySelector(`style[${k}]`)?.remove();return}}if(e.querySelector(`style[data-file-tree-style]`)==null){let t=document.createElement(`style`);t.setAttribute(k,``),t.textContent=pe(de),e.prepend(t)}}function M(e,t){ye(e,t),ve(t),_e(e,t)}function ye(e,t){let n=e.querySelector(`template[shadowrootmode="open"], template[data-file-tree-shadowrootmode="open"]`);n instanceof HTMLTemplateElement&&(t.childNodes.length>0||(t.appendChild(n.content.cloneNode(!0)),n.hasAttribute(`shadowrootmode`)&&n.remove()))}if(typeof HTMLElement<`u`&&customElements.get(`file-tree-container`)==null){class e extends HTMLElement{constructor(){super()}connectedCallback(){let e=this.shadowRoot??this.attachShadow({mode:`open`});M(this,e)}}if(customElements.define(ie,e),typeof document<`u`)for(let e of Array.from(document.querySelectorAll(ie)))e instanceof HTMLElement&&M(e,e.shadowRoot??e.attachShadow({mode:`open`}))}var be=`<svg data-icon-sprite aria-hidden="true" width="0" height="0">
  <symbol id="file-tree-icon-chevron" viewBox="0 0 16 16">
    <path d="M12.4697 5.46973C12.7626 5.17684 13.2374 5.17684 13.5303 5.46973C13.8232 5.76262 13.8232 6.23738 13.5303 6.53028L8.53028 11.5303C8.23738 11.8232 7.76262 11.8232 7.46973 11.5303L2.46973 6.53028C2.17684 6.23738 2.17684 5.76262 2.46973 5.46973C2.76262 5.17684 3.23738 5.17684 3.53028 5.46973L8 9.93946L12.4697 5.46973Z" fill="currentcolor"/>
  </symbol>
  <symbol id="file-tree-icon-dot" viewBox="0 0 6 6">
    <circle cx="3" cy="3" r="3" />
  </symbol>
  <symbol id="file-tree-icon-file" viewBox="0 0 16 16">
    <path fill="currentColor" d="M8 1v3a3 3 0 0 0 3 3h3v5.5a2.5 2.5 0 0 1-2.5 2.5h-7A2.5 2.5 0 0 1 2 12.5v-9A2.5 2.5 0 0 1 4.5 1z" class="bg" opacity=".5"/>
    <path fill="currentColor" d="M9.5 1a.5.5 0 0 1 .354.146l4 4A.5.5 0 0 1 14 5.5V6h-3a2 2 0 0 1-2-2V1z" class="fg"/>
  </symbol>
  <symbol id="file-tree-icon-lock" viewBox="0 0 16 16">
    <path fill="currentcolor" d="M4 5.336V4a4 4 0 1 1 8 0v1.336c1.586.54 2 1.843 2 4.664v1c0 4.118-.883 5-5 5H7c-4.117 0-5-.883-5-5v-1c0-2.821.414-4.124 2-4.664M5.5 4v1.054Q6.166 4.998 7 5h2q.834-.002 1.5.054V4a2.5 2.5 0 0 0-5 0m-2 6v1c0 .995.055 1.692.167 2.193.107.483.246.686.35.79s.307.243.79.35c.5.112 1.198.167 2.193.167h2c.995 0 1.692-.055 2.193-.166.483-.108.686-.247.79-.35.104-.105.243-.308.35-.791.112-.5.167-1.198.167-2.193v-1c0-.995-.055-1.692-.166-2.193-.108-.483-.247-.686-.35-.79-.105-.104-.308-.243-.791-.35C10.693 6.555 9.995 6.5 9 6.5H7c-.995 0-1.692.055-2.193.167-.483.107-.686.246-.79.35s-.243.307-.35.79C3.555 8.307 3.5 9.005 3.5 10" />
  </symbol>
  <symbol id="file-tree-icon-ellipsis" viewBox="0 0 16 16">
    <path d="M5 8.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0M9.5 8.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0M14 8.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0" />
  </symbol>
</svg>`,xe=`<symbol id="file-tree-builtin-astro" viewBox="0 0 16 16">
  <path fill="currentColor" d="M6.08 13.92c-.63-.57-.81-1.79-.55-2.67.45.56 1.08.73 1.73.83 1 .15 1.99.1 2.92-.37l.32-.19q.13.38.08.78a2.1 2.1 0 0 1-.9 1.5q-.3.24-.61.43c-.64.44-.81.95-.57 1.69l.02.08a1.7 1.7 0 0 1-.74-.64 2 2 0 0 1-.3-.98q0-.27-.02-.52-.07-.61-.61-.62a.7.7 0 0 0-.75.6z" class="bg" opacity=".6"/>
  <path fill="currentColor" d="M2.5 11.1s1.86-.9 3.72-.9l1.4-4.39c.05-.21.2-.36.38-.36s.33.15.38.36l1.4 4.38c2.2 0 3.72.92 3.72.92l-3.16-8.69q-.13-.4-.45-.42H6.11q-.3.02-.45.42z" class="fg"/>
</symbol>`,Se=`<symbol id="file-tree-builtin-babel" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M9.49.5q1.92.05 2.66.54 1.27.6 1.35 1.52v.23a4 4 0 0 1-.53 1.9l-1.38 1.24q-.74.38-.72.63c.77.82 1.33 1.29.85 2.42q-.47 1.1-2.04 2.28c-.5.32-1.88 1.35-2.96 1.86-1.64.77-3.1 1.4-4.65 1.89-.51.16-1.5.16-1.5.16L.5 15A76 76 0 0 0 5.76 3.49q-.1-.08-.1-.2.1 0 .32-.35l-.03-.09q-1.17.39-2.38 1.3l-.13.03q0-.1-.21-.16-.46.31-.82.7l-.13-.19.16-.06-.03-.16-.34.29L2 4.5q.36-.48.72-.54l.04-.1V3.8q.16 0 .15-.06l.13-.06a6 6 0 0 0 1.13-.9v-.03H4.1l-.12.07q0-.1-.1-.1l-.15.07-.04-.1q.93-.52 1.63-1.05Q7.89.65 9.5.5M8.46 7.83l-.32.04c-1.31.54-2.31.82-2.91.88a71 71 0 0 0-2.2 4.54h.07q.58-.04 3.04-1.42.13 0 1.66-1.05L9.18 9.7v.03q.45-.2.81-1.3v-.2q-.5-.46-1.53-.4m.28-5.75c-.5.1-.75.19-.72.38l-1.16 2.6q-.17.1-.34.95-.3.48-.25.77v.1l.22.05A15 15 0 0 1 8.86 6c1.1-.71 2.12-1.38 2.8-2.54q.24-.33.21-.54-.02-.33-.4-.54c-.54 0-1.07-.34-1.63-.28l-.94-.03z" clip-rule="evenodd"/>
</symbol>`,N=`<symbol id="file-tree-builtin-bash" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8 1C2.24 1 1 2.24 1 8s1.24 7 7 7 7-1.24 7-7-1.24-7-7-7" class="bg" opacity=".2"/>
  <path fill="currentColor" d="M11.5 11a.5.5 0 0 1 0 1h-3a.5.5 0 0 1 0-1zM7 6.75C7 6.42 6.64 6 6 6s-1 .42-1 .75q-.01.25.22.41.26.21.89.35.74.14 1.28.53c.37.29.61.7.61 1.21 0 .87-.68 1.5-1.5 1.7v.55a.5.5 0 0 1-1 0v-.56c-.82-.18-1.5-.82-1.5-1.69a.5.5 0 0 1 1 0c0 .33.36.75 1 .75s1-.42 1-.75q.01-.25-.22-.41a2 2 0 0 0-.89-.35q-.74-.14-1.28-.53A1.5 1.5 0 0 1 4 6.75c0-.87.68-1.5 1.5-1.7V4.5a.5.5 0 0 1 1 0v.56c.82.18 1.5.82 1.5 1.69a.5.5 0 0 1-1 0" class="fg-stroke"/>
</symbol>`,Ce=`<symbol id="file-tree-builtin-biome" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8 2 4.88 7.35a7 7 0 0 1 3.7-.13l1.04.25-.99 4.16-1.05-.25a2.7 2.7 0 0 0-3.07 1.45l-.98-.47a4 4 0 0 1 1.07-1.31 3.8 3.8 0 0 1 3.23-.71l.5-2.08a6 6 0 0 0-5.07 1.12A5.9 5.9 0 0 0 1 14h14z"/>
</symbol>`,we=`<symbol id="file-tree-builtin-bootstrap" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M11.72 1.5A2.5 2.5 0 0 1 14.2 4q.02 1.08.3 2.09c.22.73.56 1.24 1.08 1.45.22.08.4.27.4.5s-.18.43-.4.51q-.76.34-1.08 1.45c-.2.65-.27 1.32-.3 2a2.5 2.5 0 0 1-2.48 2.5H4.25A2.6 2.6 0 0 1 1.7 12c-.04-.85-.1-1.68-.22-2.04C1.26 9.23.92 8.7.4 8.5.18 8.42 0 8.23 0 8s.18-.42.4-.5q.77-.35 1.09-1.46c.1-.36.17-1.19.2-2.04a2.6 2.6 0 0 1 2.56-2.5z" class="bg" clip-rule="evenodd" opacity=".2"/>
  <path fill="currentColor" fill-rule="evenodd" d="M8.47 4.54c1.23 0 2.04.68 2.04 1.73 0 .73-.55 1.39-1.24 1.5v.04c.94.1 1.58.77 1.58 1.7 0 1.2-.9 1.95-2.37 1.95H5.97a.3.3 0 0 1-.2-.08.3.3 0 0 1-.08-.2V4.82a.3.3 0 0 1 .08-.2.3.3 0 0 1 .2-.08zm-1.7 6.04h1.49q1.47-.01 1.49-1.15Q9.74 8.31 8.2 8.3H6.77zm0-5.16v2.06h1.21c.93 0 1.45-.38 1.45-1.06 0-.65-.44-1-1.22-1z" class="fg" clip-rule="evenodd"/>
</symbol>`,Te=`<symbol id="file-tree-builtin-browserslist" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8.88 6.96c0 3.82 3.72 4.7 5.7 3.74-.23.9-1.04 1.67-2.35 1.93-.02.4.42 1.28.82 1.63-.9.35-1.94-.12-2.51-.48a5 5 0 0 0-.32 1.87c-.68 0-1.57-1-1.8-1.37-.3.18-.85 1.15-.96 1.72a2.4 2.4 0 0 1-.81-.86 2.4 2.4 0 0 1-.3-1.15c-.38.27-1.48.95-1.99 1.18-.25-.58-.15-1.3 0-2.06-.21.12-1.8.27-2.43.12.32-.36.75-1.19.94-1.57A4.5 4.5 0 0 1 .44 10.6c.48-.22.97-.53 1.49-1.06C1.26 9.17.24 8.64 0 7.7a6 6 0 0 0 1.79-.32C1.28 7.08.44 6.15.6 5.01c.42.21 1.3.37 1.73.3a3.4 3.4 0 0 1-.25-2.75 5 5 0 0 0 1.48 1c-.08-.8.3-2.31.8-2.71.2.46.73 1.21 1.08 1.4.09-.61.87-2.06 1.57-2.25 0 .5.27 1.4.5 1.67.51-.54 2.25-1.44 3.64-1.13-.43.45-.75.61-.86.98 1.05 0 2.78.34 4.27 1.93-2.34-.89-5.69.56-5.69 3.5" class="bg" opacity=".5"/>
  <path fill="currentColor" d="M11.21 3.59a4.1 4.1 0 0 0 2.47 2.89c.24-.22.61-.38.95-.19.76.44.2 1.26-.34 1.66l-.07.06a13 13 0 0 1-4.49 1.61l-.3-.43a10.5 10.5 0 0 0 4.13-1.31 1 1 0 0 0 .23-.25.5.5 0 0 0-.21-.69l-.15-.06a4.5 4.5 0 0 1-1.77-1.31 4.5 4.5 0 0 1-.88-1.77q.2-.12.43-.21"/>
  <path fill="currentColor" d="M10.36 5.18a.4.4 0 0 0-.03.38c.09.2.3.3.46.23s.24-.3.15-.5l-.01-.02q.23.13.34.39a.83.83 0 0 1-.43 1.08.8.8 0 0 1-1.08-.43.83.83 0 0 1 .6-1.13"/>
</symbol>`,Ee=`<symbol id="file-tree-builtin-bun" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8 14c3.87 0 7-2.46 7-5.49 0-1.88-1.2-3.53-3.04-4.52q-1.1-.61-1.84-1.07C9.2 2.35 8.64 2 8 2s-1.36.45-2.31 1.03A29 29 0 0 1 4.04 4C2.2 4.98 1 6.63 1 8.51 1 11.54 4.13 14 8 14M7.18 3.88q.3-.66.3-1.37c0-.08.11-.1.13-.01.38 1.57-.53 2.35-1.2 2.61-.08.03-.12-.07-.06-.12a3 3 0 0 0 .83-1.12m1.2-.05a3 3 0 0 0-.45-1.3V2.5c-.04-.07.05-.15.1-.1 1.15 1.2.77 2.3.33 2.87-.05.05-.13 0-.11-.08q.21-.67.13-1.37m1.04-.32a3 3 0 0 0-.94-1.02v-.01c-.06-.05-.01-.16.07-.12 1.51.61 1.61 1.8 1.43 2.5l-.03.03a.07.07 0 0 1-.1-.06 3 3 0 0 0-.43-1.32m-2.97.32c-.36.3-.74.43-1.2.56q-.11 0-.1-.1a3.5 3.5 0 0 0 1.76-1.57s.09-.07.1.04c0 .18-.2.76-.56 1.07m2.89 6.36q-.13.52-.55.88a1.3 1.3 0 0 1-.75.35 1.3 1.3 0 0 1-.77-.35 1.7 1.7 0 0 1-.54-.88.13.13 0 0 1 .15-.15h2.31a.14.14 0 0 1 .15.15M6.15 8.95a1.1 1.1 0 0 1-1.39-.14A1.1 1.1 0 0 1 5.12 7a1.1 1.1 0 0 1 1.2.25 1.1 1.1 0 0 1-.17 1.69m4.96 0a1.1 1.1 0 0 1-1.4-.14 1.1 1.1 0 0 1 .37-1.8 1.1 1.1 0 0 1 1.2.25 1.1 1.1 0 0 1 .24 1.2 1 1 0 0 1-.41.5"/>
</symbol>`,De=`<symbol id="file-tree-builtin-c" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M8 1q.084 0 .166.021.098.023.186.075c1.055.624 4.22 2.486 5.277 3.11.085.05.15.112.209.192h-.002l.028.037a.5.5 0 0 1 .103.21q.031.102.033.21v6.29a.71.71 0 0 1-.347.616l-5.307 3.144a.68.68 0 0 1-.693 0l-5.307-3.144A.72.72 0 0 1 2 11.145V4.832a.71.71 0 0 1 .346-.612l5.288-3.126A.7.7 0 0 1 7.992 1zm2.901 4.349a3.75 3.75 0 1 0 0 5.302l-1.06-1.06a2.25 2.25 0 1 1 0-3.182z" clip-rule="evenodd"/>
</symbol>`,Oe=`<symbol id="file-tree-builtin-claude" viewBox="0 0 16 16">
  <path fill="currentColor" d="M3.75 10.31 6.5 8.77l.04-.14-.04-.07h-.14l-.46-.03-1.57-.04-1.38-.07-1.33-.07-.34-.07L1 7.86l.03-.21.28-.18.4.03.89.07 1.33.08.97.06 1.43.16h.22l.03-.1-.07-.05-.06-.06-1.39-.92-1.48-.98-.79-.57-.42-.28-.2-.28-.1-.6.39-.41.52.04.12.03.52.4 1.12.86L6.2 6.04l.2.17.09-.06.01-.04-.1-.15-.76-1.46-.85-1.46-.37-.6-.1-.36a1 1 0 0 1-.06-.42l.42-.59.25-.07.6.08.22.2.36.84.58 1.3.9 1.77.29.53.14.47.04.14h.1v-.07l.07-1 .14-1.22.14-1.57.04-.45.23-.53.42-.28.36.15.28.41-.04.25-.16 1.08-.36 1.7-.21 1.14h.12l.14-.15.58-.76.97-1.2.42-.5.5-.51.32-.25h.6l.44.66-.2.68-.61.79-.52.65-.74 1-.45.8.04.05h.1l1.68-.36.9-.16 1.06-.18.5.23.05.22-.2.48-1.15.28-1.34.28-2 .46-.04.01.03.04.9.09.4.03h.94l1.77.14.46.28.27.37-.04.28-.72.37-.95-.23-2.24-.53-.76-.18h-.11v.06l.64.63L12 10.86l1.48 1.35.07.34-.18.28-.2-.03-1.29-.98-.5-.42-1.12-.95h-.07v.1l.25.38 1.37 2.05.07.63-.1.2-.36.14-.38-.08-.8-1.12-.85-1.26-.66-1.15-.07.05-.4 4.23-.19.21-.42.17-.35-.28-.2-.42.2-.87.23-1.12.18-.9.17-1.1.1-.36v-.03h-.1l-.84 1.16-1.27 1.72-1 1.07-.24.1-.42-.22.04-.39.22-.32 1.4-1.8.84-1.1.57-.64-.02-.07h-.04l-3.7 2.4-.66.09-.28-.28.03-.42.14-.14 1.12-.77z"/>
</symbol>`,ke=`<symbol id="file-tree-builtin-cpp" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M8 1q.084 0 .166.021.098.023.186.075c1.055.624 4.22 2.486 5.277 3.11.085.05.15.112.209.192h-.002l.028.037a.5.5 0 0 1 .103.21q.031.102.033.21v6.29a.71.71 0 0 1-.347.616l-5.307 3.144a.68.68 0 0 1-.693 0l-5.307-3.144A.72.72 0 0 1 2 11.145V4.832a.71.71 0 0 1 .346-.612l5.288-3.126A.7.7 0 0 1 7.992 1zm2.901 4.349a3.75 3.75 0 1 0 0 5.302l-1.06-1.06a2.25 2.25 0 1 1 0-3.182z" clip-rule="evenodd"/>
</symbol>`,P=`<symbol id="file-tree-builtin-css" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8 15c-5.76 0-7-1.24-7-7V2a1 1 0 0 1 1-1h6c5.77 0 7 1.24 7 7s-1.24 7-7 7" class="vector" opacity=".2"/>
  <path fill="currentColor" d="M10.1 9.19h.73c.03.49.22.6 1 .6.76 0 .93-.12.93-.68 0-.52-.17-.67-.94-.85-1.38-.3-1.68-.56-1.68-1.47 0-1.05.3-1.29 1.67-1.29 1.29 0 1.57.2 1.6 1.13h-.74c-.01-.34-.17-.42-.85-.42-.77 0-.94.1-.94.58 0 .42.17.55.96.73 1.36.3 1.66.58 1.66 1.59 0 1.14-.31 1.39-1.73 1.39-1.39 0-1.69-.24-1.67-1.31m-3.9 0h.74c.03.49.21.6.99.6.76 0 .93-.12.93-.68 0-.52-.17-.67-.93-.85-1.39-.3-1.69-.56-1.69-1.47 0-1.05.3-1.29 1.67-1.29 1.3 0 1.58.2 1.6 1.13h-.73c-.02-.34-.18-.42-.85-.42-.78 0-.95.1-.95.58 0 .42.17.55.96.73 1.37.3 1.67.58 1.67 1.59 0 1.14-.32 1.39-1.74 1.39-1.38 0-1.68-.24-1.66-1.31m-1.22 0h.75c-.09 1.07-.37 1.31-1.56 1.31-1.37 0-1.68-.45-1.68-2.5 0-1.96.36-2.5 1.68-2.5 1.16 0 1.44.25 1.52 1.35h-.76c-.08-.52-.22-.64-.76-.64-.74 0-.9.33-.9 1.78 0 1.47.16 1.8.9 1.8.58 0 .74-.11.8-.6"/>
</symbol>`,Ae=`<symbol id="file-tree-builtin-database" viewBox="0 0 16 16">
  <path fill="currentColor" d="M14.953 9.733a12.4 12.4 0 0 1-.244 1.936c-.207.933-.532 1.58-.996 2.044s-1.11.789-2.044.996C10.73 14.918 9.533 15 8 15s-2.73-.082-3.669-.291c-.933-.207-1.58-.532-2.044-.996s-.789-1.11-.996-2.044c-.122-.547-.2-1.182-.244-1.92q.23.364.532.667c.64.639 1.482 1.031 2.533 1.265 1.046.232 2.33.315 3.884.315 1.555 0 2.838-.083 3.884-.315 1.051-.234 1.893-.626 2.532-1.265a4 4 0 0 0 .541-.683"/>
  <path fill="currentColor" d="M14.93 5.924c-.046.663-.118 1.24-.23 1.743-.207.932-.532 1.579-.995 2.042s-1.11.789-2.042.996c-.938.209-2.135.291-3.667.291-1.531 0-2.729-.082-3.667-.29-.932-.208-1.579-.534-2.042-.997s-.789-1.11-.996-2.042a12 12 0 0 1-.227-1.683l.016-.188a4 4 0 0 0 .5.62c.638.639 1.48 1.031 2.532 1.265 1.046.232 2.33.315 3.884.315 1.555 0 2.838-.083 3.884-.315 1.051-.234 1.893-.626 2.532-1.265.192-.192.357-.404.506-.633z"/>
  <path fill="currentColor" d="M8 1c1.533 0 2.73.082 3.669.291.933.207 1.58.533 2.044.996.403.404.904.944.91 1.695.004.764-.509 1.318-.918 1.727-.463.463-1.11.789-2.042.996-.938.209-2.135.291-3.667.291-1.531 0-2.729-.082-3.667-.29-.932-.208-1.579-.534-2.042-.997-.406-.406-.915-.953-.915-1.71 0-.758.509-1.305.915-1.712.464-.463 1.11-.789 2.044-.996C5.27 1.082 6.467 1 8 1"/>
</symbol>`,je=`<symbol id="file-tree-builtin-default" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8 1v3a3 3 0 0 0 3 3h3v5.5a2.5 2.5 0 0 1-2.5 2.5h-7A2.5 2.5 0 0 1 2 12.5v-9A2.5 2.5 0 0 1 4.5 1z" class="bg" opacity=".4"/>
  <path fill="currentColor" d="M9.5 1a.5.5 0 0 1 .354.146l4 4A.5.5 0 0 1 14 5.5V6h-3a2 2 0 0 1-2-2V1z" class="fg"/>
</symbol>`,Me=`<symbol id="file-tree-builtin-docker" viewBox="0 0 16 16">
  <path fill="currentColor" d="M15.85 6.54c-.05-.04-.45-.36-1.31-.36q-.34 0-.68.06a2.7 2.7 0 0 0-1.14-1.79l-.23-.14-.15.23a3 3 0 0 0-.4 1q-.24 1.01.26 1.84c-.4.24-1.03.3-1.17.3H.5a.5.5 0 0 0-.5.52q-.01 1.46.46 2.83.55 1.5 1.6 2.18c.79.5 2.08.79 3.54.79q.96 0 1.94-.18a8 8 0 0 0 2.55-.97 7 7 0 0 0 1.73-1.5 10 10 0 0 0 1.7-3.06h.15a2.4 2.4 0 0 0 1.8-.7 2 2 0 0 0 .47-.74l.06-.2z"/>
  <path fill="currentColor" d="M1.48 7.36h1.4a.14.14 0 0 0 .14-.13V5.91q-.01-.12-.13-.14H1.48a.13.13 0 0 0-.13.14v1.32q.02.13.13.13m1.94 0h1.41a.14.14 0 0 0 .13-.13V5.91q-.01-.12-.13-.14h-1.4a.13.13 0 0 0-.13.14v1.32q0 .13.12.13m1.98 0h1.4q.13 0 .14-.13V5.91a.13.13 0 0 0-.14-.14H5.4q-.1.01-.12.14v1.32q0 .13.12.13m1.95 0h1.42q.1 0 .12-.13V5.91q0-.12-.12-.14H7.35q-.1.01-.12.14v1.32q.01.13.12.13M3.42 5.5h1.41c.07 0 .13-.08.13-.15V4.03a.13.13 0 0 0-.13-.14h-1.4q-.12 0-.13.14v1.31q0 .13.12.15m1.98 0h1.4c.08 0 .14-.08.14-.15V4.03q0-.13-.14-.14H5.4q-.1 0-.12.14v1.31q0 .13.12.15m1.95 0h1.42c.06 0 .12-.08.12-.15V4.03q-.01-.13-.12-.14H7.35q-.1 0-.12.14v1.31q.01.13.12.15m0-1.9h1.42q.1-.02.12-.14v-1.3Q8.88 2 8.77 2H7.35q-.1 0-.12.14v1.3q.01.13.12.14m1.97 3.78h1.4a.13.13 0 0 0 .14-.13V5.91q-.01-.12-.13-.14H9.32q-.1.01-.12.14v1.32q.01.13.12.13" opacity=".5"/>
</symbol>`,Ne=`<symbol id="file-tree-builtin-eslint" viewBox="0 0 16 16">
  <path fill="currentColor" d="M11.16 6.1 8.12 4.35a.3.3 0 0 0-.24 0L4.84 6.1a.3.3 0 0 0-.12.2v3.5q0 .14.12.22l3.04 1.74q.12.08.24 0l3.04-1.74a.2.2 0 0 0 .13-.22V6.3a.3.3 0 0 0-.13-.2" opacity=".5"/>
  <path fill="currentColor" d="m.1 7.69 3.63-6.3A.8.8 0 0 1 4.37 1h7.26c.26 0 .5.17.64.4l3.63 6.27a.8.8 0 0 1 0 .75l-3.63 6.24a.7.7 0 0 1-.64.34H4.37a.7.7 0 0 1-.64-.34L.1 8.41a.7.7 0 0 1 0-.72m3 3.02q.01.15.14.23l4.63 2.66q.13.06.26 0l4.63-2.66a.3.3 0 0 0 .14-.23V5.4a.3.3 0 0 0-.14-.23L8.13 2.52a.3.3 0 0 0-.26 0L3.24 5.17a.3.3 0 0 0-.14.23z"/>
</symbol>`,Pe=`<symbol id="file-tree-builtin-font" viewBox="0 0 16 16">
  <path fill="currentColor" d="M12.3 13c-1.59 0-2.68-.99-2.68-2.5 0-1.43 1-2.34 2.88-2.35h2.16v-.83c0-1.08-.62-1.68-1.73-1.68-1.05 0-1.66.54-1.73 1.36H9.93c.09-1.43 1.06-2.48 3.05-2.48 1.75 0 3.02.95 3.02 2.68v5.66h-1.29v-1.02h-.04c-.41.66-1.16 1.16-2.37 1.16m.36-1.12c1.14 0 2-.72 2-1.74v-.96H12.6c-1.12 0-1.6.54-1.6 1.28 0 .97.8 1.42 1.66 1.42m-11.24.98H0L3.8 2h1.39l3.8 10.86H7.54l-1.08-3.2H2.5zm3.09-9.25h-.04l-1.6 4.95H6.1z"/>
</symbol>`,Fe=`<symbol id="file-tree-builtin-git" viewBox="0 0 16 16">
  <path fill="currentColor" d="M14.74 7.38 8.62 1.26a.9.9 0 0 0-1.27 0L6.08 2.53l1.61 1.61a1.07 1.07 0 0 1 1.36 1.37l1.55 1.55a1.07 1.07 0 0 1 1.1 1.77 1.07 1.07 0 0 1-1.74-1.16L8.5 6.22v3.8a1.07 1.07 0 1 1-.89-.02V6.15a1.07 1.07 0 0 1-.58-1.4l-1.58-1.6-4.2 4.2a.9.9 0 0 0 0 1.27l6.12 6.12a.9.9 0 0 0 1.27 0l6.09-6.09a.9.9 0 0 0 0-1.27"/>
</symbol>`,Ie=`<symbol id="file-tree-builtin-go" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M4.41 4.57A3.2 3.2 0 0 1 6.87 5q.74.49 1.08 1.29.08.12-.1.16l-1.55.4c-.14.03-.15.04-.27-.1a1 1 0 0 0-.44-.34 1.6 1.6 0 0 0-1.68.14q-.95.61-.94 1.73c0 .73.52 1.33 1.25 1.43q.95.1 1.58-.6l.25-.34h-1.8c-.19 0-.24-.12-.17-.27.12-.28.34-.76.47-1a.3.3 0 0 1 .24-.14h2.98a4 4 0 0 1 .64-1.19 4 4 0 0 1 2.6-1.52 3.5 3.5 0 0 1 2.64.46q1.13.73 1.31 2.04a3.5 3.5 0 0 1-1.06 3.09q-.93.92-2.23 1.17l-.74.08a3.5 3.5 0 0 1-2.27-.8 3 3 0 0 1-.93-1.42 4 4 0 0 1-.39.61 4 4 0 0 1-2.64 1.56 3.3 3.3 0 0 1-2.5-.6 3 3 0 0 1-1.18-2.03 3.5 3.5 0 0 1 .8-2.67 4 4 0 0 1 2.6-1.58M13.1 7.5a1.53 1.53 0 0 0-1.9-1.21q-1.3.3-1.62 1.59a1.5 1.5 0 0 0 .85 1.72q.77.33 1.52-.05a2 2 0 0 0 1.18-1.74q0-.17-.03-.3" clip-rule="evenodd"/>
</symbol>`,Le=`<symbol id="file-tree-builtin-graphql" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M8 1a1.25 1.25 0 0 1 1.18 1.65l2.8 1.61q.33-.25.77-.26a1.25 1.25 0 0 1 .48 2.4v3.2a1.25 1.25 0 1 1-1.25 2.13l-2.8 1.62A1.25 1.25 0 0 1 8 15a1.25 1.25 0 0 1-1.18-1.65l-2.8-1.62q-.33.26-.77.27a1.25 1.25 0 0 1-.48-2.4V6.4a1.25 1.25 0 1 1 1.25-2.14l2.8-1.61A1.25 1.25 0 0 1 8 1M4.44 11.14l-.06.13 2.75 1.58a1.25 1.25 0 0 1 1.74 0l2.74-1.58-.05-.13zm3.89-7.68a1.3 1.3 0 0 1-.66 0L4.03 9.77q.37.3.45.78h7.04q.08-.48.45-.78zM4.38 4.73a1.24 1.24 0 0 1-1.02 1.76v3.02l.13.01 3.67-6.35-.03-.02zm4.46-1.56 3.67 6.35.13-.01V6.49a1.25 1.25 0 0 1-1.03-1.76L8.87 3.15z" clip-rule="evenodd"/>
</symbol>`,Re=`<symbol id="file-tree-builtin-html" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8 1C2.24 1 1 2.24 1 8s1.24 7 7 7 7-1.24 7-7-1.24-7-7-7" class="bg" opacity=".2"/>
  <path fill="currentColor" d="M10.48 3.76a.5.5 0 0 1 .4.58L10.6 5.8h1.14a.5.5 0 0 1 0 1h-1.32L10 9.2h1.08a.5.5 0 0 1 0 1H9.8l-.3 1.64a.5.5 0 1 1-.98-.18l.27-1.46H6.4l-.3 1.64a.5.5 0 1 1-.98-.18l.27-1.46H4.25a.5.5 0 0 1 0-1h1.32L6 6.8H4.93a.5.5 0 0 1 0-1H6.2l.3-1.64a.5.5 0 1 1 .98.18L7.2 5.8h2.4l.3-1.64a.5.5 0 0 1 .58-.4M6.58 9.2h2.4l.44-2.4h-2.4z" class="fg"/>
</symbol>`,ze=`<symbol id="file-tree-builtin-image" viewBox="0 0 16 16">
  <path fill="currentColor" d="M12.5 2A2.5 2.5 0 0 1 15 4.5v4.67l-4.05-3.54-4.08 4.08-3-2L1 10.6V4.5A2.5 2.5 0 0 1 3.5 2z" opacity=".3"/>
  <path fill="currentColor" d="M15 10.5v1a2.5 2.5 0 0 1-2.5 2.5h-9a2.5 2.5 0 0 1-2.46-2.04L4 9l3 2 4-4zm-7-5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0"/>
</symbol>`,Be=`<symbol id="file-tree-builtin-javascript" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8 1C2.24 1 1 2.24 1 8s1.24 7 7 7 7-1.24 7-7-1.24-7-7-7" class="bg" opacity=".2"/>
  <path fill="currentColor" d="M8.1 9.64h.95c.04.62.28.76 1.28.76s1.2-.14 1.2-.85c0-.66-.2-.85-1.2-1.07-1.79-.38-2.18-.7-2.18-1.86C8.15 5.3 8.54 5 10.31 5c1.67 0 2.04.26 2.07 1.42h-.95c-.02-.43-.23-.53-1.1-.53-1 0-1.22.14-1.22.74 0 .52.22.7 1.24.92 1.76.38 2.15.73 2.15 2 0 1.44-.4 1.75-2.24 1.75-1.8 0-2.18-.3-2.15-1.66M3.5 9.5h.98c0 .76.15.92.85.92.77 0 .94-.18.94-1.02V5.1h1v4.34c0 1.54-.35 1.87-1.92 1.87-1.55 0-1.89-.32-1.86-1.8"/>
</symbol>`,Ve=`<symbol id="file-tree-builtin-json" viewBox="0 0 16 16">
  <path fill="currentColor" d="M13.25 11.5V9.75a.5.5 0 0 1 .36-.48l.55-.15a1.16 1.16 0 0 0 0-2.24l-.55-.15a.5.5 0 0 1-.36-.48V4.5a2.5 2.5 0 0 0-2.5-2.5h-.25a.5.5 0 0 0 0 1h.25a1.5 1.5 0 0 1 1.5 1.5v1.75a1.5 1.5 0 0 0 1.09 1.44l.54.15a.16.16 0 0 1 0 .32l-.54.15a1.5 1.5 0 0 0-1.09 1.44v1.75a1.5 1.5 0 0 1-1.5 1.5h-.25a.5.5 0 0 0 0 1h.25a2.5 2.5 0 0 0 2.5-2.5m-10.5 0V9.75a.5.5 0 0 0-.36-.48l-.55-.15a1.16 1.16 0 0 1 0-2.24l.55-.15a.5.5 0 0 0 .36-.48V4.5A2.5 2.5 0 0 1 5.25 2h.25a.5.5 0 0 1 0 1h-.25a1.5 1.5 0 0 0-1.5 1.5v1.75a1.5 1.5 0 0 1-1.09 1.44l-.54.15a.16.16 0 0 0 0 .32l.54.15a1.5 1.5 0 0 1 1.09 1.45v1.74a1.5 1.5 0 0 0 1.5 1.5h.25a.5.5 0 0 1 0 1h-.25a2.5 2.5 0 0 1-2.5-2.5"/>
</symbol>`,He=`<symbol id="file-tree-builtin-markdown" viewBox="0 0 16 16">
  <path fill="currentColor" d="M1 12V4h2l2 2.5L7 4h2v8H7V7.5l-2 2-2-2V12zm9-3 3 3.5L16 9h-2V4h-2v5z"/>
</symbol>`,Ue=`<symbol id="file-tree-builtin-mcp" viewBox="0 0 16 16">
  <path fill="currentColor" d="M9.26-.04a3 3 0 0 1 2 .82 2.8 2.8 0 0 1 .8 2.35 2.9 2.9 0 0 1 2.41.8l.03.02a2.74 2.74 0 0 1 0 3.94l-5.8 5.69-.04.06-.02.07q0 .04.02.07.01.04.04.06l1.2 1.17a.55.55 0 0 1 0 .79.6.6 0 0 1-.81 0l-1.2-1.17a1.3 1.3 0 0 1 0-1.84L13.7 7.1a1.65 1.65 0 0 0 .37-1.82 2 2 0 0 0-.37-.54l-.03-.03a1.73 1.73 0 0 0-2.4 0L6.47 9.4l-.07.06a.58.58 0 0 1-.92-.18.6.6 0 0 1 .12-.6l4.85-4.76a1.65 1.65 0 0 0 0-2.36 1.73 1.73 0 0 0-2.4 0l-6.43 6.3a.6.6 0 0 1-.8 0 .55.55 0 0 1 0-.8L7.25.79a3 3 0 0 1 2-.82"/>
  <path fill="currentColor" d="M9.26 2.19a.6.6 0 0 1 .52.34.6.6 0 0 1 0 .43l-.12.18L4.9 7.79a1.65 1.65 0 0 0 0 2.36 1.73 1.73 0 0 0 2.4 0l4.75-4.66a.58.58 0 0 1 .93.18.6.6 0 0 1-.12.61l-4.75 4.66a2.9 2.9 0 0 1-4.01 0 2.75 2.75 0 0 1-.62-3.04A3 3 0 0 1 4.1 7l4.74-4.65a.6.6 0 0 1 .4-.16"/>
</symbol>`,We=`<symbol id="file-tree-builtin-nextjs" viewBox="0 0 16 16">
  <defs>
  <linearGradient id="a" x1="4.522" x2="14" y1="3.943" y2="16" gradientUnits="userSpaceOnUse">
  <stop stop-color="currentColor"/>
  <stop offset="1" stop-color="currentColor" stop-opacity="0"/>
  </linearGradient>
  </defs>
  <path fill="currentColor" d="M3 2h1.522v9.09H3z"/>
  <path fill="url(#a)" d="M4.903 2 15 15.075q-.565.5-1.195.925L4.522 3.943z"/>
  <path fill="currentColor" d="M12.172 2h-1.508v9.094h1.508z"/>
</symbol>`,Ge=`<symbol id="file-tree-builtin-npm" viewBox="0 0 16 16">
  <path fill="currentColor" d="M2 1a1 1 0 0 0-1 1v12a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V2a1 1 0 0 0-1-1z" class="vector" opacity=".2"/>
  <path fill="currentColor" d="M10.5 13H13V3H3v10h5V5.5h2.5z"/>
</symbol>`,Ke=`<symbol id="file-tree-builtin-oxc" viewBox="0 0 16 16">
  <path fill="currentColor" d="M9.5 1a.5.5 0 0 1 .5.5V3h3.5a.5.5 0 0 1 .38.83L10.5 7.69v1.44q.41.04.95-.16a4 4 0 0 0 .72-.35l.04-.03h.01a.5.5 0 0 1 .67.1l2 2.5a.5.5 0 0 1 0 .62c-.76.96-3.14 2.69-6.89 2.69s-6.13-1.73-6.89-2.69a.5.5 0 0 1 0-.62l2-2.5a.5.5 0 0 1 .67-.1l.05.03.16.09q.22.13.56.26.54.2.95.16V7.69L2.12 3.83A.5.5 0 0 1 2.5 3H6V1.5a.5.5 0 0 1 .5-.5zM7 3.5a.5.5 0 0 1-.5.5H3.6l2.78 3.17a.5.5 0 0 1 .12.33v2a.5.5 0 0 1-.28.45c-.7.35-1.5.15-2.02-.05a5 5 0 0 1-.58-.26l-1.46 1.84c.82.78 2.8 2.02 5.84 2.02s5.02-1.24 5.84-2.02l-1.46-1.83a5 5 0 0 1-.58.26c-.52.2-1.33.39-2.02.04a.5.5 0 0 1-.28-.45v-2a.5.5 0 0 1 .12-.33L12.4 4H9.5a.5.5 0 0 1-.5-.5V2H7z"/>
</symbol>`,qe=`<symbol id="file-tree-builtin-postcss" viewBox="0 0 16 16">
  <path fill="currentColor" d="M14.5 8a6.5 6.5 0 0 0-5.9-6.47l5.42 8.93A7 7 0 0 0 14.5 8M2.88 12A6.5 6.5 0 0 0 8 14.5c2.08 0 3.93-.98 5.12-2.5zm8.62-1h1.68L11.5 8.24zm-1-.55a4 4 0 0 1-.7.55h.7zM8 5.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5M5.5 11h.7a4 4 0 0 1-.7-.55zm-2.68 0H4.5V8.24zm3.76-6.2A4 4 0 0 1 8 4.5q.76 0 1.42.3L8 2.46zM1.5 8q0 1.31.48 2.46L7.4 1.53A6.5 6.5 0 0 0 1.5 8m14 0a7.5 7.5 0 0 1-.99 3.72l-.01.03-.02.03A7.5 7.5 0 0 1 8 15.5a7.5 7.5 0 0 1-6.5-3.75l-.01-.03A7.5 7.5 0 1 1 15.5 8"/>
</symbol>`,Je=`<symbol id="file-tree-builtin-prettier" viewBox="0 0 16 16">
  <path fill="currentColor" d="M6 12v1H4.93v-1zm1-2v1H2v-1zm6-4v1h-3V6zm-1-4v1H9V2z"/>
  <path fill="currentColor" d="M11.5 10v1H8v-1zM5 6v1H2V6zm5-2v1H9V4z" opacity=".8"/>
  <path fill="currentColor" d="M6 14v1H2v-1zm-.5-6v1H2V8zM13 4v1h-3V4zM4.93 2v1H2V2z" opacity=".6"/>
  <path fill="currentColor" d="M4.93 12v1H2v-1zM13 8v1H9V8zM5.5 4v1H2V4zM9 2v1H4.93V2z" opacity=".4"/>
</symbol>`,Ye=`<symbol id="file-tree-builtin-python" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8.33 8.4H10c1.16 0 1.9-.73 1.9-1.86V5.08q0-.24.25-.24h.74c.75 0 1.33.32 1.66.97q.4.73.41 1.46c.09.9.09 1.78-.24 2.67-.25.73-.75 1.3-1.58 1.46h-4.8c-.08 0-.25 0-.25.08v.4s.17.09.25.09h2.82q.34-.02.33.32v1.06c0 .56-.25.97-.75 1.13-.41.16-.83.33-1.24.4a7 7 0 0 1-2.98-.07 3 3 0 0 1-1.16-.49c-.33-.32-.58-.65-.5-1.14v-2.91c0-1.13.67-1.78 1.82-1.78q.89-.1 1.66-.08m2.32 4.86a.65.65 0 0 0-.66-.65c-.34 0-.67.33-.67.65s.33.57.67.65a.65.65 0 0 0 .66-.65" class="bg" opacity=".8"/>
  <path fill="currentColor" d="M7.67 7.6H6c-1.16 0-1.9.73-1.9 1.86v1.46q0 .24-.25.24h-.74c-.75 0-1.33-.32-1.66-.97a3 3 0 0 1-.41-1.46 6 6 0 0 1 .24-2.67c.25-.73.75-1.3 1.58-1.46h4.8c.08 0 .25 0 .25-.08v-.4s-.17-.09-.25-.09H4.85c-.24 0-.33-.08-.33-.32V2.65c0-.56.25-.97.75-1.13.41-.16.83-.33 1.24-.4a7 7 0 0 1 2.98.07c.41.09.83.25 1.16.49.33.32.58.65.5 1.13v2.92c0 1.14-.67 1.78-1.82 1.78-.58.08-1.16.08-1.66.08M5.35 2.73c0 .33.25.65.66.65.33 0 .66-.32.66-.65 0-.32-.33-.56-.66-.64a.65.65 0 0 0-.66.64" class="fg"/>
</symbol>`,F=`<symbol id="file-tree-builtin-react" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8 6.65c.73 0 1.31.6 1.31 1.35S8.73 9.35 8 9.35 6.69 8.75 6.69 8 7.27 6.65 8 6.65"/>
  <path fill="currentColor" fill-rule="evenodd" d="M8 2.55c1.3-.99 2.59-1.34 3.5-.8.92.55 1.27 1.87 1.08 3.53C14.06 5.94 15 6.9 15 8s-.94 2.06-2.42 2.72c.19 1.65-.16 2.98-1.08 3.52-.91.55-2.2.2-3.5-.8-1.3 1-2.58 1.35-3.5.8-.91-.54-1.27-1.87-1.08-3.52C1.94 10.06 1 9.1 1 8s.94-2.06 2.42-2.72c-.19-1.66.17-2.98 1.08-3.52s2.2-.2 3.5.8M4.26 11.2c-.08 1.34.28 2.03.68 2.26s1.15.22 2.25-.52l.11-.09a12 12 0 0 1-1.24-1.39 11 11 0 0 1-1.8-.41zm7.47-.15q-.83.27-1.79.41-.6.8-1.24 1.4l.11.08c1.1.74 1.86.76 2.25.52.4-.23.76-.92.68-2.26zm-3.04.54a14 14 0 0 1-1.38 0q.34.38.69.7.35-.32.7-.7M8 5.29q-.76 0-1.47.1A13 13 0 0 0 5.07 8a14 14 0 0 0 1.46 2.62 13 13 0 0 0 2.94 0A13 13 0 0 0 10.93 8a14 14 0 0 0-1.46-2.62A13 13 0 0 0 8 5.3M4.64 9.18q-.15.5-.25.96.44.16.94.27a15 15 0 0 1-.7-1.23m6.73 0a15 15 0 0 1-.7 1.23q.5-.11.95-.27a10 10 0 0 0-.25-.96M3.44 6.26C2.27 6.86 1.87 7.53 1.87 8s.4 1.14 1.57 1.74l.13.07q.18-.88.55-1.81a12 12 0 0 1-.55-1.8q-.07.02-.13.06m8.99-.07A12 12 0 0 1 11.88 8q.36.94.55 1.8l.13-.06c1.17-.6 1.56-1.27 1.56-1.74s-.39-1.14-1.56-1.74zm-7.1-.6q-.5.11-.94.27.1.46.25.96a15 15 0 0 1 .69-1.23m5.34 0a15 15 0 0 1 .7 1.23q.14-.5.24-.96-.44-.15-.94-.27M7.18 3.06c-1.09-.74-1.85-.76-2.24-.52s-.76.92-.69 2.26l.01.15a11 11 0 0 1 1.8-.41q.6-.8 1.24-1.4zm3.88-.52c-.4-.24-1.15-.22-2.25.52l-.12.08q.65.6 1.25 1.4.96.15 1.8.41v-.14c.08-1.35-.28-2.04-.68-2.27M8 3.7a10 10 0 0 0-.7.7 14 14 0 0 1 1.4 0 10 10 0 0 0-.7-.7" clip-rule="evenodd"/>
</symbol>`,Xe=`<symbol id="file-tree-builtin-ruby" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M11.04 2c.48 0 .92.23 1.18.6l2.54 3.65c.37.52.3 1.23-.15 1.69l-5.58 5.64a1.47 1.47 0 0 1-2.06 0L1.39 7.94a1.3 1.3 0 0 1-.15-1.7l2.54-3.63q.2-.3.5-.45.33-.16.68-.16zm.84 2.17a.5.5 0 0 0-.7-.05L8 6.84 4.83 4.12a.5.5 0 0 0-.65.76L6.65 7H3.5a.5.5 0 0 0 0 1h9a.5.5 0 0 0 0-1H9.35l2.48-2.12a.5.5 0 0 0 .05-.7" clip-rule="evenodd"/>
</symbol>`,Ze=`<symbol id="file-tree-builtin-rust" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M8 .8a.2.2 0 0 1 .18.1l.38.6.16.02.5-.53.01-.01a.2.2 0 0 1 .33.08l.25.68.16.05.59-.43h.02a.2.2 0 0 1 .3.14l.12.71.15.08.65-.3a.2.2 0 0 1 .2.02.2.2 0 0 1 .1.18l-.03.72.12.1.71-.16a.2.2 0 0 1 .25.25l-.17.7q.06.06.1.13l.73-.03A.2.2 0 0 1 14 4a.2.2 0 0 1 .02.2l-.3.66.08.14.71.12a.2.2 0 0 1 .14.32l-.43.59.05.16.68.25a.2.2 0 0 1 .07.35l-.53.49.01.16.62.38a.2.2 0 0 1 0 .36l-.62.38-.01.16.53.5a.2.2 0 0 1-.07.34l-.68.25-.05.16.43.59a.2.2 0 0 1-.14.32l-.72.12-.07.15.3.65a.2.2 0 0 1-.02.2.2.2 0 0 1-.18.1l-.72-.03-.1.13.16.7a.2.2 0 0 1-.25.25l-.7-.17-.13.1.03.73a.2.2 0 0 1-.1.18.2.2 0 0 1-.2.02l-.66-.3-.14.08-.12.71a.2.2 0 0 1-.32.14l-.59-.43-.16.05-.25.68a.2.2 0 0 1-.34.07l-.5-.53-.16.01-.38.62a.2.2 0 0 1-.36 0l-.38-.62-.16-.01-.5.53a.2.2 0 0 1-.34-.07l-.25-.68-.16-.05-.59.43a.2.2 0 0 1-.32-.14L5 13.78l-.15-.07-.65.3a.2.2 0 0 1-.2-.02.2.2 0 0 1-.1-.18l.03-.72-.13-.1-.7.16a.2.2 0 0 1-.25-.25l.17-.7-.1-.13-.73.03a.2.2 0 0 1-.2-.3l.3-.66-.08-.14-.71-.12a.2.2 0 0 1-.14-.32l.43-.59-.05-.16-.68-.25A.2.2 0 0 1 1 9.22l.53-.5-.02-.16-.6-.38A.2.2 0 0 1 .8 8a.2.2 0 0 1 .1-.18l.6-.38.02-.16-.53-.5a.2.2 0 0 1 .07-.34l.68-.25.05-.16-.43-.59a.2.2 0 0 1 .14-.32L2.2 5l.08-.15L2 4.2a.2.2 0 0 1 .2-.3l.72.03.1-.13-.16-.7a.2.2 0 0 1 .25-.25l.7.16.13-.1-.03-.72A.2.2 0 0 1 4 2a.2.2 0 0 1 .2-.02l.65.3L5 2.2l.12-.71v-.03a.2.2 0 0 1 .32-.1l.59.41.16-.04.25-.68.01-.02A.2.2 0 0 1 6.8.99l.49.53.16-.02.38-.61.02-.02A.2.2 0 0 1 8 .79M6.8 9.45h1.26l.06.01q.03.01.03.05v1.52q0 .07-.09.06h-4.5A5.4 5.4 0 0 0 8 13.42a5.4 5.4 0 0 0 4.45-2.33h-2.42c-.36 0-.68-.5-.77-.75-.08-.22-.2-.91-.25-1.12-.15-.61-.59-.71-.78-.73H6.8zM8 2.58a5.4 5.4 0 0 0-4.07 1.85h5.74l.17.02c.23.03.6.12.96.35.34.23.83.68.83 1.4 0 .66-.55 1.16-1.08 1.5.42.33.7.53.86 1.44.04.17.34.32.62.29.29-.03.62-.16.62-.75v-.24q0-.1.07-.1h.68A5.43 5.43 0 0 0 8 2.59M2.96 6.03a5.4 5.4 0 0 0-.19 3.37h1.66V6.03zM6.8 7.06h1.66c.35 0 .77-.12.77-.47 0-.42-.55-.53-.65-.53H6.8z" clip-rule="evenodd"/>
</symbol>`,Qe=`<symbol id="file-tree-builtin-sass" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M8.08 1.44c2.41-.91 4.96-.37 5.35 1.27.39 1.62-.92 3.56-2.6 4.25a5 5 0 0 1-3.26.35c-.58-.2-.92-.62-1-.85-.03-.09-.09-.24 0-.3.05-.03.08-.02.22.15s.7.6 1.75.48c2.78-.34 4.45-2.64 3.92-3.88-.37-.87-2.5-1.26-5.18.16C4.03 4.81 3.85 6.24 3.82 6.8c-.08 1.5 1.73 2.28 2.7 3.4q.04.03.07.08c.3-.12.7-.19 1.35-.2 1.58-.03 2.47 1.08 2.43 2.08-.03.78-.7 1.1-.82 1.13-.1.01-.14.02-.15-.06q-.03-.06.13-.15c.16-.09.42-.3.48-.72.05-.43-.24-1.44-1.76-1.63a3 3 0 0 0-1.33.08c.27.62.32 1.87-.29 2.83-.63 1-1.8 1.61-2.93 1.27-.37-.1-.93-.92-.45-2.05.46-1.07 2.4-2.12 2.66-2.26-.9-.83-3.08-1.95-3.4-3.65-.08-.49.13-1.65 1.46-2.98a12 12 0 0 1 4.11-2.52m-1.88 9.7c-.01.01-.9.47-1.52 1.17-.59.66-.75 1.48-.43 1.69.3.18 1-.04 1.51-.62a3 3 0 0 0 .5-.9q.2-.64.02-1.39z" clip-rule="evenodd"/>
</symbol>`,$e=`<symbol id="file-tree-builtin-stylelint" viewBox="0 0 16 16">
  <path fill="currentColor" d="M4 3v3.5l1.5-1L7 15 .5 6l1-1.5L0 3l2.5-2h1zm12 0-1.5 1.5 1 1.5L9 15l1.5-9.5 1.5 1V3l.5-2h1zm-8 8.5a.5.5 0 1 1 0 1 .5.5 0 0 1 0-1m0-3a.5.5 0 1 1 0 1 .5.5 0 0 1 0-1m0-3a.5.5 0 1 1 0 1 .5.5 0 0 1 0-1"/>
  <path fill="currentColor" d="M6.5 2.5V4l-2 1.5v-4zm5 3L9.5 4V2.5l2-1zM9 4H7V2.5h2z"/>
</symbol>`,et=`<symbol id="file-tree-builtin-svelte" viewBox="0 0 16 16">
  <path fill="currentColor" d="m3.98 3.7 3.36-2.08a4.5 4.5 0 0 1 5.9 1.23 4 4 0 0 1 .7 3.02q-.16.75-.58 1.4c.42.77.56 1.66.4 2.52a3.7 3.7 0 0 1-1.57 2.4l-.17.1-3.36 2.09a4.5 4.5 0 0 1-5.9-1.23 4 4 0 0 1-.66-1.44 4 4 0 0 1-.04-1.58 4 4 0 0 1 .58-1.4 4 4 0 0 1-.4-2.52 3.7 3.7 0 0 1 1.57-2.4zl3.36-2.08zm7.87 0a2.7 2.7 0 0 0-1.26-.95 2.7 2.7 0 0 0-1.6-.07 3 3 0 0 0-.52.2l-.16.09-3.36 2.08a2 2 0 0 0-.69.64 2 2 0 0 0-.36.86 2.3 2.3 0 0 0 .42 1.81A2.7 2.7 0 0 0 7.18 9.4q.28-.06.53-.2l.16-.09 1.28-.79.2-.09a.8.8 0 0 1 .87.31.7.7 0 0 1 .13.55.7.7 0 0 1-.24.4l-.08.05-3.36 2.08-.2.09a1 1 0 0 1-.49-.02 1 1 0 0 1-.38-.3 1 1 0 0 1-.13-.37v-.1l.01-.13-.13-.03a4 4 0 0 1-1.1-.5l-.2-.14-.18-.12-.07.18-.08.3a2.3 2.3 0 0 0 .43 1.82q.45.64 1.19.93.73.28 1.51.14l.16-.04q.27-.07.52-.2l.16-.09 3.36-2.08q.4-.25.69-.64.27-.4.36-.86a2.3 2.3 0 0 0-.42-1.82 2.7 2.7 0 0 0-1.27-.95 2.7 2.7 0 0 0-1.6-.08q-.27.07-.52.2l-.16.1-1.28.79-.2.09a1 1 0 0 1-.49-.03 1 1 0 0 1-.38-.29.7.7 0 0 1-.13-.54.7.7 0 0 1 .24-.4l.08-.06L9.33 4.4l.2-.1a.8.8 0 0 1 .87.32 1 1 0 0 1 .13.38v.22l.11.04q.6.18 1.12.5l.2.14.17.12.06-.19.08-.3a2.3 2.3 0 0 0-.42-1.81z"/>
</symbol>`,tt=`<symbol id="file-tree-builtin-svg" viewBox="0 0 16 16">
  <path fill="currentColor" d="M5 7a2 2 0 0 1 2-2h6a2 2 0 0 1 2 2v6a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2z"/>
  <path fill="currentColor" d="M6 1a5 5 0 0 1 4.58 3H7a3 3 0 0 0-3 3v3.58A5 5 0 0 1 6 1" opacity=".5"/>
</symbol>`,nt=`<symbol id="file-tree-builtin-svgo" viewBox="0 0 16 16">
  <path fill="currentColor" d="M9.43 4.8A.6.6 0 1 1 9.19 6l-.56.96a1.2 1.2 0 0 1 .32 1.58l.7.53a.89.89 0 1 1-.17.22l-.7-.52a1.2 1.2 0 0 1-1.4.25l-.56.87a.75.75 0 1 1-.57-.2 1 1 0 0 1 .32.05l.56-.87a1.2 1.2 0 0 1-.4-1.24l-1.2-.47a.56.56 0 1 1 .1-.28v.02l1.2.47a1.2 1.2 0 0 1 1.56-.55l.56-.97a.6.6 0 0 1-.15-.64.6.6 0 0 1 .63-.4"/>
  <path fill="currentColor" fill-rule="evenodd" d="M9.17 1q.16.63.27 1.26a6 6 0 0 1 1.61.67q.52-.38 1.08-.71l1.65 1.64q-.32.56-.68 1.05.48.78.72 1.67.6.09 1.18.25v2.32q-.55.15-1.11.24a6 6 0 0 1-.7 1.82q.31.44.59.91l-1.65 1.65-.85-.55a6 6 0 0 1-1.9.83q-.08.47-.2.95H6.84q-.12-.46-.2-.93a6 6 0 0 1-1.96-.81q-.39.27-.8.51l-1.65-1.65q.25-.43.53-.84a6 6 0 0 1-.75-1.9L1 9.16V6.83q.54-.14 1.09-.24a6 6 0 0 1 .77-1.74q-.33-.47-.63-.98l1.65-1.65q.54.32 1.03.68a6 6 0 0 1 1.66-.66q.1-.61.26-1.24zM7.96 3.73a4 4 0 0 0-1.74.36 4.5 4.5 0 0 0-2.3 2.3 4.4 4.4 0 0 0-.1 3.29l.03.06a4.4 4.4 0 0 0 2.4 2.47 4.4 4.4 0 0 0 3.48-.02l.03-.02a4.4 4.4 0 0 0 2.3-2.42l.06-.14a4.4 4.4 0 0 0-.2-3.4 4.4 4.4 0 0 0-2.13-2.07L9.47 4a4 4 0 0 0-1.51-.27" clip-rule="evenodd"/>
</symbol>`,rt=`<symbol id="file-tree-builtin-swift" viewBox="0 0 16 16">
  <path fill="currentColor" d="M9.63 1c6.15 4.35 4.16 9.15 4.16 9.15s1.75 2.05 1.04 3.85c0 0-.72-1.26-1.93-1.26-1.17 0-1.85 1.26-4.2 1.26C3.47 14 1 9.46 1 9.46c4.71 3.22 7.93.94 7.93.94C6.8 9.12 2.29 3 2.29 3c3.93 3.47 5.63 4.39 5.63 4.39-1.01-.87-3.86-5.13-3.86-5.13C6.34 4.66 10.86 8 10.86 8c1.28-3.7-1.23-7-1.23-7"/>
</symbol>`,it=`<symbol id="file-tree-builtin-table" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8 4a3 3 0 0 0 3 3h3v5.5a2.5 2.5 0 0 1-2.5 2.5h-7A2.5 2.5 0 0 1 2 12.5v-9A2.5 2.5 0 0 1 4.5 1H8z" class="bg" opacity=".4"/>
  <path fill="currentColor" d="M11.5 8a.5.5 0 0 1 .5.5v4a.5.5 0 0 1-.5.5h-7a.5.5 0 0 1-.5-.5v-4a.5.5 0 0 1 .5-.5zM5 12h2.5v-1H5zm3.5 0H11v-1H8.5zM5 10h2.5V9H5zm3.5 0H11V9H8.5zm1-9a.5.5 0 0 1 .354.146l4 4A.5.5 0 0 1 14 5.5V6h-3a2 2 0 0 1-2-2V1z" class="fg"/>
</symbol>`,at=`<symbol id="file-tree-builtin-tailwind" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M8 4Q5.2 4 4.5 6.67q1.05-1.34 2.45-1c.53.12.91.5 1.33.9C8.98 7.23 9.77 8 11.5 8q2.8 0 3.5-2.67-1.05 1.34-2.45 1c-.53-.12-.91-.5-1.33-.9C10.52 4.77 9.73 4 8 4M4.5 8Q1.7 8 1 10.67q1.05-1.34 2.45-1c.53.12.91.5 1.33.9C5.48 11.23 6.26 12 8 12q2.8 0 3.5-2.67-1.05 1.34-2.45 1c-.53-.12-.91-.5-1.33-.9C7.02 8.77 6.24 8 4.5 8" clip-rule="evenodd"/>
</symbol>`,ot=`<symbol id="file-tree-builtin-terraform" viewBox="0 0 16 16">
  <path fill="currentColor" d="M1 0v5.05l4.35 2.53V2.53zm9.18 5.34L5.83 2.82v5.05l4.35 2.53zm.47 5.06V5.34L15 2.82v5.05zm-.48 5.6-4.35-2.53V8.42l4.35 2.53z"/>
</symbol>`,st=`<symbol id="file-tree-builtin-text" viewBox="0 0 16 16">
  <path fill="currentColor" fill-rule="evenodd" d="M8 4a3 3 0 0 0 3 3h3v5.5a2.5 2.5 0 0 1-2.5 2.5h-7A2.5 2.5 0 0 1 2 12.5v-9A2.5 2.5 0 0 1 4.5 1H8z" class="bg" clip-rule="evenodd" opacity=".4"/>
  <path fill="currentColor" d="M8.5 11a.5.5 0 0 1 0 1h-3a.5.5 0 0 1 0-1zm2-2a.5.5 0 0 1 0 1h-5a.5.5 0 0 1 0-1zm-1-8a.5.5 0 0 1 .354.146l4 4A.5.5 0 0 1 14 5.5V6h-3a2 2 0 0 1-2-2V1z"/>
</symbol>`,ct=`<symbol id="file-tree-builtin-typescript" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8 1C2.24 1 1 2.24 1 8s1.24 7 7 7 7-1.24 7-7-1.24-7-7-7" class="bg" opacity=".2"/>
  <path fill="currentColor" d="M8.1 9.64h.95c.04.62.28.76 1.28.76s1.2-.14 1.2-.85c0-.66-.2-.85-1.2-1.07-1.79-.38-2.18-.7-2.18-1.86C8.15 5.3 8.54 5 10.31 5c1.67 0 2.04.26 2.07 1.42h-.95c-.02-.43-.23-.53-1.1-.53-1 0-1.22.14-1.22.74 0 .52.22.7 1.24.92 1.76.38 2.15.73 2.15 2 0 1.44-.4 1.75-2.24 1.75-1.8 0-2.18-.3-2.15-1.66m-3 1.57V5.99H3.5v-.9h4.21v.9H6.1v5.22z"/>
</symbol>`,lt=`<symbol id="file-tree-builtin-vite" viewBox="0 0 16 16">
  <path fill="currentColor" d="M8.57 14.87c-.18.26-.55.11-.55-.22v-3.18l-.05-.27-.13-.22-.2-.15-.24-.06H4.29c-.26 0-.4-.32-.26-.55L6.08 7c.3-.46 0-1.1-.5-1.1H1.8c-.25 0-.4-.32-.25-.56l2.65-4.2A.3.3 0 0 1 4.46 1h7.9c.26 0 .4.32.26.55l-2.05 3.23c-.29.46 0 1.1.5 1.1h3.12c.26 0 .4.34.24.57z"/>
</symbol>`,ut=`<symbol id="file-tree-builtin-vscode" viewBox="0 0 16 16">
  <path fill="currentColor" d="m5.11 9.68-2.4 1.84a.6.6 0 0 1-.75-.04l-.77-.7a.6.6 0 0 1 0-.87L3.28 8zm5.52-8.42a.51.51 0 0 1 .87.36V4.8L7.32 8 5.1 6.32z" opacity=".75"/>
  <path fill="currentColor" d="M11.1 14.99h.03zM1.96 4.52a.6.6 0 0 1 .75-.04l8.8 6.71v3.19a.51.51 0 0 1-.88.36L1.19 6.1a.6.6 0 0 1 0-.87z" opacity=".65"/>
  <path fill="currentColor" d="M11.62 14.91a.9.9 0 0 1-1-.17.51.51 0 0 0 .88-.36V1.62a.51.51 0 0 0-.87-.36.9.9 0 0 1 1-.17l2.87 1.39a.9.9 0 0 1 .5.8v9.44a.9.9 0 0 1-.5.8z"/>
</symbol>`,I=`<symbol id="file-tree-builtin-vue" viewBox="0 0 16 16">
  <path fill="currentColor" d="M9.62 2.25 8 5.02 6.38 2.25H1l7 12 7-12z" opacity=".5"/>
  <path fill="currentColor" d="M9.54 2.25 8 4.95l-1.54-2.7H4l4 7 4-7z"/>
</symbol>`,L=`<symbol id="file-tree-builtin-wasm" viewBox="0 0 16 16">
  <path fill="currentColor" d="M13 1a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H3a2 2 0 0 1-2-2V3a2 2 0 0 1 2-2h3a2 2 0 1 0 4 0z" class="subtract" opacity=".2"/>
  <path fill="currentColor" d="M4.64 11.4h.02l.8-3.4h.91l.73 3.45L7.88 8h.96l-1.25 5h-.97L5.9 9.6 5.1 13h-1L3 8h.98z"/>
  <path fill="currentColor" fill-rule="evenodd" d="M13 13h-1.02l-.33-1.11H9.9L9.64 13h-.97l1.26-5h1.54zm-2.49-3.77-.42 1.84h1.32l-.49-1.84z" clip-rule="evenodd"/>
</symbol>`,dt=`<symbol id="file-tree-builtin-webpack" viewBox="0 0 16 16">
  <path fill="currentColor" d="M14.1 11.79 8.26 15v-2.5l3.64-1.94zm.4-.35V4.73l-2.14 1.2v4.3zm-12.6.35L7.74 15v-2.5L4.1 10.56zm-.4-.35V4.73l2.14 1.2v4.3zm.25-7.15 6-3.29v2.42L3.9 5.47l-.03.01zm12.5 0L8.25 1v2.42l3.85 2.05.03.01z" class="bg" opacity=".4"/>
  <path fill="currentColor" d="m7.74 11.93-3.59-1.92v-3.8l3.6 2.02zm.52 0 3.59-1.92v-3.8l-3.6 2.02zM4.4 5.77 8 3.85l3.6 1.93L8 7.8z" class="fg"/>
</symbol>`,ft=`<symbol id="file-tree-builtin-yml" viewBox="0 0 16 16">
  <path fill="currentColor" d="M7.5 2A1.5 1.5 0 0 1 9 3.5v3A1.5 1.5 0 0 1 7.5 8h-2v2A1.5 1.5 0 0 0 7 11.5v-1A1.5 1.5 0 0 1 8.5 9h5a1.5 1.5 0 0 1 1.5 1.5v3a1.5 1.5 0 0 1-1.5 1.5h-5A1.5 1.5 0 0 1 7 13.5v-1A2.5 2.5 0 0 1 4.5 10V8h-2A1.5 1.5 0 0 1 1 6.5v-3A1.5 1.5 0 0 1 2.5 2zm1 8a.5.5 0 0 0-.5.5v3a.5.5 0 0 0 .5.5h5a.5.5 0 0 0 .5-.5v-3a.5.5 0 0 0-.5-.5zm-6-7a.5.5 0 0 0-.5.5v3a.5.5 0 0 0 .5.5h5a.5.5 0 0 0 .5-.5v-3a.5.5 0 0 0-.5-.5z"/>
</symbol>`,pt=`<symbol id="file-tree-builtin-zig" viewBox="0 0 16 16">
  <path fill="currentColor" d="m14.73 1.5-7.29 8.82h4.17l-1.73 2.04H5.76L1.27 14.5l7.3-8.91H4.39l1.73-2.05h4.12z"/>
  <path fill="currentColor" d="M5.21 3.54 3.56 5.6h-.55v4.73h.83L2.1 12.36H1V3.54zm9.79 0v8.82h-4.3l1.74-2.04h.55V5.68h-.83l1.74-2.14z"/>
</symbol>`,mt=[N,De,ke,P,Ae,je,Pe,Fe,Ie,Re,ze,Be,Ve,He,Ue,Ye,Xe,Ze,rt,it,st,ct,`<symbol id="file-tree-builtin-zip" viewBox="0 0 16 16">
  <path fill="currentColor" d="M4.585 2a2 2 0 0 1 1.028.285l1.788 1.072a1 1 0 0 0 .514.143H12A2 2 0 0 1 13.935 5H0V4a2 2 0 0 1 2-2z" class="bg" opacity=".5"/>
  <path fill="currentColor" fill-rule="evenodd" d="M14 12a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2v-1.25h1v-1H0V6h14zM9.9 8.25c-.883 0-1.9.5-1.9.5H7v1h1v1s1.017.5 1.9.5c.884 0 1.6-.672 1.6-1.5s-.716-1.5-1.6-1.5M2 9.75v1h1v-1zm2 0v1h1v-1zm2 0v1h1v-1zm-5-1v1h1v-1zm2 0v1h1v-1zm2 0v1h1v-1z" class="fg" clip-rule="evenodd"/>
</symbol>`],ht=[xe,Se,Ce,we,Te,Ee,Oe,Me,Ne,Le,We,Ge,Ke,qe,Je,F,Qe,$e,et,tt,nt,at,ot,lt,ut,I,L,dt,ft,pt];function gt(e,t){return t.length===0?e:e.replace(`</svg>`,`\n  ${t.join(`
  `)}\n</svg>`)}var _t=gt(be,mt),vt={minimal:be,standard:_t,complete:gt(_t,ht)},yt={".babelrc":`babel`,".babelrc.json":`babel`,".bash_profile":`bash`,".bashrc":`bash`,".browserslistrc":`browserslist`,".dockerignore":`docker`,".eslintignore":`eslint`,".eslintrc":`eslint`,".eslintrc.cjs":`eslint`,".eslintrc.js":`eslint`,".eslintrc.json":`eslint`,".eslintrc.yaml":`eslint`,".eslintrc.yml":`eslint`,".gitattributes":`git`,".gitignore":`git`,".gitkeep":`git`,".gitmodules":`git`,".oxlintrc.json":`oxc`,".postcssrc":`postcss`,".postcssrc.json":`postcss`,".postcssrc.yaml":`postcss`,".postcssrc.yml":`postcss`,".prettierignore":`prettier`,".prettierrc":`prettier`,".prettierrc.cjs":`prettier`,".prettierrc.js":`prettier`,".prettierrc.json":`prettier`,".prettierrc.mjs":`prettier`,".prettierrc.toml":`prettier`,".prettierrc.yaml":`prettier`,".prettierrc.yml":`prettier`,".stylelintignore":`stylelint`,".stylelintrc":`stylelint`,".stylelintrc.cjs":`stylelint`,".stylelintrc.js":`stylelint`,".stylelintrc.json":`stylelint`,".stylelintrc.mjs":`stylelint`,".stylelintrc.yaml":`stylelint`,".stylelintrc.yml":`stylelint`,".terraform.lock.hcl":`terraform`,".zprofile":`bash`,".zshenv":`bash`,".zshrc":`bash`,"babel.config.cjs":`babel`,"babel.config.js":`babel`,"babel.config.json":`babel`,"babel.config.mjs":`babel`,"biome.json":`biome`,"biome.jsonc":`biome`,"bootstrap.bundle.js":`bootstrap`,"bootstrap.bundle.min.js":`bootstrap`,"bootstrap.css":`bootstrap`,"bootstrap.js":`bootstrap`,"bootstrap.min.css":`bootstrap`,"bootstrap.min.js":`bootstrap`,"bun.lock":`bun`,"bun.lockb":`bun`,"bunfig.toml":`bun`,"claude.md":`claude`,"compose.yaml":`docker`,"compose.yml":`docker`,"docker-compose.override.yml":`docker`,"docker-compose.yaml":`docker`,"docker-compose.yml":`docker`,dockerfile:`docker`,"eslint.config.cjs":`eslint`,"eslint.config.js":`eslint`,"eslint.config.mjs":`eslint`,"eslint.config.mts":`eslint`,"eslint.config.ts":`eslint`,gemfile:`ruby`,"next.config.js":`nextjs`,"next.config.mjs":`nextjs`,"next.config.mts":`nextjs`,"next.config.ts":`nextjs`,"postcss.config.cjs":`postcss`,"postcss.config.js":`postcss`,"postcss.config.mjs":`postcss`,"postcss.config.ts":`postcss`,"prettier.config.cjs":`prettier`,"prettier.config.js":`prettier`,"prettier.config.mjs":`prettier`,rakefile:`ruby`,"readme.md":`markdown`,"stylelint.config.cjs":`stylelint`,"stylelint.config.js":`stylelint`,"stylelint.config.mjs":`stylelint`,"svgo.config.cjs":`svgo`,"svgo.config.js":`svgo`,"svgo.config.mjs":`svgo`,"svgo.config.ts":`svgo`,"tailwind.config.cjs":`tailwind`,"tailwind.config.js":`tailwind`,"tailwind.config.mjs":`tailwind`,"tailwind.config.ts":`tailwind`,"vite.config.js":`vite`,"vite.config.mjs":`vite`,"vite.config.mts":`vite`,"vite.config.ts":`vite`,"webpack.config.babel.js":`webpack`,"webpack.config.cjs":`webpack`,"webpack.config.js":`webpack`,"webpack.config.mjs":`webpack`,"webpack.config.ts":`webpack`},R={"7z":`zip`,astro:`astro`,AUTHORS:`text`,avif:`image`,bash:`bash`,bmp:`image`,bz2:`zip`,c:`c`,cc:`cpp`,cfg:`text`,CHANGELOG:`text`,cjs:`javascript`,"code-workspace":`vscode`,conf:`text`,CONTRIBUTORS:`text`,cpp:`cpp`,csh:`bash`,css:`css`,csv:`table`,cts:`typescript`,cxx:`cpp`,db:`database`,editorconfig:`text`,env:`text`,"env.development":`text`,"env.local":`text`,"env.production":`text`,eot:`font`,erb:`ruby`,fish:`bash`,gemspec:`ruby`,gif:`image`,go:`go`,gql:`graphql`,graphql:`graphql`,gz:`zip`,h:`c`,hh:`cpp`,hpp:`cpp`,htm:`html`,html:`html`,hxx:`cpp`,icns:`image`,ico:`image`,ini:`text`,inl:`cpp`,jar:`zip`,jpeg:`image`,jpg:`image`,js:`javascript`,json:`json`,json5:`json`,jsonc:`json`,jsonl:`json`,jsx:`javascript`,ksh:`bash`,less:`css`,LICENSE:`text`,log:`text`,markdown:`markdown`,mcp:`mcp`,md:`markdown`,mdx:`markdown`,"mdx.tsx":`markdown`,mjs:`javascript`,mm:`cpp`,mts:`typescript`,ods:`table`,otf:`font`,png:`image`,postcss:`css`,py:`python`,pyi:`python`,pyw:`python`,pyx:`python`,rake:`ruby`,rar:`zip`,rb:`ruby`,rs:`rust`,rst:`text`,rtf:`text`,sass:`css`,scss:`css`,sh:`bash`,sql:`database`,sqlite:`database`,sqlite3:`database`,styl:`css`,svelte:`svelte`,svg:`svg`,swift:`swift`,tar:`zip`,tf:`terraform`,tfstate:`terraform`,tfvars:`terraform`,tgz:`zip`,tif:`image`,tiff:`image`,ts:`typescript`,tsv:`table`,tsx:`typescript`,ttf:`font`,txt:`text`,vue:`vue`,war:`zip`,wasm:`wasm`,wast:`wasm`,wat:`wasm`,webp:`image`,woff:`font`,woff2:`font`,xhtml:`html`,xls:`table`,xlsx:`table`,xz:`zip`,yaml:`yml`,yml:`yml`,zig:`zig`,zip:`zip`,zsh:`bash`},bt={jsx:`react`,sass:`sass`,scss:`sass`,tsx:`react`},z=new Set([`bash`,`c`,`cpp`,`css`,`database`,`default`,`font`,`git`,`go`,`html`,`image`,`javascript`,`json`,`markdown`,`mcp`,`python`,`ruby`,`rust`,`swift`,`table`,`text`,`typescript`,`zip`]),xt=new Set([`complete`]);function St(e){return vt[e===`none`?`minimal`:e]}function Ct(e){return`file-tree-builtin-${e}`}function wt(e){return e!==`none`&&xt.has(e)}function Tt(e,t,n){if(e===`minimal`||e===`none`)return;let r=e===`complete`,i=yt[t.toLowerCase()];if(i!=null&&(r||z.has(i)))return i;for(let e of n){if(r){let t=bt[e];if(t!=null)return t}let t=R[e];if(t!=null&&(r||z.has(t)))return t}return`default`}function Et(e){return e.spriteSheet!=null||e.remap!=null||e.byFileName!=null||e.byFileExtension!=null||e.byFileNameContains!=null}function Dt(e){return e==null?{set:`complete`,colored:!0}:typeof e==`string`?{set:e,colored:!0}:{...e,set:e.set??(Et(e)?`none`:`complete`),colored:e.colored??!0}}var Ot=e=>e.trim().toLowerCase(),kt=e=>e.split(`/`).at(-1)??e,At=e=>{let t=e.toLowerCase().split(`.`),n=[];for(let e=1;e<t.length;e+=1)n.push(t.slice(e).join(`.`));return n};function jt(e,t){return typeof e==`string`?{name:e,remappedFrom:t}:{...e,remappedFrom:t}}function Mt(e){let t=Dt(e),n=t.remap,r=new Map;for(let[e,n]of Object.entries(t.byFileName??{}))r.set(e.toLowerCase(),n);let i=new Map;for(let[e,n]of Object.entries(t.byFileExtension??{}))i.set(Ot(e),n);let a=Object.entries(t.byFileNameContains??{}).map(([e,t])=>[e.toLowerCase(),t]);return{resolveIcon:(e,o)=>{if(e===`file-tree-icon-file`&&o!=null){let n=kt(o),s=n.toLowerCase(),c=r.get(s);if(c!=null)return jt(c,e);for(let[t,n]of a)if(s.includes(t))return jt(n,e);let l=At(n);for(let t of l){let n=i.get(t);if(n!=null)return jt(n,e)}let u=Tt(t.set,n,l);if(u!=null&&t.set!==`none`)return{name:Ct(u),remappedFrom:e,token:u}}let s=n?.[e];return s==null?{name:e}:jt(s,e)}}}var Nt=5,Pt=32,Ft=128;function It(){return{childIdByNameId:new Map,childIds:[],childPositionById:new Map,childVisibleChunkSums:null,totalChildSubtreeNodeCount:0,totalChildVisibleSubtreeCount:0}}function Lt(){return{childIdByNameId:null,childIds:[],childPositionById:null,childVisibleChunkSums:null,totalChildSubtreeNodeCount:0,totalChildVisibleSubtreeCount:0}}function Rt(e,t){if(t.childIdByNameId!=null)return t.childIdByNameId;let n=new Map;for(let r of t.childIds){let t=e[r];t!=null&&n.set(t.nameId,r)}return t.childIdByNameId=n,n}function zt(e){if(e.childPositionById!=null)return e.childPositionById;let t=new Map;for(let n=0;n<e.childIds.length;n++){let r=e.childIds[n];r!=null&&t.set(r,n)}return e.childPositionById=t,t}function Bt(e,t){e.childPositionById!=null&&e.childPositionById.set(t,e.childIds.length),e.childIds.push(t)}function Vt(e,t){if(e.childPositionById!=null)for(let n=t;n<e.childIds.length;n++){let t=e.childIds[n];t!=null&&e.childPositionById.set(t,n)}}function Ht(e,t){let n=0,r=0;for(let i of t.childIds){let t=e[i];t!=null&&(n+=t.subtreeNodeCount,r+=t.visibleSubtreeCount)}t.totalChildSubtreeNodeCount=n,t.totalChildVisibleSubtreeCount=r,Kt(e,t)}function Ut(e,t,n,r){if(e.totalChildSubtreeNodeCount+=n,e.totalChildVisibleSubtreeCount+=r,e.childVisibleChunkSums==null||r===0)return;let i=zt(e).get(t);if(i===void 0)return;let a=i>>Nt;e.childVisibleChunkSums[a]+=r}function Wt(e,t,n){let r=t.childVisibleChunkSums;if(r!=null){let i=n,a=0;for(let o of r){if(i<o){let r=qt(e,t,a,i);return{...r,childVisibleIndex:n-r.localVisibleIndex}}i-=o,a+=Pt}throw Error(`Visible child index ${String(n)} is out of range`)}let i=n;for(let r=0;r<t.childIds.length;r++){let a=t.childIds[r];if(a==null)continue;let o=e[a];if(o!=null){if(i<o.visibleSubtreeCount)return{childIndex:r,childVisibleIndex:n-i,localVisibleIndex:i};i-=o.visibleSubtreeCount}}throw Error(`Visible child index ${String(n)} is out of range`)}function Gt(e,t,n){let r=0,i=t.childVisibleChunkSums,a=0;if(i!=null){let e=n>>Nt;for(let t=0;t<e;t+=1)r+=i[t]??0;a=e<<Nt}for(let i=a;i<n;i+=1){let n=t.childIds[i];if(n==null)continue;let a=e[n];a!=null&&(r+=a.visibleSubtreeCount)}return r}function Kt(e,t){if(t.childIds.length<Ft){t.childVisibleChunkSums=null;return}let n=Math.ceil(t.childIds.length/Pt),r=new Int32Array(n);for(let n=0;n<t.childIds.length;n++){let i=t.childIds[n];if(i==null)continue;let a=e[i];a!=null&&(r[n>>Nt]+=a.visibleSubtreeCount)}t.childVisibleChunkSums=r}function qt(e,t,n,r){let i=Math.min(t.childIds.length,n+Pt),a=r;for(let r=n;r<i;r++){let n=t.childIds[r];if(n==null)continue;let i=e[n];if(i!=null){if(a<i.visibleSubtreeCount)return{childIndex:r,localVisibleIndex:a};a-=i.visibleSubtreeCount}}throw Error(`Visible child index ${String(r)} is out of range`)}var Jt=7,Yt=3,Xt=8,Zt=4;function Qt(e,t,n=0){return e<<Zt|n<<Yt|t}function $t(e){return e.depthAndFlags>>>Zt}function en(e){return(e.depthAndFlags&Xt)>>Yt}function B(e){return(e.depthAndFlags&Xt)!==0}function tn(e){return e.depthAndFlags&Jt}function V(e,t){return(tn(e)&t)!==0}function nn(e,t){e.depthAndFlags|=t}function rn(e,t){e.depthAndFlags=Qt(t,tn(e),en(e))}var an=Symbol(`benchmarkInstrumentation`);function on(e,t){return t==null||Object.defineProperty(e,an,{configurable:!0,enumerable:!1,value:t,writable:!1}),e}function sn(e){return e==null?null:e[an]??null}function H(e,t,n){return e==null?n():e.measurePhase(t,n)}function cn(e,t,n){!Number.isFinite(n)||e==null||e.setCounter(t,n)}function ln(e){return e>=48&&e<=57}function un(e){let t=[],n=0,r=0;for(;r<e.length;){for(;r<e.length&&!ln(e.charCodeAt(r));)r+=1;if(r>=e.length)break;r>n&&t.push(e.slice(n,r));let i=0;for(;r<e.length&&ln(e.charCodeAt(r));)i=i*10+(e.charCodeAt(r)-48),r+=1;t.push(i),n=r}return(n<e.length||t.length===0)&&t.push(e.slice(n)),t}function dn(e){let t=e.toLowerCase();return{lowerValue:t,tokens:un(t)}}function fn(e,t){let n=Math.min(e.length,t.length);for(let r=0;r<n;r++){let n=e[r],i=t[r];if(n===i)continue;if(typeof n==`number`&&typeof i==`number`)return n<i?-1:1;let a=String(n),o=String(i);if(a!==o)return a<o?-1:1}return e.length===t.length?0:e.length<t.length?-1:1}function pn(e,t){if(e.tokens.length===1&&t.tokens.length===1&&typeof e.tokens[0]==`string`&&typeof t.tokens[0]==`string`)return e.lowerValue===t.lowerValue?0:e.lowerValue<t.lowerValue?-1:1;let n=fn(e.tokens,t.tokens);return n===0?e.lowerValue===t.lowerValue?0:e.lowerValue<t.lowerValue?-1:1:n}function mn(e,t,n){let r=pn(n(e),n(t));return r===0?e===t?0:e<t?-1:1:r}function hn(e,t){return mn(e,t,dn)}function gn(e,t){return t===e.segments.length-1?+!!e.isDirectory:1}function _n(e,t){let n=Math.min(e.segments.length,t.segments.length);for(let r=0;r<n;r++){let n=e.segments[r],i=t.segments[r];if(n===i)continue;let a=gn(e,r);return a===gn(t,r)?hn(n,i):a===1?-1:1}return e.segments.length===t.segments.length?e.isDirectory===t.isDirectory?0:e.isDirectory?-1:1:e.segments.length<t.segments.length?-1:1}function vn(e,t){return _n(e,t)}function yn(e,t,n){let r=e=>{let t=n.get(e);if(t!=null)return t;let r=dn(e);return n.set(e,r),r},i=Math.min(e.segments.length,t.segments.length);for(let n=0;n<i;n++){let i=e.segments[n],a=t.segments[n];if(i===a)continue;let o=gn(e,n);return o===gn(t,n)?mn(i,a,r):o===1?-1:1}return e.segments.length===t.segments.length?e.isDirectory===t.isDirectory?0:e.isDirectory?-1:1:e.segments.length<t.segments.length?-1:1}function bn(e,t){let n=e.sortKeyById[t];if(n!==void 0)return n;let r=e.valueById[t],i=dn(r);return e.sortKeyById[t]=i,i}function xn(e={}){return{flattenEmptyDirectories:e.flattenEmptyDirectories!==!1,sort:e.sort??`default`}}function Sn(e){let t=e.length>0&&e.charCodeAt(e.length-1)===47,n=t?e.length-1:e.length,r=[],i=0;for(let t=0;t<n;t++)e.charCodeAt(t)===47&&(r.push(e.slice(i,t)),i=t+1);return r.push(e.slice(i,n)),{hasTrailingSlash:t,segments:r}}function Cn(e){let{hasTrailingSlash:t,segments:n}=Sn(e);return{basename:n[n.length-1]??``,isDirectory:t,path:e,segments:n}}function wn(e){if(e.length===0)return{requiresDirectory:!1,segments:[]};let{hasTrailingSlash:t,segments:n}=Sn(e);return{requiresDirectory:t,segments:n}}var Tn=``;function En(){let e=new Map;return e.set(Tn,0),{idByValue:e,valueById:[Tn],sortKeyById:[dn(Tn)]}}function Dn(e,t){let n=e.idByValue.get(t);if(n!==void 0)return n;let r=e.valueById.length;return e.idByValue.set(t,r),e.valueById.push(t),r}function On(e,t){let n=e.valueById[t];if(n===void 0)throw Error(`Unknown segment ID: ${String(t)}`);return n}var kn=Symbol(`pathStorePreparedInputKind`);function An(e,t){return e[kn]=t,e}function jn(e){return{basename:e.basename,depth:e.segments.length,isDirectory:e.isDirectory,path:e.path,segments:e.segments}}function Mn(e,t,n){return n==="default"?vn(e,t):n(jn(e),jn(t))}function Nn(){return{depthAndFlags:Qt(0,3,1),nameId:0,parentId:0,subtreeNodeCount:1,visibleSubtreeCount:1}}function Pn(e,t){let n=Math.min(e.length,t.length);for(let r=0;r<n;r++)if(e[r]!==t[r])return r;return n}function Fn(e){return e.isDirectory?e.segments.length:e.segments.length-1}function In(e){return Array.isArray(e)&&e.every(e=>typeof e==`object`&&!!e&&typeof e.path==`string`&&Array.isArray(e.segments)&&typeof e.basename==`string`&&typeof e.isDirectory==`boolean`)}function Ln(e){return Array.isArray(e)&&e.every(e=>typeof e==`string`)}function Rn(e,t={}){return Wn(e,t).map(e=>e.path)}function zn(e,t={}){let n=Wn(e,t);return An({paths:n.map(e=>e.path),preparedPaths:n},`prepared`)}function Bn(e){let t=e.length,n=!1;for(let r=0;r<t;r+=1){let t=e[r];if(t.length>0&&t.charCodeAt(t.length-1)===47){n=!0;break}}return An({paths:e,presortedPaths:e,presortedPathsContainDirectories:n},`presorted`)}function Vn(e){let t=e,n=t.preparedPaths;if(t[kn]===`prepared`&&n!=null)return n;if(!In(n))throw Error(`preparedInput must come from PathStore.prepareInput()`);return n}function Hn(e){let t=e;return t[kn]===`presorted`&&t.presortedPaths!=null||Ln(t.presortedPaths)?t.presortedPaths:null}function Un(e){let t=e;return typeof t.presortedPathsContainDirectories==`boolean`?t.presortedPathsContainDirectories:null}function Wn(e,t={}){let n=xn(t),r=sn(t);cn(r,`workload.inputFiles`,e.length);let i=H(r,`store.preparePathEntries.parse`,()=>e.map(e=>Cn(e)));return H(r,`store.preparePathEntries.sort`,()=>i.sort((e,t)=>Mn(e,t,n.sort))),i}var Gn=class{directories=new Map;directoryStack=[0];presortedDirectoryNodeIds=[];initialExpandedPathSet;createdDirectoriesAllExpanded=!1;createdDirectoryCount=0;lastPreparedPath=null;nodes=[Nn()];options;instrumentation;segmentSortKeyCache=new Map;segmentTable=En();hasDeferredDirectoryIndexes=!1;constructor(e={}){this.instrumentation=sn(e),this.options=xn(e);let t=e.initialExpandedPaths??null;if(t==null||t.length===0)this.initialExpandedPathSet=null;else{let e=new Set,n=t.length;for(let r=0;r<n;r+=1){let n=t[r],i=n.length;e.add(i>0&&n.charCodeAt(i-1)===47?n.slice(0,i-1):n)}this.initialExpandedPathSet=e,this.createdDirectoriesAllExpanded=!0}this.directories.set(0,It())}appendPaths(e){return H(this.instrumentation,`store.builder.appendPaths.parse`,()=>this.appendPreparedPaths(e.map(e=>Cn(e))))}appendPreparedPaths(e,t=!0){return this.createdDirectoriesAllExpanded=!1,H(this.instrumentation,`store.builder.appendPreparedPaths`,()=>{for(let n of e)this.appendPreparedPath(n,t)}),this}appendPresortedPaths(e,t=null){return H(this.instrumentation,`store.builder.appendPresortedPaths`,()=>{if(t===!1){this.appendPresortedFilePaths(e);return}this.createdDirectoriesAllExpanded=!1;let n=null,r=0,i=this.nodes,a=this.segmentTable,o=a.idByValue,s=a.valueById,c=this.directoryStack,l=0,u=``,d=0;for(let t of e){if(n===t)throw Error(`Duplicate path: "${t}"`);let e=t.length>0&&t.charCodeAt(t.length-1)===47,a=e?t.length-1:t.length,f=0,p=0;if(n!=null){if(u.length>0&&t.length>u.length&&t.startsWith(u))f=d,p=u.length;else{let r=Math.min(a,n.length),i=!0;for(let e=0;e<r;e++){let r=t.charCodeAt(e);if(r!==n.charCodeAt(e)){i=!1;break}r===47&&(f++,p=e+1)}i&&e&&r===a&&n.length>a&&n.charCodeAt(a)===47&&(f++,p=a+1)}}l=f,r=f;let m=p,h=t.indexOf(`/`,m);for(;h>=0&&h<a;){let e=c[l];if(e===void 0)throw Error(`Directory stack underflow while building the path store`);r++;let n=t.slice(m,h),a=o.get(n);a===void 0&&(a=s.length,o.set(n,a),s.push(n));let u=i.length;i.push({depthAndFlags:Qt(r,0,1),nameId:a,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),this.recordCreatedDirectoryPath(t.slice(0,h)),l++,c[l]=u,m=h+1,h=t.indexOf(`/`,m)}if(e){if(m<a){let e=c[l];if(e===void 0)throw Error(`Unable to resolve directory parent for "${t}"`);r++;let n=t.slice(m,a),u=o.get(n);u===void 0&&(u=s.length,o.set(n,u),s.push(n));let d=i.length;i.push({depthAndFlags:Qt(r,0,1),nameId:u,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),l++,c[l]=d}let e=c[l];if(e===void 0)throw Error(`Unable to resolve directory node for "${t}"`);this.promoteDirectoryToExplicit(e,t)}else{let e=c[l];if(e===void 0)throw Error(`Unable to resolve file parent for "${t}"`);let n=t.slice(m),a=o.get(n);a===void 0&&(a=s.length,o.set(n,a),s.push(n)),i.push({depthAndFlags:Qt(r+1,0),nameId:a,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1})}m!==u.length&&(u=t.substring(0,m),d=r),n=t}c.length=l+1,n!=null&&(this.lastPreparedPath=Cn(n)),this.hasDeferredDirectoryIndexes=!0}),this}appendPresortedFilePaths(e){let t=null,n=0,r=this.nodes,i=this.segmentTable,a=i.idByValue,o=i.valueById,s=this.directoryStack,c=0,l=``,u=0;for(let i of e){if(t===i)throw Error(`Duplicate path: "${i}"`);let e=i.length,d=0,f=0;if(t!=null){if(l.length>0&&i.length>l.length&&i.startsWith(l))d=u,f=l.length;else{let n=Math.min(e,t.length);for(let e=0;e<n;e++){let n=i.charCodeAt(e);if(n!==t.charCodeAt(e))break;n===47&&(d++,f=e+1)}}}c=d,n=d;let p=f,m=i.indexOf(`/`,p);for(;m>=0;){let e=s[c];if(e===void 0)throw Error(`Directory stack underflow while building the path store`);n++;let t=i.slice(p,m),l=a.get(t);l===void 0&&(l=o.length,a.set(t,l),o.push(t));let u=r.length;r.push({depthAndFlags:Qt(n,0,1),nameId:l,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),this.recordCreatedDirectoryPath(i.slice(0,m)),this.presortedDirectoryNodeIds.push(u),c++,s[c]=u,p=m+1,m=i.indexOf(`/`,p)}let h=s[c];if(h===void 0)throw Error(`Unable to resolve file parent for "${i}"`);let g=i.slice(p),_=a.get(g);_===void 0&&(_=o.length,a.set(g,_),o.push(g)),r.push({depthAndFlags:Qt(n+1,0),nameId:_,parentId:h,subtreeNodeCount:1,visibleSubtreeCount:1}),p!==l.length&&(l=i.substring(0,p),u=n),t=i}s.length=c+1,t!=null&&(this.lastPreparedPath=Cn(t)),this.hasDeferredDirectoryIndexes=!0}finish(e={}){let t=e.skipSubtreeCountPass===!0;return this.hasDeferredDirectoryIndexes?(H(this.instrumentation,`store.builder.buildDirectoryIndexes`,()=>this.buildPresortedFinish(t)),this.hasDeferredDirectoryIndexes=!1):t||H(this.instrumentation,`store.builder.computeSubtreeCounts`,()=>this.computeSubtreeCounts(0)),{directories:this.directories,nodes:this.nodes,options:this.options,rootId:0,segmentTable:this.segmentTable,presortedDirectoryNodeIds:this.presortedDirectoryNodeIds.length>0?this.presortedDirectoryNodeIds:null}}didMatchAllInitialExpandedPaths(){return this.createdDirectoriesAllExpanded&&this.initialExpandedPathSet!=null&&this.createdDirectoryCount===this.initialExpandedPathSet.size}appendPreparedPath(e,t){if(this.hasDeferredDirectoryIndexes&&=(this.buildDirectoryIndexes(),!1),this.lastPreparedPath!=null){if(e.path===this.lastPreparedPath.path)throw Error(`Duplicate path: "${e.path}"`);if(t&&(this.options.sort==="default"?yn(this.lastPreparedPath,e,this.segmentSortKeyCache):Mn(this.lastPreparedPath,e,this.options.sort))>0)throw Error(`Builder input must be sorted before appendPaths(): "${e.path}"`)}let n=this.lastPreparedPath,r=Fn(e),i=n==null?0:Fn(n),a=n==null?0:Pn(n.segments,e.segments),o=Math.min(a,r,i);this.directoryStack.length=o+1;for(let n=o;n<r;n++){let r=this.directoryStack[this.directoryStack.length-1];if(r===void 0)throw Error(`Directory stack underflow while building the path store`);let i=t?this.getOrCreateDirectoryChild(r,e.segments[n]):this.createDirectoryChild(r,e.segments[n]);this.directoryStack.push(i)}if(e.isDirectory){let t=this.directoryStack[this.directoryStack.length-1];if(t===void 0)throw Error(`Unable to resolve directory node for "${e.path}"`);this.promoteDirectoryToExplicit(t,e.path),this.lastPreparedPath=e;return}let s=this.directoryStack[this.directoryStack.length-1];if(s===void 0)throw Error(`Unable to resolve file parent for "${e.path}"`);t?this.createFileChild(s,e.basename,e.path):this.createFileChildUnchecked(s,e.basename),this.lastPreparedPath=e}recordCreatedDirectoryPath(e){!this.createdDirectoriesAllExpanded||this.initialExpandedPathSet==null||(this.createdDirectoryCount+=1,this.initialExpandedPathSet.has(e)||(this.createdDirectoriesAllExpanded=!1))}createFileChild(e,t,n){let r=Dn(this.segmentTable,t),i=this.getDirectoryIndex(e),a=i.childIdByNameId;if(a!=null&&a.get(r)!==void 0)throw Error(`Path collides with an existing entry: "${n}"`);let o=this.nodes[e];if(o===void 0)throw Error(`Unknown parent node ID: ${String(e)}`);let s=this.nodes.length;return this.nodes.push({depthAndFlags:Qt($t(o)+1,0),nameId:r,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),a?.set(r,s),Bt(i,s),s}createFileChildUnchecked(e,t){let n=Dn(this.segmentTable,t),r=this.getDirectoryIndex(e),i=this.nodes[e];if(i===void 0)throw Error(`Unknown parent node ID: ${String(e)}`);let a=this.nodes.length;return this.nodes.push({depthAndFlags:Qt($t(i)+1,0),nameId:n,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),r.childIdByNameId!=null&&r.childIdByNameId.set(n,a),Bt(r,a),a}getOrCreateDirectoryChild(e,t){let n=Dn(this.segmentTable,t),r=this.getDirectoryIndex(e);if(r.childIdByNameId!=null){let e=r.childIdByNameId.get(n);if(e!==void 0){let n=this.nodes[e];if(n!=null&&!B(n))throw Error(`Path collides with an existing file while creating directory "${t}"`);return e}}let i=this.nodes[e];if(i===void 0)throw Error(`Unknown parent node ID: ${String(e)}`);let a=this.nodes.length;return this.nodes.push({depthAndFlags:Qt($t(i)+1,0,1),nameId:n,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),r.childIdByNameId!=null&&r.childIdByNameId.set(n,a),Bt(r,a),this.directories.set(a,It()),a}createDirectoryChild(e,t){let n=Dn(this.segmentTable,t),r=this.getDirectoryIndex(e),i=this.nodes[e];if(i===void 0)throw Error(`Unknown parent node ID: ${String(e)}`);let a=this.nodes.length;return this.nodes.push({depthAndFlags:Qt($t(i)+1,0,1),nameId:n,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),r.childIdByNameId!=null&&r.childIdByNameId.set(n,a),Bt(r,a),this.directories.set(a,It()),a}promoteDirectoryToExplicit(e,t){let n=this.nodes[e];if(n===void 0)throw Error(`Unknown directory node ID: ${String(e)}`);if(!B(n))throw Error(`Path is not a directory: "${t}"`);if(V(n,1))throw Error(`Duplicate path: "${t}"`);nn(n,1)}getDirectoryIndex(e){let t=this.directories.get(e);if(t!==void 0)return t;throw Error(`Unknown directory child index for node ${String(e)}`)}buildPresortedFinish(e){let t=this.nodes,n=this.directories;n.set(0,Lt());let r=-1,i=null;for(let e=1;e<t.length;e++){let a=t[e];if(a==null)continue;if(B(a)){let t=Lt();n.set(e,t),r=e,i=t}let o;a.parentId===r?o=i:(o=n.get(a.parentId),r=a.parentId,i=o??null),o?.childIds.push(e)}if(!e)for(let e=t.length-1;e>=1;e--){let n=t[e];if(n==null)continue;let r=t[n.parentId];r!=null&&(r.subtreeNodeCount+=n.subtreeNodeCount,r.visibleSubtreeCount+=n.visibleSubtreeCount)}}buildDirectoryIndexes(){let e=this.nodes;for(let t=1;t<e.length;t++){let n=e[t];if(n==null)continue;B(n)&&this.directories.set(t,It());let r=this.directories.get(n.parentId);r!=null&&(r.childIdByNameId!=null&&r.childIdByNameId.set(n.nameId,t),Bt(r,t))}}computeSubtreeCounts(e){let t=this.nodes[e];if(t===void 0)throw Error(`Unknown node ID: ${String(e)}`);if(!B(t))return t.subtreeNodeCount=1,t.visibleSubtreeCount=1,1;let n=this.getDirectoryIndex(e),r=1;for(let e of n.childIds)r+=this.computeSubtreeCounts(e);return Ht(this.nodes,n),t.subtreeNodeCount=r,t.visibleSubtreeCount=r,r}};function Kn(e,t=`closed`,n=null){let r=Jn(t);return{activeNodeCount:e.nodes.length-1,collapsedDirectoryIds:new Set,collapseNewDirectoriesByDefault:!1,defaultExpansion:r,directoriesOpenByDefault:r===`open`,hasCollapsedDirectoryOverrides:!1,directoryLoadInfoById:new Map,expandedDirectoryIds:new Set,instrumentation:n,listeners:new Map,pathCacheByNodeId:new Map([[e.rootId,{path:``,version:0}]]),pathCacheVersion:0,snapshot:e,transactionStack:[]}}function qn(){return{affectedAncestorIds:new Set,affectedNodeIds:new Set,events:[]}}function Jn(e){if(typeof e!=`number`)return e;if(!Number.isInteger(e)||e<0)throw Error(`initialExpansion must be "open", "closed", or a non-negative integer depth. Received: ${String(e)}`);return e}function Yn(e,t){return V(t,2)||e.defaultExpansion===`open`?!0:e.defaultExpansion!==`closed`&&$t(t)<=e.defaultExpansion}function Xn(e,t,n=e.snapshot.nodes[t]){return n==null||!B(n)?!1:e.directoriesOpenByDefault&&!e.hasCollapsedDirectoryOverrides?!0:e.collapsedDirectoryIds.has(t)?!1:e.expandedDirectoryIds.has(t)?!0:Yn(e,n)}function Zn(e,t,n,r=e.snapshot.nodes[t]){if(r==null||!B(r))return;let i=Yn(e,r);if(n){if(i){e.collapsedDirectoryIds.delete(t),e.hasCollapsedDirectoryOverrides=e.collapsedDirectoryIds.size>0;return}e.expandedDirectoryIds.add(t);return}if(i){e.collapsedDirectoryIds.add(t),e.hasCollapsedDirectoryOverrides=!0;return}e.expandedDirectoryIds.delete(t)}function Qn(e,t){let n=e.directoryLoadInfoById.get(t);if(n!=null)return n;let r={activeAttemptId:null,errorMessage:null,nextAttemptId:1,state:`loaded`};return e.directoryLoadInfoById.set(t,r),r}function $n(e,t){return e.directoryLoadInfoById.get(t)?.state??`loaded`}function er(e,t){let n=Qn(e,t);if(n.state===`loading`&&n.activeAttemptId!=null)return{attemptId:n.activeAttemptId,nodeId:t,reused:!0};let r=n.nextAttemptId;return n.activeAttemptId=r,n.errorMessage=null,n.nextAttemptId+=1,n.state=`loading`,{attemptId:r,nodeId:t,reused:!1}}function tr(e,t){let n=Qn(e,t);n.activeAttemptId=null,n.errorMessage=null,n.state=`unloaded`}function nr(e,t,n){let r=e.directoryLoadInfoById.get(t);return r==null||r.activeAttemptId!==n?!1:(r.activeAttemptId=null,r.errorMessage=null,r.state=`loaded`,!0)}function rr(e,t,n){return e.directoryLoadInfoById.get(t)?.activeAttemptId===n}function ir(e,t,n,r){let i=e.directoryLoadInfoById.get(t);return i==null||i.activeAttemptId!==n?!1:(i.activeAttemptId=null,i.errorMessage=r??null,i.state=`error`,!0)}function ar(e,t){e.directoryLoadInfoById.delete(t)}function or(e,t,n){let r=n,i=e.listeners.get(t);return i==null?e.listeners.set(t,new Set([r])):i.add(r),()=>{let n=e.listeners.get(t);n!=null&&(n.delete(r),n.size===0&&e.listeners.delete(t))}}function sr(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!0,operation:`add`,path:e.path,projectionChanged:e.projectionChanged,visibleCountDelta:null}}function cr(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!0,operation:`remove`,path:e.path,projectionChanged:e.projectionChanged,recursive:e.recursive,visibleCountDelta:null}}function lr(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!0,from:e.from,operation:`move`,projectionChanged:e.projectionChanged,to:e.to,visibleCountDelta:null}}function ur(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!1,operation:`expand`,path:e.path,projectionChanged:!0,visibleCountDelta:null}}function dr(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!1,operation:`collapse`,path:e.path,projectionChanged:!0,visibleCountDelta:null}}function fr(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!1,operation:`mark-directory-unloaded`,path:e.path,projectionChanged:e.projectionChanged,visibleCountDelta:null}}function pr(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],attemptId:e.attemptId,canonicalChanged:!1,operation:`begin-child-load`,path:e.path,projectionChanged:e.projectionChanged,reused:e.reused,visibleCountDelta:null}}function mr(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],attemptId:e.attemptId,canonicalChanged:e.childEvents.some(e=>e.canonicalChanged),childEvents:e.childEvents,operation:`apply-child-patch`,path:e.path,projectionChanged:e.projectionChanged,visibleCountDelta:null}}function hr(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],attemptId:e.attemptId,canonicalChanged:!1,operation:`complete-child-load`,path:e.path,projectionChanged:e.projectionChanged,stale:e.stale,visibleCountDelta:null}}function gr(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],attemptId:e.attemptId,canonicalChanged:!1,errorMessage:e.errorMessage,operation:`fail-child-load`,path:e.path,projectionChanged:e.projectionChanged,stale:e.stale,visibleCountDelta:null}}function _r(e){return{activeNodeCountAfter:e.activeNodeCountAfter,activeNodeCountBefore:e.activeNodeCountBefore,affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],cachedPathEntryCountAfter:e.cachedPathEntryCountAfter,cachedPathEntryCountBefore:e.cachedPathEntryCountBefore,canonicalChanged:!1,idsPreserved:e.idsPreserved,loadInfoEntryCountAfter:e.loadInfoEntryCountAfter,loadInfoEntryCountBefore:e.loadInfoEntryCountBefore,mode:e.mode,operation:`cleanup`,projectionChanged:e.projectionChanged,reclaimedCachedPathEntryCount:e.reclaimedCachedPathEntryCount,reclaimedLoadInfoEntryCount:e.reclaimedLoadInfoEntryCount,reclaimedNodeSlotCount:e.reclaimedNodeSlotCount,reclaimedSegmentCount:e.reclaimedSegmentCount,segmentCountAfter:e.segmentCountAfter,segmentCountBefore:e.segmentCountBefore,totalNodeSlotCountAfter:e.totalNodeSlotCountAfter,totalNodeSlotCountBefore:e.totalNodeSlotCountBefore,visibleCountDelta:null}}function vr(e,t,n){return{...n,visibleCountDelta:kr(e)-t}}function yr(e,t){let n=kr(e),r=qn();e.transactionStack.push(r);try{t()}catch(t){throw Sr(e,r,!1),t}Sr(e,r,!0,kr(e)-n)}function br(e,t){let n=e.instrumentation;if(n==null){xr(e,t);return}H(n,`store.events.record`,()=>xr(e,t))}function xr(e,t){let n=e.transactionStack[e.transactionStack.length-1]??null;if(n==null){Dr(e,t);return}n.events.push(t),Er(n,t)}function Sr(e,t,n,r=null){if(e.transactionStack.pop()!==t)throw Error(`Transaction stack underflow`);if(!n)return;let i=e.transactionStack[e.transactionStack.length-1]??null;if(i!=null){let n=e.instrumentation;n==null?Tr(i,t):H(n,`store.events.batch.merge`,()=>Tr(i,t));return}let a=Cr(t,r),o=e.instrumentation;if(o==null){Dr(e,a);return}H(o,`store.events.batch.commit`,()=>Dr(e,a))}function Cr(e,t){return{affectedAncestorIds:[...e.affectedAncestorIds],affectedNodeIds:[...e.affectedNodeIds],canonicalChanged:e.events.some(e=>e.canonicalChanged),events:[...e.events],operation:`batch`,projectionChanged:e.events.some(e=>e.projectionChanged),visibleCountDelta:t}}function wr(e,t){for(let n of t.affectedAncestorIds)e.affectedAncestorIds.add(n);for(let n of t.affectedNodeIds)e.affectedNodeIds.add(n)}function Tr(e,t){for(let n of t.events)e.events.push(n);wr(e,t)}function Er(e,t){for(let n of t.affectedNodeIds)e.affectedNodeIds.add(n);for(let n of t.affectedAncestorIds)e.affectedAncestorIds.add(n)}function Dr(e,t){let n=e.instrumentation;if(n==null){Or(e,t);return}H(n,`store.events.emit`,()=>Or(e,t))}function Or(e,t){e.listeners.get(t.operation)?.forEach(e=>e(t)),e.listeners.get(`*`)?.forEach(e=>e(t))}function kr(e){return e.snapshot.nodes[e.snapshot.rootId]?.visibleSubtreeCount??0}function Ar(e,t){if(e.snapshot.options.flattenEmptyDirectories!==!0)return null;let n=e.snapshot.nodes[t];if(n==null||!B(n)||V(n,2))return null;let r=e.snapshot.directories.get(t);if(r==null||r.childIds.length!==1)return null;let i=r.childIds[0];if(i==null)return null;let a=e.snapshot.nodes[i];return a==null||!B(a)?null:i}function jr(e,t){let n=t;for(;;){let t=Ar(e,n);if(t==null)return n;n=t}}function Mr(e,t){let n=[t],r=t;for(;;){let t=Ar(e,r);if(t==null)return n;n.push(t),r=t}}function Nr(e,t){let n=t==null?e.snapshot.rootId:Hr(e,t);return n==null?[]:Wr(e,n)}function Pr(e,t){let n=Cn(t),r=n.isDirectory?n.segments:n.segments.slice(0,-1),i=ai(e,ii(e,r)),{createdNodeIds:a,directoryId:o}=Gr(e,r),s=new Set(a),c=o;if(n.isDirectory){let n=G(e,o);if(V(n,1))throw Error(`Path already exists: "${t}"`);nn(n,1),e.pathCacheByNodeId.set(o,{path:t,version:e.pathCacheVersion}),s.add(o)}else c=qr(e,o,n.basename),s.add(c);zr(e,o);let l=ai(e,o);return sr({affectedAncestorIds:Vr(e,c),affectedNodeIds:[...s],path:t,projectionChanged:oi(i,l)})}function Fr(e,t,n){let r=Hr(e,t);if(r==null)throw Error(`Path does not exist: "${t}"`);let i=G(e,r);if(V(i,2))throw Error(`The root node cannot be removed`);if(B(i)&&W(e,r).childIds.length>0&&n.recursive!==!0)throw Error(`Cannot remove a non-empty directory without recursive: "${t}"`);let a=i.parentId,o=ai(e,a),s=ni(e,r);Xr(e,a,r,i.nameId),ri(e,a),zr(e,a);let c=ai(e,a);return cr({affectedAncestorIds:Vr(e,a),affectedNodeIds:s,path:t,projectionChanged:oi(o,c),recursive:n.recursive===!0})}function Ir(e,t,n,r){let i=Hr(e,t);if(i==null)throw Error(`Source path does not exist: "${t}"`);let a=G(e,i);if(V(a,2))throw Error(`The root node cannot be moved`);let o=r.collision??`error`,s=ei(e,i,n),c=ai(e,a.parentId),l=ai(e,s.parentId),u=On(e.snapshot.segmentTable,a.nameId),d=Dn(e.snapshot.segmentTable,s.basename);if(s.parentId===a.parentId&&u===s.basename)return null;if(B(a)&&ui(e,i,s.parentId))throw Error(`Cannot move a directory into one of its descendants`);let f=Rt(e.snapshot.nodes,W(e,s.parentId)).get(d),p=s.existingNodeId??f??null;if(p!=null&&p!==i&&ti(e,p,o,en(a))===`skip`)return null;let m=a.parentId;Xr(e,m,i,a.nameId),a.parentId=s.parentId,a.nameId=d,e.pathCacheByNodeId.delete(i),li(e,i),Yr(e,s.parentId,i),ri(e,m),e.pathCacheVersion++,zr(e,m),s.parentId!==m&&zr(e,s.parentId);let h=ai(e,m),g=ai(e,s.parentId);return lr({affectedAncestorIds:[...new Set([...Vr(e,m),...Vr(e,s.parentId)])],affectedNodeIds:[i],from:t,projectionChanged:si([c,l],[h,g]),to:U(e,i)})}function Lr(e,t){let n=e.pathCacheByNodeId.get(t);return n!=null&&n.version===e.pathCacheVersion?n.path:null}function Rr(e,t,n){return e.pathCacheByNodeId.set(t,{path:n,version:e.pathCacheVersion}),n}function U(e,t){let n=G(e,t),r=Lr(e,t);if(r!=null)return r;if(V(n,2))return Rr(e,t,``);let i=U(e,n.parentId),a=On(e.snapshot.segmentTable,n.nameId),o=i.length===0?a:`${i}${a}`;return Rr(e,t,B(n)?`${o}/`:o)}function zr(e,t){let n=e.instrumentation;if(n==null){fi(e,t);return}H(n,`store.recomputeCountsUpwardFrom`,()=>fi(e,t))}function Br(e,t){let n=[[t,0]],{nodes:r,directories:i}=e.snapshot;for(;n.length>0;){let t=n[n.length-1],a=t[0],o=r[a];if(o==null||!B(o)){di(e,a,o,!0),n.pop();continue}let s=i.get(a);if(s==null||t[1]>=s.childIds.length){di(e,a,o,!0),n.pop();continue}let c=s.childIds[t[1]++];n.push([c,0])}}function Vr(e,t){let n=[],r=t;for(;r!=null;){let t=G(e,r);if(n.push(r),r===e.snapshot.rootId)break;r=t.parentId}return n}function Hr(e,t){if(t.length===0)return e.snapshot.rootId;let n=wn(t);return Ur(e,n.segments,n.requiresDirectory)}function Ur(e,t,n){let r=e.snapshot.rootId;for(let n of t){let t=e.snapshot.segmentTable.idByValue.get(n);if(t===void 0)return null;let i=W(e,r),a=Rt(e.snapshot.nodes,i).get(t);if(a===void 0)return null;r=a}let i=G(e,r);return n&&!B(i)?null:r}function W(e,t){let n=e.snapshot.directories.get(t);if(n===void 0)throw Error(`Unknown directory child index for node ${String(t)}`);return n}function G(e,t){let n=e.snapshot.nodes[t];if(n===void 0||V(n,4))throw Error(`Unknown node ID: ${String(t)}`);return n}function Wr(e,t){let n=e.snapshot.nodes[t];if(n===void 0||V(n,4))return[];if(!B(n))return[U(e,t)];if(W(e,t).childIds.length===0)return V(n,1)&&!V(n,2)?[U(e,t)]:[];let r=[],i=[{childIndex:0,nodeId:t}];for(;i.length>0;){let t=i[i.length-1];if(t==null)break;let n=e.snapshot.nodes[t.nodeId];if(n===void 0||V(n,4)){i.pop();continue}if(!B(n)){r.push(U(e,t.nodeId)),i.pop();continue}let a=W(e,t.nodeId);if(a.childIds.length===0){V(n,1)&&!V(n,2)&&r.push(U(e,t.nodeId)),i.pop();continue}let o=a.childIds[t.childIndex];if(o==null){i.pop();continue}t.childIndex++,i.push({childIndex:0,nodeId:o})}return r}function Gr(e,t){let n=[],r=e.snapshot.rootId;for(let i of t){let t=Dn(e.snapshot.segmentTable,i),a=W(e,r),o=Rt(e.snapshot.nodes,a).get(t);if(o!==void 0){if(!B(G(e,o)))throw Error(`Cannot create a directory that collides with an existing file: "${i}"`);r=o;continue}r=Kr(e,r,t),n.push(r)}return{createdNodeIds:n,directoryId:r}}function Kr(e,t,n){let r=G(e,t),i=e.snapshot.nodes.length;return e.snapshot.nodes.push({depthAndFlags:Qt($t(r)+1,0,1),nameId:n,parentId:t,subtreeNodeCount:1,visibleSubtreeCount:1}),e.snapshot.directories.set(i,It()),Yr(e,t,i),e.collapseNewDirectoriesByDefault&&(e.collapsedDirectoryIds.add(i),e.hasCollapsedDirectoryOverrides=!0),e.activeNodeCount++,i}function qr(e,t,n){let r=Dn(e.snapshot.segmentTable,n),i=W(e,t);if(Rt(e.snapshot.nodes,i).has(r))throw Error(`Path already exists: "${mi(e,t,n)}"`);let a=G(e,t),o=e.snapshot.nodes.length;return e.snapshot.nodes.push({depthAndFlags:Qt($t(a)+1,0),nameId:r,parentId:t,subtreeNodeCount:1,visibleSubtreeCount:1}),Yr(e,t,o),e.activeNodeCount++,o}function Jr(e,t,n){let r=0,i=t.childIds.length;for(;r<i;){let a=r+i>>>1,o=t.childIds[a];if(o==null){i=a;continue}Zr(e,n,o)<0?i=a:r=a+1}return r}function Yr(e,t,n){let r=W(e,t),i=G(e,n);Rt(e.snapshot.nodes,r).set(i.nameId,n),Ut(r,n,i.subtreeNodeCount,i.visibleSubtreeCount);let a=Jr(e,r,n);r.childIds.splice(a,0,n),Vt(r,a),Kt(e.snapshot.nodes,r)}function Xr(e,t,n,r){let i=W(e,t),a=zt(i),o=a.get(n)??-1;Rt(e.snapshot.nodes,i).delete(r),a.delete(n);let s=e.snapshot.nodes[n];s!=null&&Ut(i,n,-s.subtreeNodeCount,-s.visibleSubtreeCount),o>=0&&(i.childIds.splice(o,1),Vt(i,o),Kt(e.snapshot.nodes,i))}function Zr(e,t,n){let r=e.snapshot.options.sort;return r==="default"?Qr(e,t,n):r($r(e,t),$r(e,n))}function Qr(e,t,n){let r=G(e,t),i=G(e,n),a=B(r);if(a!==B(i))return a?-1:1;let o=pn(bn(e.snapshot.segmentTable,r.nameId),bn(e.snapshot.segmentTable,i.nameId));if(o!==0)return o;let s=On(e.snapshot.segmentTable,r.nameId),c=On(e.snapshot.segmentTable,i.nameId);return s===c?t<n?-1:1:s<c?-1:1}function $r(e,t){let n=G(e,t),r=U(e,t),i=B(n),a=i?r.slice(0,-1):r;return{basename:On(e.snapshot.segmentTable,n.nameId),depth:$t(n),isDirectory:i,path:r,segments:a.length===0?[]:a.split(`/`)}}function ei(e,t,n){let r=G(e,t),i=Hr(e,n);if(i!=null){let t=G(e,i);if(B(t))return{basename:On(e.snapshot.segmentTable,r.nameId),existingNodeId:null,parentId:i};let a=wn(n).segments;return{basename:a[a.length-1]??``,existingNodeId:i,parentId:t.parentId}}let a=wn(n),o=a.segments[a.segments.length-1]??``,s=a.segments.slice(0,-1),c=s.length===0?e.snapshot.rootId:Ur(e,s,!0);if(c==null)throw Error(`Destination parent does not exist: "${n}"`);return{basename:o,existingNodeId:null,parentId:c}}function ti(e,t,n,r){if(n===`skip`)return`skip`;if(n===`error`)throw Error(`Destination already exists: "${U(e,t)}"`);let i=G(e,t);if(en(i)!==r)throw Error(`replace collision requires the same source and destination kinds`);if(B(i)&&W(e,t).childIds.length>0)throw Error(`replace collision does not support non-empty directories`);let a=i.parentId,o=i.nameId;return ni(e,t),Xr(e,a,t,o),ri(e,a),zr(e,a),`handled`}function ni(e,t){let n=[],r=[{nodeId:t,visitedChildren:!1}];for(;r.length>0;){let t=r.pop();if(t==null)break;let i=G(e,t.nodeId);if(t.visitedChildren||!B(i)){B(i)&&e.snapshot.directories.delete(t.nodeId),nn(i,4),e.pathCacheByNodeId.delete(t.nodeId),e.collapsedDirectoryIds.delete(t.nodeId)&&(e.hasCollapsedDirectoryOverrides=e.collapsedDirectoryIds.size>0),e.expandedDirectoryIds.delete(t.nodeId),ar(e,t.nodeId),e.activeNodeCount--,n.push(t.nodeId);continue}r.push({nodeId:t.nodeId,visitedChildren:!0});let a=W(e,t.nodeId);for(let e=a.childIds.length-1;e>=0;e--){let t=a.childIds[e];t!=null&&r.push({nodeId:t,visitedChildren:!1})}}return n}function ri(e,t){let n=t;for(;n!=null;){let t=G(e,n);if(!B(t)||V(t,2)||W(e,n).childIds.length>0)return;nn(t,1),n=t.parentId===n?null:t.parentId}}function ii(e,t){let n=e.snapshot.rootId;for(let r of t){let t=e.snapshot.segmentTable.idByValue.get(r);if(t==null)break;let i=Rt(e.snapshot.nodes,W(e,n)).get(t);if(i==null||!B(G(e,i)))break;n=i}return n}function ai(e,t){let n=ci(e,t);if(n==null)return null;let r=jr(e,n),i=G(e,r),a=n===r?null:Mr(e,n).map(t=>U(e,t));return JSON.stringify({flattenedSegmentPaths:a,hasChildren:W(e,r).childIds.length>0,path:U(e,r),terminalKind:en(i)})}function oi(e,t){return si([e],[t])}function si(e,t){for(let n=0;n<e.length;n+=1){let r=e[n],i=t[n];if(r==null||i==null||r!==i)return!0}return!1}function ci(e,t){let n=t;for(;n!=null;){let t=G(e,n);if(!B(t)||V(t,2))return null;if(!Xn(e,n,t))return n;n=t.parentId}return null}function li(e,t){let n=G(e,t);if(rn(n,(t===e.snapshot.rootId?-1:$t(G(e,n.parentId)))+1),!B(n))return;let r=W(e,t);for(let t of r.childIds)li(e,t)}function ui(e,t,n){let r=n;for(;r!=null;){if(r===t)return!0;let n=G(e,r);if(r===e.snapshot.rootId)return!1;r=n.parentId}return!1}function di(e,t,n=G(e,t),r=!1){let i=e.instrumentation;if(i==null){pi(e,t,n,r);return}H(i,`store.recomputeNodeCounts`,()=>pi(e,t,n,r))}function fi(e,t){let n=t;for(;n!=null;){let t=G(e,n),r=t.subtreeNodeCount,i=t.visibleSubtreeCount;if(di(e,n,t),n===e.snapshot.rootId)return;let a=t.subtreeNodeCount-r,o=t.visibleSubtreeCount-i,s=t.parentId;(a!==0||o!==0)&&Ut(W(e,s),n,a,o),n=s}}function pi(e,t,n,r){if(!B(n)){n.subtreeNodeCount=1,n.visibleSubtreeCount=1;return}let i=W(e,t);if(r){let t=e.instrumentation;t==null?Ht(e.snapshot.nodes,i):H(t,`store.recomputeNodeCounts.rebuildChildAggregates`,()=>Ht(e.snapshot.nodes,i))}let a=1+i.totalChildSubtreeNodeCount,o=i.totalChildVisibleSubtreeCount;if(n.subtreeNodeCount=a,V(n,2)){n.visibleSubtreeCount=o;return}n.visibleSubtreeCount=Ar(e,t)==null?Xn(e,t,n)?1+o:1:o}function mi(e,t,n){let r=U(e,t);return r.length===0?n:`${r}${n}`}function hi(e){return e!=null&&!V(e,4)}function gi(e,t){let n=e.snapshot.nodes[t];return!hi(n)||!B(n)||V(n,2)?null:n}function _i(e){let t=0;for(let[n,r]of e.pathCacheByNodeId)r.version===e.pathCacheVersion&&hi(e.snapshot.nodes[n])&&(t+=1);return t}function vi(e){return Math.max(0,e.valueById.length-1)}function yi(e){return{activeNodeCount:e.activeNodeCount,cachedPathEntryCount:_i(e),loadInfoEntryCount:e.directoryLoadInfoById.size,segmentCount:vi(e.snapshot.segmentTable),totalNodeSlotCount:Math.max(0,e.snapshot.nodes.length-1)}}function bi(e,t,n,r){return{activeNodeCountAfter:r.activeNodeCount,activeNodeCountBefore:n.activeNodeCount,cachedPathEntryCountAfter:r.cachedPathEntryCount,cachedPathEntryCountBefore:n.cachedPathEntryCount,idsPreserved:t,loadInfoEntryCountAfter:r.loadInfoEntryCount,loadInfoEntryCountBefore:n.loadInfoEntryCount,mode:e,reclaimedCachedPathEntryCount:n.cachedPathEntryCount-r.cachedPathEntryCount,reclaimedLoadInfoEntryCount:n.loadInfoEntryCount-r.loadInfoEntryCount,reclaimedNodeSlotCount:n.totalNodeSlotCount-r.totalNodeSlotCount,reclaimedSegmentCount:n.segmentCount-r.segmentCount,segmentCountAfter:r.segmentCount,segmentCountBefore:n.segmentCount,totalNodeSlotCountAfter:r.totalNodeSlotCount,totalNodeSlotCountBefore:n.totalNodeSlotCount}}function xi(e){let t=[],n=[];for(let n of e.collapsedDirectoryIds)gi(e,n)!=null&&t.push(U(e,n));for(let t of e.expandedDirectoryIds)gi(e,t)!=null&&n.push(U(e,t));return{collapsedPaths:t,expandedPaths:n}}function Si(e){let t=[];for(let[n,r]of e.directoryLoadInfoById)gi(e,n)!=null&&$n(e,n)!==`loaded`&&t.push({info:{activeAttemptId:null,errorMessage:r.errorMessage,nextAttemptId:r.nextAttemptId,state:r.state},path:U(e,n)});return t}function Ci(e,t){e.collapsedDirectoryIds.clear(),e.hasCollapsedDirectoryOverrides=!1,e.expandedDirectoryIds.clear();for(let n of t.expandedPaths){let t=Hr(e,n);t!=null&&Zn(e,t,!0,G(e,t))}for(let n of t.collapsedPaths){let t=Hr(e,n);t!=null&&Zn(e,t,!1,G(e,t))}}function wi(e,t){e.directoryLoadInfoById.clear();for(let n of t){let t=Hr(e,n.path);t!=null&&gi(e,t)!=null&&e.directoryLoadInfoById.set(t,{activeAttemptId:null,errorMessage:n.info.errorMessage,nextAttemptId:n.info.nextAttemptId,state:n.info.state})}}function Ti(e){e.pathCacheVersion+=1,e.pathCacheByNodeId.clear(),e.pathCacheByNodeId.set(e.snapshot.rootId,{path:``,version:e.pathCacheVersion})}function Ei(e){let t=e.snapshot.segmentTable,n=En();for(let r of e.snapshot.nodes)if(hi(r)){if(V(r,2)){r.nameId=0;continue}r.nameId=Dn(n,On(t,r.nameId))}e.snapshot.segmentTable=n}function Di(e){for(let[t,n]of e.snapshot.directories){let r=e.snapshot.nodes[t];if(!hi(r)||!B(r)){e.snapshot.directories.delete(t);continue}let i=n.childIds.filter(n=>{let r=e.snapshot.nodes[n];return hi(r)&&r.parentId===t});n.childIds=i,n.childIdByNameId=new Map(i.map(t=>[G(e,t).nameId,t])),n.childPositionById=new Map(i.map((e,t)=>[e,t])),Ht(e.snapshot.nodes,n)}}function Oi(e){let t=e.snapshot.nodes.length-1;for(;t>e.snapshot.rootId;){let n=e.snapshot.nodes[t];if(hi(n))break;--t}e.snapshot.nodes.length=t+1}function ki(e){let t=xi(e),n=Si(e);H(e.instrumentation,`store.cleanup.stable.clearPathCaches`,()=>Ti(e)),H(e.instrumentation,`store.cleanup.stable.rebuildSegmentTable`,()=>Ei(e)),H(e.instrumentation,`store.cleanup.stable.rebuildDirectoryIndexes`,()=>Di(e)),H(e.instrumentation,`store.cleanup.stable.trimTrailingRemovedNodeSlots`,()=>Oi(e)),H(e.instrumentation,`store.cleanup.stable.restoreExpansionOverrides`,()=>Ci(e,t)),H(e.instrumentation,`store.cleanup.stable.restoreDirectoryLoadInfos`,()=>wi(e,n)),H(e.instrumentation,`store.cleanup.stable.recomputeCounts`,()=>Br(e,e.snapshot.rootId))}function Ai(e){let t=xi(e),n=Si(e),r=H(e.instrumentation,`store.cleanup.aggressive.listPaths`,()=>Nr(e)),i=on({...e.snapshot.options},e.instrumentation),a=H(e.instrumentation,`store.cleanup.aggressive.rebuildSnapshot`,()=>{let e=new Gn(i);return e.appendPaths(r),e.finish()});e.snapshot=a,e.activeNodeCount=a.nodes.length-1,e.pathCacheByNodeId=new Map([[a.rootId,{path:``,version:0}]]),e.pathCacheVersion=0,H(e.instrumentation,`store.cleanup.aggressive.restoreExpansionOverrides`,()=>Ci(e,t)),H(e.instrumentation,`store.cleanup.aggressive.restoreDirectoryLoadInfos`,()=>wi(e,n)),H(e.instrumentation,`store.cleanup.aggressive.recomputeCounts`,()=>Br(e,e.snapshot.rootId))}function ji(e){for(let t of e.directoryLoadInfoById.values())if(t.state===`loading`&&t.activeAttemptId!=null)return!0;return!1}function Mi(e,t){let n=yi(e);t===`stable`?H(e.instrumentation,`store.cleanup.stable`,()=>ki(e)):H(e.instrumentation,`store.cleanup.aggressive`,()=>Ai(e));let r=yi(e);return bi(t,t===`stable`,n,r)}var Ni=64;function Pi(e,t){let n=t+2;if(n<=e.length)return e;let r=e.length;for(;r<n;)r*=2;let i=new Int32Array(r);return i.fill(-1),i.set(e),i}function K(e){return G(e,e.snapshot.rootId).visibleSubtreeCount}function Fi(e,t,n,r){let i=G(e,t.terminalNodeId),a=Math.max(1,i.visibleSubtreeCount);return Math.min(r-1,n+a-1)}function Ii(e,t,n,r){return{ancestorPaths:r,index:t.index,posInSet:t.posInSet,row:ta(e,t.cursor),setSize:t.setSize,subtreeEndIndex:Fi(e,t.cursor,t.index,n)}}function Li(e,t,n,r,i,a){let o=W(e,t),{childIndex:s,childVisibleIndex:c,localVisibleIndex:l}=Wt(e.snapshot.nodes,o,n),u=o.childIds[s];if(u==null)throw Error(`Visible index ${String(n)} is out of range`);return Ri(e,u,l,r+c,i+1,s,o.childIds.length,a)}function Ri(e,t,n,r,i,a,o,s){if(!B(G(e,t))){if(n===0)return{ancestors:s,cursor:{headNodeId:t,terminalNodeId:t,visibleDepth:i},index:r,posInSet:a,setSize:o};throw Error(`Visible index ${String(n)} is out of range for file`)}let c=Yi(e,t,i);if(n===0)return{ancestors:s,cursor:c,index:r,posInSet:a,setSize:o};let l=G(e,c.terminalNodeId);if(!B(l)||!Xn(e,c.terminalNodeId,l))throw Error(`Visible index ${String(n)} is out of range for collapsed directory`);return Li(e,c.terminalNodeId,n-1,r+1,c.visibleDepth,[...s,{cursor:c,index:r,posInSet:a,setSize:o}])}function zi(e,t){let n=K(e);if(t<0||t>=n)return null;let r=Li(e,e.snapshot.rootId,t,0,-1,[]),i=r.ancestors.map(t=>U(e,t.cursor.terminalNodeId)),a=null;return{ancestorPaths:i,get ancestorRows(){if(a!=null)return a;let t=[],i=[];for(let a of r.ancestors){let r=Ii(e,a,n,[...i]);t.push(r),i.push(r.row.path)}return a=t,a},index:r.index,posInSet:r.posInSet,row:ta(e,r.cursor),setSize:r.setSize,subtreeEndIndex:Fi(e,r.cursor,r.index,n)}}function Bi(e,t,n){let r=e.instrumentation,i=K(e);if(i<=0||n<t)return[];let a=Math.max(0,Math.min(t,i-1)),o=Math.max(a,Math.min(n,i-1));if(r==null){if(a===0)return ea(e,o+1);let t=[],n=Ki(e,a);for(let r=a;r<=o&&n!=null;r++){let r=ta(e,n);t.push(r),n=Zi(e,n)}return t}let s=[],c=0,l=0,u=H(r,`store.getVisibleSlice.selectFirstRow`,()=>Ki(e,a));for(let t=a;t<=o&&u!=null;t++){let t=H(r,`store.getVisibleSlice.materializeRow`,()=>ta(e,u));s.push(t),t.isFlattened&&(c++,l+=t.flattenedSegments?.length??0),u=H(r,`store.getVisibleSlice.advanceCursor`,()=>Zi(e,u))}return cn(r,`workload.visibleRowsRead`,s.length),cn(r,`workload.flattenedRowsRead`,c),cn(r,`workload.flattenedSegmentsRead`,l),s}function Vi(e,t=K(e)){let n=e.instrumentation;return n==null?$i(e,t):H(n,`store.getVisibleTreeProjection`,()=>$i(e,t))}function Hi(e){return Qi(Vi(e))}function Ui(e,t){let n=Hr(e,t);if(n==null||n===e.snapshot.rootId||B(G(e,n))&&jr(e,n)!==n)return null;let r=0,i=n,{nodes:a,rootId:o}=e.snapshot;for(;i!==o;){let t=G(e,i).parentId,n=W(e,t),s=zt(n).get(i);if(s==null)throw Error(`Child ${String(i)} was not found in its parent index`);if(r+=Gt(a,n,s),t!==o){let n=G(e,t),a=Ar(e,t);if(!Xn(e,t,n)&&a!==i)return null;jr(e,t)===t&&(r+=1)}i=t}return r}function Wi(e,t){let n=Hr(e,t);if(n==null)throw Error(`Path does not exist: "${t}"`);let r=G(e,n);if(!B(r))throw Error(`Path is not a directory: "${t}"`);return Xn(e,n,r)?null:(Zn(e,n,!0,r),zr(e,n),ur({affectedAncestorIds:Vr(e,n),affectedNodeIds:[n],path:t,projectionChanged:!0}))}function Gi(e,t){let n=Hr(e,t);if(n==null)throw Error(`Path does not exist: "${t}"`);let r=G(e,n);if(!B(r))throw Error(`Path is not a directory: "${t}"`);return Xn(e,n,r)?(Zn(e,n,!1,r),zr(e,n),dr({affectedAncestorIds:Vr(e,n),affectedNodeIds:[n],path:t,projectionChanged:!0})):null}function Ki(e,t){return t<0||t>=K(e)?null:qi(e,e.snapshot.rootId,t,-1)}function qi(e,t,n,r){let i=W(e,t),a=e.instrumentation,{childIndex:o,localVisibleIndex:s}=a==null?Wt(e.snapshot.nodes,i,n):H(a,`store.getVisibleSlice.selectChildIndex`,()=>Wt(e.snapshot.nodes,i,n)),c=i.childIds[o];if(c!=null)return Ji(e,c,s,r+1);throw Error(`Visible index ${String(n)} is out of range`)}function Ji(e,t,n,r){if(!B(G(e,t))){if(n===0)return{headNodeId:t,terminalNodeId:t,visibleDepth:r};throw Error(`Visible index ${String(n)} is out of range for file`)}let i=Yi(e,t,r);if(n===0)return i;let a=G(e,i.terminalNodeId);if(!B(a)||!Xn(e,i.terminalNodeId,a))throw Error(`Visible index ${String(n)} is out of range for collapsed directory`);return qi(e,i.terminalNodeId,n-1,i.visibleDepth)}function Yi(e,t,n){return B(G(e,t))?e.instrumentation==null?{headNodeId:t,terminalNodeId:jr(e,t),visibleDepth:n}:{headNodeId:t,terminalNodeId:H(e.instrumentation,`store.getVisibleSlice.flatten.resolveTerminalDirectory`,()=>jr(e,t)),visibleDepth:n}:{headNodeId:t,terminalNodeId:t,visibleDepth:n}}function Xi(e,t){let n=G(e,t);if(!B(n))return!0;let r=n.parentId;return r===e.snapshot.rootId||Ar(e,r)!==t}function Zi(e,t){let n=G(e,t.terminalNodeId);if(B(n)){let r=W(e,t.terminalNodeId);if(Xn(e,t.terminalNodeId,n)&&r.childIds.length>0){let n=r.childIds[0];return n==null?null:Ji(e,n,0,t.visibleDepth+1)}}let r=t.terminalNodeId,i=t.visibleDepth;for(;;){let t=G(e,r);if(r===e.snapshot.rootId)return null;let n=t.parentId,a=W(e,n),o=zt(a).get(r)??-1;if(o<0)throw Error(`Child ${String(r)} was not found in its parent index`);let s=a.childIds[o+1]??null;if(s!=null)return Ji(e,s,0,i);Xi(e,r)&&i--,r=n}}function Qi(e){let t=e.paths.length,n=Array(t);for(let r=0;r<t;r+=1){let t=e.getParentIndex(r);n[r]={index:r,parentPath:t>=0?e.paths[t]??null:null,path:e.paths[r]??``,posInSet:e.posInSetByIndex[r]??0,setSize:e.setSizeByIndex[r]??0}}return{getParentIndex:e.getParentIndex,rows:n,get visibleIndexByPath(){return e.visibleIndexByPath}}}function $i(e,t){let n=Array(t),r=new Int32Array(t),i=new Int32Array(t),a=new Int32Array(t),o=new Int32Array(Ni);o.fill(-1);let s=0,{nodes:c,directories:l,segmentTable:u}=e.snapshot,d=[[l.get(e.snapshot.rootId),0,-1,``]],f=e.snapshot.options.flattenEmptyDirectories,p=e.pathCacheByNodeId,m=e.pathCacheVersion,h=u.valueById;for(;d.length>0&&s<t;){let t=d[d.length-1],u=t[0];if(t[1]>=u.childIds.length){d.pop();continue}let g=t[1],_=u.childIds[t[1]++],v=c[_],y=t[2]+1,b=t[3];o=Pi(o,y);let x,S=_;if(B(v))S=f?jr(e,_):_,x=S===_?`${b}${h[v.nameId]}/`:U(e,S);else{let e=p.get(_);x=e!=null&&e.version===m?e.path:`${b}${h[v.nameId]}`}r[s]=o[y],n[s]=x,i[s]=g,a[s]=u.childIds.length,o[y+1]=s,s+=1;let C=c[S];C!=null&&B(C)&&Xn(e,S,C)&&d.push([l.get(S),0,y,x])}s<t&&(n.length=s);let g=r.subarray(0,s),_=i.subarray(0,s),v=a.subarray(0,s),y=null;return{getParentIndex(e){return e<0||e>=s?-1:g[e]??-1},paths:n,posInSetByIndex:_,setSizeByIndex:v,get visibleIndexByPath(){if(y==null){y=new Map;for(let e=0;e<s;e+=1)y.set(n[e]??``,e)}return y}}}function ea(e,t){let n=Array(t),r=0,{nodes:i,directories:a,segmentTable:o}=e.snapshot,s=[[a.get(e.snapshot.rootId),0,-1]],c=o.valueById,l=e.snapshot.options.flattenEmptyDirectories,u=e.pathCacheByNodeId,d=e.pathCacheVersion;for(;s.length>0&&r<t;){let t=s[s.length-1],o=t[0];if(t[1]>=o.childIds.length){s.pop();continue}let f=o.childIds[t[1]++],p=i[f],m=t[2]+1;if(!B(p)){let t=u.get(f);n[r++]={depth:m,flattenedSegments:void 0,hasChildren:!1,id:f,isExpanded:!1,isFlattened:!1,isLoading:!1,kind:`file`,loadState:void 0,name:c[p.nameId],path:t!=null&&t.version===d?t.path:U(e,f)};continue}let h=l?jr(e,f):f,g={headNodeId:f,terminalNodeId:h,visibleDepth:m};n[r++]=ta(e,g);let _=i[h];_!=null&&B(_)&&Xn(e,h,_)&&s.push([a.get(h),0,m])}return r<t&&(n.length=r),n}function ta(e,t){let n=G(e,t.terminalNodeId),r=B(n)?na(e,t):null,i=U(e,t.terminalNodeId),a=On(e.snapshot.segmentTable,n.nameId),o=B(n)&&W(e,t.terminalNodeId).childIds.length>0,s=t.headNodeId!==t.terminalNodeId,c=e.instrumentation,l=s?c==null?Mr(e,t.headNodeId).map(n=>{let r=G(e,n);return{isTerminal:n===t.terminalNodeId,name:On(e.snapshot.segmentTable,r.nameId),nodeId:n,path:U(e,n)}}):H(c,`store.getVisibleSlice.flatten.collectSegments`,()=>Mr(e,t.headNodeId).map(n=>{let r=G(e,n);return{isTerminal:n===t.terminalNodeId,name:On(e.snapshot.segmentTable,r.nameId),nodeId:n,path:U(e,n)}})):void 0;return{depth:t.visibleDepth,flattenedSegments:l,hasChildren:o,id:t.terminalNodeId,isExpanded:B(n)&&Xn(e,t.terminalNodeId,n),isFlattened:s,isLoading:r===`loading`,kind:B(n)?`directory`:`file`,loadState:r==null||r===`loaded`?void 0:r,name:a,path:i}}function na(e,t){if(t.headNodeId===t.terminalNodeId)return $n(e,t.terminalNodeId);let n=Mr(e,t.headNodeId),r=!1,i=!1;for(let t of n){let n=$n(e,t);if(n===`loading`)return`loading`;if(n===`error`){i=!0;continue}n===`unloaded`&&(r=!0)}return i?`error`:r?`unloaded`:`loaded`}function ra(e){let{directories:t,nodes:n,options:r,rootId:i,presortedDirectoryNodeIds:a}=e.snapshot,o=r.flattenEmptyDirectories===!0,s=e=>{let r=n[e];if(r==null||!B(r))return;let i=t.get(e);if(i==null)throw Error(`Unknown directory child index for node ${String(e)}`);let a=i.childIds,s=a.length,c=0,l=0;for(let e=0;e<s;e++){let t=a[e];if(t==null)continue;let r=n[t];c+=r.subtreeNodeCount,l+=r.visibleSubtreeCount}i.totalChildSubtreeNodeCount=c,i.totalChildVisibleSubtreeCount=l,s>=128&&Kt(n,i),r.subtreeNodeCount=1+c;let u;if(o&&s===1){let e=n[a[0]];u=e!=null&&B(e)?l:1+l}else u=1+l;r.visibleSubtreeCount=u};if(a!=null)for(let e=a.length-1;e>=0;e--)s(a[e]);else for(let e=n.length-1;e>=1;e--)s(e);let c=n[i],l=t.get(i);if(c==null||l==null)return;let u=l.childIds,d=0,f=0;for(let e=0;e<u.length;e++){let t=u[e];if(t==null)continue;let r=n[t];d+=r.subtreeNodeCount,f+=r.visibleSubtreeCount}l.totalChildSubtreeNodeCount=d,l.totalChildVisibleSubtreeCount=f,Kt(n,l),c.subtreeNodeCount=1+d,c.visibleSubtreeCount=f}function ia(e){return e.initialExpansion===`open`&&(e.initialExpandedPaths==null||e.initialExpandedPaths.length===0)}var aa=class e{#e;constructor(e={}){let t=sn(e),n=H(t,`store.builder.create`,()=>new Gn(e));if(e.preparedInput!=null){let t=Hn(e.preparedInput);t==null?n.appendPreparedPaths(Vn(e.preparedInput),!1):n.appendPresortedPaths(t,Un(e.preparedInput))}else{let r=e.paths??[];e.presorted===!0?n.appendPaths(r):n.appendPreparedPaths(H(t,`store.preparePathEntries`,()=>Wn(r,e)))}let r=H(t,`store.builder.finish`,()=>n.finish({skipSubtreeCountPass:!0})),i=H(t,`store.state.detectAllDirectoriesExpanded`,()=>(e.initialExpansion??`closed`)===`closed`&&n.didMatchAllInitialExpandedPaths());this.#e=H(t,`store.state.create`,()=>Kn(r,i?`open`:e.initialExpansion??`closed`,t)),i&&(this.#e.collapseNewDirectoriesByDefault=!0);let a=i?this.#e.snapshot.directories.size-1:H(t,`store.state.initializeExpandedPaths`,()=>this.initializeExpandedPaths(e.initialExpandedPaths));i||ia(e)||(e.initialExpansion??`closed`)===`closed`&&a===this.#e.snapshot.directories.size-1||(e.initialExpandedPaths?.length??0)>0&&H(t,`store.state.checkAllDirectoriesExpanded`,()=>this.hasAllDirectoriesExpanded())?H(t,`store.state.initializeOpenVisibleCounts`,()=>ra(this.#e)):H(t,`store.state.recomputeCounts`,()=>Br(this.#e,this.#e.snapshot.rootId))}static preparePaths(e,t={}){return Rn(e,t)}static prepareInput(e,t={}){return zn(e,t)}static preparePresortedInput(e){return Bn(e)}list(e){return H(this.#e.instrumentation,`store.list`,()=>Nr(this.#e,e))}add(e){H(this.#e.instrumentation,`store.add`,()=>{let t=K(this.#e);br(this.#e,vr(this.#e,t,Pr(this.#e,e)))})}remove(e,t={}){H(this.#e.instrumentation,`store.remove`,()=>{let n=K(this.#e);br(this.#e,vr(this.#e,n,Fr(this.#e,e,t)))})}move(e,t,n={}){H(this.#e.instrumentation,`store.move`,()=>{let r=K(this.#e),i=Ir(this.#e,e,t,n);i!=null&&br(this.#e,vr(this.#e,r,i))})}batch(e){yr(this.#e,()=>{if(typeof e==`function`){e(this);return}for(let t of e)switch(t.type){case`add`:this.add(t.path);break;case`remove`:this.remove(t.path,{recursive:t.recursive});break;case`move`:this.move(t.from,t.to,{collision:t.collision});break}})}getVisibleCount(){return H(this.#e.instrumentation,`store.getVisibleCount`,()=>K(this.#e))}getVisibleSlice(e,t){return H(this.#e.instrumentation,`store.getVisibleSlice`,()=>Bi(this.#e,e,t))}getVisibleRowContext(e){return H(this.#e.instrumentation,`store.getVisibleRowContext`,()=>zi(this.#e,e))}getVisibleTreeProjection(){return Hi(this.#e)}getVisibleTreeProjectionData(e){return Vi(this.#e,e)}getVisibleIndex(e){return H(this.#e.instrumentation,`store.getVisibleIndex`,()=>Ui(this.#e,e))}getPathInfo(e){return H(this.#e.instrumentation,`store.getPathInfo`,()=>{let t=Hr(this.#e,e);if(t==null)return null;let n=G(this.#e,t);return{depth:$t(n),kind:B(n)?`directory`:`file`,path:U(this.#e,t)}})}isExpanded(e){return H(this.#e.instrumentation,`store.isExpanded`,()=>{let t=this.requireDirectoryNodeId(e),n=G(this.#e,t);return Xn(this.#e,t,n)})}expand(e){H(this.#e.instrumentation,`store.expand`,()=>{let t=K(this.#e),n=Wi(this.#e,e);n!=null&&br(this.#e,vr(this.#e,t,n))})}collapse(e){H(this.#e.instrumentation,`store.collapse`,()=>{let t=K(this.#e),n=Gi(this.#e,e);n!=null&&br(this.#e,vr(this.#e,t,n))})}on(e,t){return or(this.#e,e,t)}getDirectoryLoadState(e){let t=this.requireDirectoryNodeId(e);return $n(this.#e,t)}markDirectoryUnloaded(e){H(this.#e.instrumentation,`store.markDirectoryUnloaded`,()=>{let t=this.requireDirectoryNodeId(e);if(W(this.#e,t).childIds.length>0)throw Error(`Cannot mark a directory with known children as unloaded: "${e}"`);let n=K(this.#e);tr(this.#e,t),br(this.#e,vr(this.#e,n,fr({affectedAncestorIds:Vr(this.#e,t),affectedNodeIds:[t],path:e,projectionChanged:this.isDirectoryProjectionVisible(t)})))})}beginChildLoad(e){return H(this.#e.instrumentation,`store.beginChildLoad`,()=>{let t=this.requireDirectoryNodeId(e),n=K(this.#e),r=er(this.#e,t);return br(this.#e,vr(this.#e,n,pr({affectedAncestorIds:Vr(this.#e,t),affectedNodeIds:[t],attemptId:r.attemptId,path:e,projectionChanged:this.isDirectoryProjectionVisible(t),reused:r.reused}))),r})}applyChildPatch(e,t){return H(this.#e.instrumentation,`store.applyChildPatch`,()=>{let n=this.resolveActiveDirectoryNodeId(e.nodeId);if(n==null||$n(this.#e,n)!==`loading`||!rr(this.#e,n,e.attemptId))return!1;let r=U(this.#e,n);this.validateChildPatch(r,t);let i=K(this.#e),a=[];for(let e of t.operations){oa(r,e);let t=K(this.#e);switch(e.type){case`add`:a.push(vr(this.#e,t,Pr(this.#e,e.path)));break;case`remove`:a.push(vr(this.#e,t,Fr(this.#e,e.path,{recursive:e.recursive})));break;case`move`:{let n=Ir(this.#e,e.from,e.to,{collision:e.collision});n!=null&&a.push(vr(this.#e,t,n));break}}}let o=a.some(e=>e.projectionChanged)||this.isDirectoryProjectionVisible(n);return br(this.#e,vr(this.#e,i,mr({affectedAncestorIds:Vr(this.#e,n),affectedNodeIds:[n],attemptId:e.attemptId,childEvents:a,path:U(this.#e,n),projectionChanged:o}))),!0})}completeChildLoad(e){return H(this.#e.instrumentation,`store.completeChildLoad`,()=>{let t=this.resolveActiveDirectoryNodeId(e.nodeId);if(t==null)return!1;let n=K(this.#e),r=nr(this.#e,t,e.attemptId);return br(this.#e,vr(this.#e,n,hr({affectedAncestorIds:Vr(this.#e,t),affectedNodeIds:[t],attemptId:e.attemptId,path:U(this.#e,t),projectionChanged:this.isDirectoryProjectionVisible(t),stale:!r}))),r})}failChildLoad(e,t){return H(this.#e.instrumentation,`store.failChildLoad`,()=>{let n=this.resolveActiveDirectoryNodeId(e.nodeId);if(n==null)return!1;let r=K(this.#e),i=ir(this.#e,n,e.attemptId,t);return br(this.#e,vr(this.#e,r,gr({affectedAncestorIds:Vr(this.#e,n),affectedNodeIds:[n],attemptId:e.attemptId,errorMessage:t,path:U(this.#e,n),projectionChanged:this.isDirectoryProjectionVisible(n),stale:!i}))),i})}cleanup(e={}){return H(this.#e.instrumentation,`store.cleanup`,()=>{if(this.#e.transactionStack.length>0)throw Error(`Cleanup cannot run during an open batch or transaction.`);if(ji(this.#e))throw Error(`Cleanup cannot run while directory loads are active.`);let t=K(this.#e),n=Mi(this.#e,e.mode??`stable`);return br(this.#e,vr(this.#e,t,_r({...n,affectedAncestorIds:[],affectedNodeIds:[],projectionChanged:n.idsPreserved===!1}))),n})}getNodeCount(){return this.#e.activeNodeCount}initializeExpandedPaths(e){if(e==null||e.length===0)return 0;let t=0,n=[],r=[],i=0,a=null,o=this.#e.snapshot.segmentTable,s=o.valueById,c=this.#e.snapshot.nodes,l=new Map;for(let u of e){a!=null&&u<a&&(a=null,i=0,n.length=0,r.length=0);let e=u.length>0&&u.charCodeAt(u.length-1)===47?u.length-1:u.length;if(e===0){a=u,i=e,n.length=0,r.length=0;continue}let d=0,f=0;if(a!=null){let t=Math.min(e,i),n=!0;for(let e=0;e<t;e+=1){let t=u.charCodeAt(e);if(t!==a.charCodeAt(e)){n=!1;break}t===47&&(d+=1,f=e+1)}n&&(t===i&&e>t&&u.charCodeAt(t)===47?(d+=1,f=t+1):t===e&&i>t&&a.charCodeAt(t)===47&&(d+=1,f=e+1)),d=Math.min(d,r.length)}let p=d===0?this.#e.snapshot.rootId:r[d-1]??this.#e.snapshot.rootId,m=d,h=!0,g=f;for(;g<=e;){let t=u.indexOf(`/`,g),i=t===-1||t>e?e:t,a=u.slice(g,i),f=W(this.#e,p).childIds,_=m===d?n[m]??0:0,v=_,y,b=l.get(a)??dn(a);l.set(a,b);let x=(e,t)=>{for(v=e;v<t;v+=1){let e=f[v],t=c[e],n=s[t.nameId];if(n===a)return y=e,!0;let r=pn(bn(o,t.nameId),b);if(r>0||r===0&&n>a)return!1}return!1};if(!x(_,f.length)&&_>0&&x(0,_),y===void 0){h=!1;break}if(!B(G(this.#e,y))){h=!1;break}if(n[m]=v,r[m]=y,p=y,m+=1,i===e)break;g=i+1}if(a=u,i=e,n.length=m,r.length=m,!h){a=null,i=0,n.length=0,r.length=0;continue}for(let e=d;e<m;e+=1){let n=r[e];if(n==null)continue;let i=G(this.#e,n);Xn(this.#e,n,i)||(Zn(this.#e,n,!0,i),t+=1)}}return t}hasAllDirectoriesExpanded(){for(let e of this.#e.snapshot.directories.keys()){if(e===this.#e.snapshot.rootId)continue;let t=G(this.#e,e);if(!Xn(this.#e,e,t))return!1}return!0}requireDirectoryNodeId(e){let t=Hr(this.#e,e);if(t==null)throw Error(`Path does not exist: "${e}"`);if(!B(G(this.#e,t)))throw Error(`Path is not a directory: "${e}"`);return t}resolveActiveDirectoryNodeId(e){try{if(!B(G(this.#e,e)))throw Error(`Node is not a directory: ${String(e)}`);return e}catch{return null}}isDirectoryProjectionVisible(e){let t=e;for(;t!==this.#e.snapshot.rootId;){let e=G(this.#e,t).parentId;if(e!==this.#e.snapshot.rootId){let n=G(this.#e,e),r=Ar(this.#e,e);if(!Xn(this.#e,e,n)&&r!==t)return!1}t=e}return!0}validateChildPatch(t,n){new e({paths:this.list(t),presorted:!0,sort:this.#e.snapshot.options.sort}).batch(n.operations)}};function oa(e,t){switch(t.type){case`add`:case`remove`:if(!t.path.startsWith(e)||t.path===e)throw Error(`Child patch operation must stay within ${e}: "${t.path}"`);break;case`move`:if(!t.from.startsWith(e)||!t.to.startsWith(e)||t.from===e||t.to===e)throw Error(`Child patch move must stay within ${e}: "${t.from}" -> "${t.to}"`)}}var sa={compact:{itemHeight:24,factor:.8},default:{itemHeight:30,factor:1},relaxed:{itemHeight:36,factor:1.2}};function ca(e,t){if(typeof e==`number`)return{itemHeight:t??sa.default.itemHeight,factor:e};let n=sa[e??`default`];return{itemHeight:t??n.itemHeight,factor:n.factor}}var la=sa.default.itemHeight,ua=e=>e.startsWith(`f::`)?e.slice(3):e;function da(e){let t=e.lastIndexOf(`/`);return t<0?{parentPath:``,baseName:e}:{parentPath:e.slice(0,t),baseName:e.slice(t+1)}}function fa(e,t){return e===``?t:`${e}/${t}`}function pa({files:e,path:t,isFolder:n,nextBasename:r}){let i=ua(t),a=r.trim();if(a.length===0)return{error:`Name cannot be empty.`};if(a.includes(`/`))return{error:`Name cannot include "/".`};let{parentPath:o,baseName:s}=da(i);if(a===s)return{nextFiles:e,sourcePath:i,destinationPath:i,isFolder:n};let c=fa(o,a),l=Array(e.length),u=new Set;if(!n){let t=`${c}/`,r=!1;for(let n=0;n<e.length;n++){let a=e[n];if(a!==i&&a.startsWith(t))return{error:`"${c}" already exists.`};let o=a===i?c:a;if(u.has(o))return{error:`"${c}" already exists.`};u.add(o),l[n]=o,a===i&&(r=!0)}return r?{nextFiles:l,sourcePath:i,destinationPath:c,isFolder:n}:{error:`Could not find the selected file to rename.`}}let d=`${i}/`,f=`${c}/`,p=0;for(let t=0;t<e.length;t++){let n=e[t],r=n===i||n.startsWith(d);if(!r&&(n===c||n.startsWith(f)))return{error:`"${c}" already exists.`};let a=r?`${c}${n.slice(i.length)}`:n;if(u.has(a))return{error:`"${c}" already exists.`};u.add(a),l[t]=a,r&&p++}return p===0?{error:`Could not find the selected folder to rename.`}:{nextFiles:l,sourcePath:i,destinationPath:c,isFolder:n}}function ma(e){return e.endsWith(`/`)}function ha(e){let t=e.endsWith(`/`)?e.slice(0,-1):e,n=t.lastIndexOf(`/`),r=n<0?t:t.slice(n+1);return e.endsWith(`/`)?`${r}/`:r}function ga(e){let t=[],n=new Set;for(let r of e)n.has(r)||(n.add(r),t.push(r));let r=new Set;for(let e of t.toSorted((e,t)=>e.length===t.length?e.localeCompare(t):e.length-t.length)){let t=(e.endsWith(`/`)?e.slice(0,-1):e).split(`/`),n=!1;for(let e=0;e<t.length-1;e+=1){let i=`${t.slice(0,e+1).join(`/`)}/`;if(r.has(i)){n=!0;break}}n||r.add(e)}return t.filter(e=>r.has(e))}function _a(e,t){return t.includes(e)?ga(t):[e]}function va(e,t){return e===t?!0:e==null||t==null?!1:e.kind===t.kind&&e.directoryPath===t.directoryPath&&e.flattenedSegmentPath===t.flattenedSegmentPath&&e.hoveredPath===t.hoveredPath}function ya(e,t){return{draggedPaths:e,target:t}}function ba(e,t){if(t.kind!==`directory`||t.directoryPath==null)return!1;for(let n of e)if(ma(n)&&(t.directoryPath===n||t.directoryPath.startsWith(n)))return!0;return!1}function xa(e,t){return t.kind===`root`||t.directoryPath==null?ha(e):t.directoryPath}function Sa(e,t){let n=e.map(e=>{let n=xa(e,t);return n===e?null:{from:e,to:n,type:`move`}}).filter(e=>e!=null);return n.length===0?null:{operations:n,result:{draggedPaths:e,operation:n.length===1?`move`:`batch`,target:t}}}function Ca(e,t){if(e===t)return!0;if(e.length!==t.length)return!1;for(let n=0;n<e.length;n+=1)if(e[n]!==t[n])return!1;return!0}function wa(e,t,n){let{paths:r,preparedInput:i}=e;if(i==null){if(r==null)throw Error(`FileTree requires paths or preparedInput`);return{paths:r,preparedInput:void 0}}let a=i.paths;if(r==null)return{paths:a,preparedInput:i};if(!Ca(aa.preparePaths(r,n==null?{}:{sort:n}),a))throw Error(`FileTree ${t} received paths and preparedInput for different path lists`);return{paths:a,preparedInput:i}}function Ta(e){return e.operation===`add`||e.operation===`remove`||e.operation===`move`||e.operation===`batch`}function Ea(e,t,n){if(e===t)return n;let r=t.endsWith(`/`)?t:`${t}/`;return e.startsWith(r)?`${n.endsWith(`/`)?n:`${n}/`}${e.slice(r.length)}`:e}function Da(e,t){if(e===t)return!0;let n=t.endsWith(`/`)?t:`${t}/`;return e.startsWith(n)}function Oa(e,t,n=!1){if(e==null)return null;switch(t.operation){case`add`:case`expand`:case`collapse`:case`mark-directory-unloaded`:case`begin-child-load`:case`apply-child-patch`:case`complete-child-load`:case`fail-child-load`:case`cleanup`:return e;case`remove`:return Da(e,t.path)?n?e:null:e;case`move`:return Ea(e,t.from,t.to);case`batch`:{let r=e;for(let e of t.events)if(r=Oa(r,e,n),r==null)return null;return r}}}function ka(e){return{canonicalChanged:e.canonicalChanged,projectionChanged:e.projectionChanged,visibleCountDelta:e.visibleCountDelta}}function Aa(e){switch(e.operation){case`add`:return{...ka(e),operation:`add`,path:e.path};case`remove`:return{...ka(e),operation:`remove`,path:e.path,recursive:e.recursive};case`move`:return{...ka(e),from:e.from,operation:`move`,to:e.to}}}function ja(e){return{...ka(e),events:e.events.filter(e=>e.operation===`add`||e.operation===`remove`||e.operation===`move`).map(e=>Aa(e)),operation:`batch`}}function Ma(e){switch(e.operation){case`add`:case`remove`:case`move`:return Aa(e);case`batch`:return ja(e);default:return null}}function Na(e,t){if(e.size!==t.length)return!1;for(let n of t)if(!e.has(n))return!1;return!0}function Pa(e){let t=e.endsWith(`/`)?e.slice(0,-1):e;if(t.length===0)return[];let n=t.split(`/`);return n.slice(0,-1).map((e,t)=>`${n.slice(0,t+1).join(`/`)}/`)}function Fa(e){return Pa(e).at(-1)??null}function Ia(e,t){return t==null?e:e.startsWith(t)?e.slice(t.length):e}function La(e){return e.endsWith(`/`)}var Ra=e=>e.toLowerCase();function za(e){let t=e.endsWith(`/`)?e.slice(0,-1):e,n=t.lastIndexOf(`/`);return n<0?t:t.slice(n+1)}function Ba(e){return e.endsWith(`/`)?e.slice(0,-1):e}function Va(e,t){return t&&!e.endsWith(`/`)?`${e}/`:e}var Ha=e=>{let t=e.trim();return t.length===0?``:(t.includes(`\\`)?t.replaceAll(`\\`,`/`):t).toLowerCase()},Ua=Symbol(`FILE_TREE_RENAME_VIEW`),Wa=512,Ga=512;function Ka(e){return e===`top`||e===`center`?e:`nearest`}function qa(e,t,n){if(e===0)return-1;if(n!=null){let e=t(n);if(e!=null)return e;let r=Pa(n);for(let e=r.length-1;e>=0;--e){let n=r[e];if(n==null)continue;let i=t(n);if(i!=null)return i}}return 0}function Ja(e,t,n){if(e.paths.length===0)return{focusedIndex:-1,getParentIndex:e.getParentIndex,paths:e.paths,posInSetByIndex:e.posInSetByIndex,setSizeByIndex:e.setSizeByIndex};if(t==null)return{focusedIndex:0,getParentIndex:e.getParentIndex,paths:e.paths,posInSetByIndex:e.posInSetByIndex,setSizeByIndex:e.setSizeByIndex};let r=n??(t=>e.visibleIndexByPath.get(t)??null);return{focusedIndex:qa(e.paths.length,r,t),getParentIndex:e.getParentIndex,paths:e.paths,posInSetByIndex:e.posInSetByIndex,setSizeByIndex:e.setSizeByIndex}}var Ya=class{#e;#t=new Set;#n=new Map;#r=null;#i=null;#a=new Map;#o=new Map;#s=-1;#c=null;#l=!1;#u=e=>-1;#d=new Map;#f=null;#p=null;#m=null;#h=null;#g=null;#_;#v;#y;#b=[];#x=new Int32Array;#S=new Int32Array;#C=void 0;#w=!1;#T=null;#E=``;#D=!1;#O=new Set;#k=[];#A;#j=null;#M=null;#N=null;#P=null;#F=null;#I=null;#L=null;#R=0;#z=null;#B=new Set;#V=0;#H;#U=0;#W=!1;#G=0;#K;constructor(e){let{dragAndDrop:t,fileTreeSearchMode:n,initialSearchQuery:r,initialSelectedPaths:i,renaming:a,onSearchChange:o,paths:s,preparedInput:c,...l}=e,u=wa({paths:s,preparedInput:c},`constructor`,l.sort);this.#e=l,t!=null&&t!==!1&&(this.#r=t===!0?{}:t),this.#w=a!=null&&a!==!1,a!=null&&a!==!1&&a!==!0&&(this.#C=a.canRename,this.#v=a.onError,this.#_=a.onRename),this.#y=o,this.#A=n??`hide-non-matches`,this.#H=this.#le(u.paths,u.preparedInput);let d=i?.map(e=>this.#Ne(e)).filter(e=>e!=null)??[],f=d.at(-1)??null;d.length>0&&(this.#B=new Set(d),this.#z=f,this.#V=1),this.#je(f,!1),r!=null&&this.#Te(r,!1),this.#K=this.#Re()}destroy(){this.#K?.(),this.#K=null,this.#n.clear(),this.#t.clear(),this.#d.clear(),this.#i=null,this.#he()}focusFirstItem(){this.#be().length>0&&this.#Fe(0)}focusLastItem(){this.#G<=0||(this.#Ie(),this.#Fe(this.#G-1))}focusNextItem(){this.#Ae(1)}focusParentItem(){if(this.#c==null)return;let e=Fa(this.#c);if(e==null)return;let t=this.#Z(e);t>=0&&this.#Fe(t)}focusPath(e){let t=this.#H.getPathInfo(e)?.path??null;if(t==null)return;this.#Ie();let n=this.#Z(t);n>=0&&this.#Fe(n)}scrollToPath(e,t){let n=this.#H.getPathInfo(e)?.path??null;if(n==null)return;this.#Ie();let r=this.#xe(n);r<0||this.#Me(r)!=null&&(t?.focus!==!1&&this.#Fe(r,!1),this.#L={id:this.#R+=1,offset:Ka(t?.offset),visibleIndex:r},this.#De())}focusMountedPathFromInput(e){let t=this.#H.getPathInfo(e)?.path??null;if(t==null)return;let n=this.#Z(t);n>=0&&this.#Fe(n)}focusNearestPath(e){let t=this.resolveNearestVisiblePath(e);if(t==null)return null;let n=this.#Z(t);return n>=0?(this.#Fe(n),this.#be()[n]??t):null}focusPreviousItem(){this.#Ae(-1)}getFocusedIndex(){return this.#s}getFocusedItem(){return this.#c==null?null:this.#Q(this.#c)}getFocusedPath(){return this.#c}getScrollRequest(){return this.#L}clearScrollRequest(e){this.#L?.id===e&&(this.#L=null)}resolveNearestVisiblePath(e){let t=this.#be();if(this.#G===0)return null;if(e==null)return this.#c??t[0]??null;let n=this.#H.getPathInfo(e)?.path??e,r=this.#Z(n);return r>=0?t[r]??n:this.#X(n)??this.#c??t[0]??null}getSelectedPaths(){return[...this.#B]}getSelectionVersion(){return this.#V}getVisibleCount(){return this.#G}getVisibleRows(e,t){if(t<e||this.#G===0)return[];let n=Math.max(0,e),r=Math.min(this.#G-1,t);if(r<n)return[];let i=r-n+1;if(this.#F==null&&!this.#l&&r>=this.#b.length&&i<=Ga){let e=[];for(let t=n;t<=r;t+=1){let n=this.#H.getVisibleRowContext(t);if(n==null)break;e.push(this.#ee(n))}return e}if(!this.#l&&r>=this.#b.length&&this.#Ie(),this.#F!=null){let e=Array.from({length:r-n+1},(e,t)=>this.#Se(n+t)),t=new Map,i=e[0]??-1,a=i;for(let n=1;n<=e.length;n+=1){let r=e[n];if(r!=null&&r===a+1){a=r;continue}if(i>=0&&this.#H.getVisibleSlice(i,a).forEach((e,n)=>{t.set(i+n,e)}),r==null){i=-1,a=-1;continue}i=r,a=r}return Array.from({length:r-n+1},(e,r)=>{let i=n+r,a=this.#Se(i),o=t.get(a),s=this.#b[a];if(o==null||s==null)throw Error(`Missing projection row for filtered visible index ${String(i)}`);return this.#$(o,i,a,{ancestorPaths:this.#re(a),path:s})})}return this.#H.getVisibleSlice(n,r).map((e,t)=>{let r=n+t,i=this.#b[r];if(i==null)throw Error(`Missing projection path for visible index ${String(r)}`);return this.#$(e,r,r,{ancestorPaths:this.#re(r),path:i})})}getStickyRowCandidates(e,t){if(this.#F!=null)return null;if(this.#G===0||e<=0||t<=0)return[];let n=[];for(let r=0;r<this.#G;r+=1){let i=e+r*t,a=Math.min(this.#G-1,Math.floor(i/t)),o=this.#te(a,r)??(a>0?this.#te(a-1,r):void 0);if(o==null)break;n.push({row:this.#ee(o),subtreeEndIndex:o.subtreeEndIndex})}return n}getItem(e){let t=this.#H.getPathInfo(e);return t==null?null:this.#Q(t.path,t)}resolveMountedDirectoryPathFromInput(e){let t=this.#H.getPathInfo(e);return t?.kind===`directory`?t.path:null}toggleMountedDirectoryFromInput(e){let t=this.resolveMountedDirectoryPathFromInput(e);t!=null&&this.#ze(t)}selectAllVisiblePaths(){this.#Ie();let e=[...this.#be()];this.#ae(e,this.#c??this.#z)}selectOnlyPath(e){let t=this.#Ne(e);t!=null&&this.#ae([t],t)}selectOnlyMountedPathFromInput(e){this.#ae([e],e)}selectPath(e){let t=this.#Ne(e);t==null||this.#B.has(t)||this.#ae([...this.#B,t])}deselectPath(e){let t=this.#Ne(e);t==null||!this.#B.has(t)||this.#ae([...this.#B].filter(e=>e!==t))}toggleFocusedSelection(){this.#c!=null&&this.togglePathSelectionFromInput(this.#c)}togglePathSelection(e){let t=this.#Ne(e);if(t!=null){if(this.#B.has(t)){this.deselectPath(t);return}this.selectPath(t)}}togglePathSelectionFromInput(e){let t=this.#Ne(e);if(t!=null){if(this.#B.has(t)){this.#ae([...this.#B].filter(e=>e!==t),t);return}this.#ae([...this.#B,t],t)}}selectPathRange(e,t){let n=this.#Ne(e);if(n==null)return;this.#Ie();let r=this.#z,i=r==null?-1:this.#Ce(r),a=this.#Ce(n);if(i===-1||a===-1){let e=t?[...this.#B,n]:[n];this.#ae(e,n);return}let[o,s]=i<=a?[i,a]:[a,i],c=this.#be().slice(o,s+1),l=t?[...this.#B,...c]:c;this.#ae(l,r)}extendSelectionFromFocused(e){if(this.#c==null)return;let t=this.#s;if(t===-1)return;let n=Math.min(this.#G-1,Math.max(0,t+e));if(n===t)return;!this.#l&&n>=this.#b.length&&this.#Ie();let r=this.#be(),i=r[t]??null,a=r[n]??null;if(i==null||a==null)return;let o=new Set(this.#B);o.has(i)&&o.has(a)?o.delete(i):o.add(a),this.#ae([...o],this.#z??i,!1),this.#Fe(n)}getDragAndDropConfig(){return this.#r}isDragAndDropEnabled(){return this.#r!=null}getDragSession(){return this.#i==null?null:{draggedPaths:[...this.#i.draggedPaths],primaryPath:this.#i.primaryPath,target:this.#i.target==null?null:{...this.#i.target}}}startDrag(e){if(this.#r==null)return!1;let t=this.#Ne(e);if(t==null||this.#M!=null&&this.#M.length>0)return!1;let n=this.getSelectedPaths(),r=_a(t,n);return this.#r.canDrag?.(r)!==!1&&(n.includes(t)||this.#ae([t],t,!1),this.#Pe(t),this.#i={draggedPaths:r,primaryPath:t,target:null},this.#De(),!0)}setDragTarget(e){let t=this.#i;if(t==null)return;let n=e;if(n!=null){let e=ya(t.draggedPaths,n);(ba(t.draggedPaths,n)||this.#r?.canDrop?.(e)===!1)&&(n=null)}va(t.target,n)||(this.#i={...t,target:n},this.#De())}cancelDrag(){this.#i!=null&&(this.#i=null,this.#De())}completeDrag(){let e=this.#i;if(e==null)return!1;this.#i=null;let t=e.target==null?null:{...e.target};if(t==null)return this.#De(),!1;let n=ya(e.draggedPaths,t);if(ba(e.draggedPaths,t)||this.#r?.canDrop?.(n)===!1)return this.#De(),!1;let r=Sa(e.draggedPaths,t);if(r==null)return this.#De(),!1;try{if(r.operations.length===1){let e=r.operations[0];if(e==null||e.type!==`move`)throw Error(`Expected a single move operation for one-item drops`);this.#H.move(e.from,e.to,{collision:e.collision})}else this.#ce(r.operations),this.#H.batch(r.operations)}catch(e){return this.#De(),this.#r?.onDropError?.(e instanceof Error?e.message:String(e),n),!1}return this.#r?.onDropComplete?.(r.result),!0}subscribe(e){return this.#t.add(e),e(),()=>{this.#t.delete(e)}}add(e){this.#H.add(e)}remove(e,t={}){this.#H.remove(e,t)}move(e,t,n={}){this.#H.move(e,t,n)}batch(e){this.#H.batch(e)}onMutation(e,t){let n=e,r=t,i=this.#n.get(n);return i??(i=new Set,this.#n.set(n,i)),i.add(r),()=>{let e=this.#n.get(n);e?.delete(r),e?.size===0&&this.#n.delete(n)}}setSearch(e){this.#Te(e,!0)}openSearch(e=``){this.#Te(e,!0)}closeSearch(){this.#Te(null,!0)}isSearchOpen(){return this.#M!==null}getSearchValue(){return this.#M??``}getSearchMatchingPaths(){return this.#k}focusNextSearchMatch(){this.#we(1)}focusPreviousSearchMatch(){this.#we(-1)}startRenaming(e=this.#c??``,t={}){if(!this.#w)return!1;let n=this.#H.getPathInfo(e);if(n==null)return!1;let r=n.path,i=La(r),a=Ba(r);if(this.#C?.({isFolder:i,path:a})===!1)return!1;for(let e of Pa(r))this.#H.isExpanded(e)||this.#H.expand(e);return this.#ae([r],r,!1),this.#M!=null&&(this.#Te(null,!1),this.#y?.(this.#M)),this.#Pe(r),this.#T=r,this.#E=za(r),this.#D=t.removeIfCanceled??!1,this.#De(),!0}[Ua](){return{cancel:()=>{this.#q()},commit:()=>{this.#J()},getPath:()=>this.#T,getValue:()=>this.#E,isActive:()=>this.#T!=null,setValue:e=>{this.#Y(e)}}}#q(){if(this.#T==null)return;let e=this.#T,t=this.#D;if(this.#T=null,this.#E=``,this.#D=!1,t){this.remove(e,La(e)?{recursive:!0}:void 0);return}this.#Pe(e),this.#De()}#J(){let e=this.#T;if(e==null)return;if(this.#D&&this.#E.trim().length===0){this.#T=null,this.#E=``,this.#D=!1,this.remove(e,La(e)?{recursive:!0}:void 0);return}let t=La(e),n=pa({files:this.#H.list(),isFolder:t,nextBasename:this.#E,path:Ba(e)});if(this.#T=null,this.#E=``,this.#D=!1,`error`in n){this.#Pe(e),this.#v?.(n.error),this.#De();return}if(n.sourcePath===n.destinationPath){this.#Pe(e),this.#De();return}this.#_?.({destinationPath:n.destinationPath,isFolder:n.isFolder,sourcePath:n.sourcePath}),this.move(Va(n.sourcePath,t),Va(n.destinationPath,t))}#Y(e){this.#T!=null&&this.#E!==e&&(this.#E=e,this.#De())}resetPaths(e,t={}){let n=this.#H.list().length,r=this.#G,i=wa({paths:e,preparedInput:t.preparedInput},`resetPaths`,this.#e.sort),a=this.#le(i.paths,i.preparedInput,t.initialExpandedPaths),o=this.#c,s=this.#T,c=this.getSelectedPaths(),l=this.#z;this.#K?.(),this.#H=a,this.#d.clear(),this.#he();let u=c.map(e=>a.getPathInfo(e)?.path??null).filter(e=>e!=null),d=!Na(this.#B,u);this.#B=new Set(u),d&&(this.#V+=1),this.#z=l==null?null:a.getPathInfo(l)?.path??null,this.#T=s==null?null:a.getPathInfo(s)?.path??null,this.#T??(this.#E=``,this.#D=!1),this.#je(o,o!=null||u.length>0||this.#z!=null),this.#K=this.#Re(),this.#De(),this.#Oe({canonicalChanged:!0,operation:`reset`,pathCountAfter:i.paths.length,pathCountBefore:n,projectionChanged:!0,usedPreparedInput:t.preparedInput!=null,visibleCountDelta:this.#G-r})}#X(e){this.#Ie();let t=Fa(e),n=Ia(e,t),r=null,i=null;for(let e of this.#be()){if(Fa(e)!==t)continue;let a=Ia(e,t);if(a<n){r=e;continue}if(a>n){i=e;break}}return r??i}#Z(e){let t=this.#Ce(e);if(t!==-1)return t;let n=Pa(e);for(let e=n.length-1;e>=0;--e){let t=n[e];if(t==null)continue;let r=this.#Ce(t);if(r!==-1)return r}return this.#be().length>0?0:-1}#Q(e,t){let n=this.#d.get(e);if(n!=null)return n;let r=t??this.#H.getPathInfo(e);if(r==null)return null;let i=r.kind===`directory`?this.#oe(r.path):this.#se(r.path);return this.#d.set(r.path,i),i}#$(e,t,n,r){return{ancestorPaths:r.ancestorPaths,depth:e.depth,flattenedSegments:e.flattenedSegments?.map(e=>({isTerminal:e.isTerminal,name:e.name,path:e.path})),hasChildren:e.hasChildren,index:t,isExpanded:e.isExpanded,isFlattened:e.isFlattened,isFocused:r.path===this.#c,isSelected:this.#B.has(r.path),kind:e.kind,level:e.depth,name:e.name,path:r.path,posInSet:r.posInSet??this.#x[n]??0,setSize:r.setSize??this.#S[n]??0}}#ee(e){return this.#$(e.row,e.index,e.index,{ancestorPaths:e.ancestorPaths,path:e.row.path,posInSet:e.posInSet,setSize:e.setSize})}#te(e,t){let n=this.#H.getVisibleRowContext(e);return n==null?void 0:n.ancestorRows[t]??(t===n.ancestorRows.length&&n.row.kind===`directory`&&n.row.isExpanded?n:void 0)}#ne(e){let t=this.#a.get(e);if(t!=null)return t;let n=this.#u(e),r=n<0?[]:[...this.#ne(n),n];return this.#a.set(e,r),r}#re(e){let t=this.#o.get(e);if(t!=null)return t;let n=this.#ne(e).map(e=>this.#b[e]??``).filter(e=>e!==``);return this.#o.set(e,n),n}#ie(e){this.#H.collapse(e)}#ae(e,t=this.#z,n=!0){let r=[...new Set(e)],i=!Na(this.#B,r),a=this.#z!==t;!i&&!a||(this.#B=new Set(r),this.#z=t,i&&(this.#V+=1),n&&this.#De())}#oe(e){return{collapse:()=>{this.#ie(e)},deselect:()=>{this.deselectPath(e)},expand:()=>{this.#ke(e)},focus:()=>{this.focusPath(e)},getPath:()=>e,isDirectory:()=>!0,isExpanded:()=>this.#H.isExpanded(e),isFocused:()=>this.#c===e,isSelected:()=>this.#B.has(e),select:()=>{this.selectPath(e)},toggleSelect:()=>{this.togglePathSelection(e)},toggle:()=>{this.#ze(e)}}}#se(e){return{deselect:()=>{this.deselectPath(e)},focus:()=>{this.focusPath(e)},getPath:()=>e,isDirectory:()=>!1,isFocused:()=>this.#c===e,isSelected:()=>this.#B.has(e),select:()=>{this.selectPath(e)},toggleSelect:()=>{this.togglePathSelection(e)}}}#ce(e){let t=this.#H.list();this.#le(t).batch(e)}#le(e,t,n){return new aa({...this.#e,paths:e,preparedInput:t??void 0,...n===void 0?{}:{initialExpandedPaths:n}})}#ue(){return this.#h??=this.#H.list(),this.#h}#de(){if(this.#m!=null)return this.#m;let e=new Set;for(let t of this.#ue()){e.add(t);for(let n of Pa(t))e.add(n)}return this.#m=[...e].sort(),this.#m}#fe(){return this.#g??=this.#ue().map(Ra),this.#g}#pe(){return this.#f??=this.#de().filter(e=>e.endsWith(`/`)),this.#f}#me(){return this.#p??=this.#pe().map(Ra),this.#p}#he(){this.#f=null,this.#p=null,this.#m=null,this.#h=null,this.#g=null}#ge(){return this.#pe().filter(e=>this.#H.isExpanded(e))}#_e(e){let t=new Set(this.#j??[]);if(e)for(let e of this.#B)for(let n of Pa(e))t.add(n);this.#ve(t)}#ve(e){this.#W=!0;try{for(let t of this.#pe()){let n=e.has(t),r=this.#H.isExpanded(t);n&&!r?this.#H.expand(t):!n&&r&&this.#H.collapse(t)}}finally{this.#W=!1}}#ye(){if(this.#M==null||this.#M.length===0){this.#k=[],this.#F=null,this.#I=null,this.#P=null,this.#G=this.#U;return}let e=this.#b;if(this.#k=e.filter(e=>this.#O.has(e)),this.#A!==`hide-non-matches`||this.#O.size===0){this.#F=null,this.#I=null,this.#P=null,this.#G=this.#U;return}let t=[],n=[],r=new Map;for(let[i,a]of e.entries())this.#N?.has(a)===!0&&(r.set(a,n.length),t.push(i),n.push(a));this.#F=t,this.#I=n,this.#P=r,this.#G=n.length}#be(){return this.#I??this.#b}#xe(e){return this.#I==null?this.#H.getVisibleIndex(e)??-1:this.#P?.get(e)??-1}#Se(e){return this.#F?.[e]??e}#Ce(e){return this.#P?.get(e)??this.#H.getVisibleIndex(e)??-1}#we(e){let t=this.#k;if(t.length===0)return;let n=this.#c,r=n==null?-1:t.indexOf(n),i=t[r<0?e>0?0:t.length-1:Math.min(t.length-1,Math.max(0,r+e))];i!=null&&this.focusPath(i)}#Te(e,t){let n=e==null?null:Ha(e),r=this.#M;if(r!==n){if(r==null&&n!=null&&(this.#j=this.#ge()),this.#M=n,n==null)this.#_e(!0),this.#j=null,this.#O.clear(),this.#N=null,this.#je(this.#c,!0);else if(n.length===0)this.#_e(!1),this.#O.clear(),this.#N=null,this.#je(this.#c,!0);else{let e=this.#Ee();this.#je(e,!0)}t&&(this.#y?.(this.#M),this.#De())}}#Ee(){if(this.#M==null||this.#M.length===0)return this.#O.clear(),this.#c;let e=this.#M,t=this.#ue(),n=this.#fe(),r=[],i=new Set,a=null;for(let o=0;o<t.length;o+=1){if(!n[o].includes(e))continue;let s=t[o];r.push(s),i.add(s),a??=s}let o=this.#pe(),s=this.#me();for(let t=0;t<o.length;t+=1){if(!s[t].includes(e))continue;let n=o[t];i.has(n)||(r.push(n),i.add(n),a??=n)}this.#O=i;let c=this.#A===`hide-non-matches`&&r.length>0?new Set:null;this.#N=c;let l=this.#A===`expand-matches`?new Set(this.#j??[]):new Set;for(let e of r){c?.add(e),e.endsWith(`/`)&&l.add(e);for(let t of Pa(e))l.add(t),c?.add(t)}return this.#ve(l),a??this.#c}#De(e){for(let t of this.#t)t(e)}#Oe(e){this.#n.get(e.operation)?.forEach(t=>{t(e)}),this.#n.get(`*`)?.forEach(t=>{t(e)})}#ke(e){for(let t of Pa(e))this.#H.isExpanded(t)||this.#H.expand(t);this.#H.isExpanded(e)||this.#H.expand(e)}#Ae(e){let t=this.#G;if(t===0)return;let n=this.#s===-1?0:this.#s,r=Math.min(t-1,Math.max(0,n+e));(r!==n||this.#s===-1)&&(!this.#l&&this.#F==null&&r>=this.#b.length&&this.#Ie(),this.#Fe(r))}#je(e,t=!0){let n=this.#H.getVisibleCount();this.#U=n;let r=Ja(this.#H.getVisibleTreeProjectionData(t?void 0:Math.min(n,Wa)),e,t?e=>this.#H.getVisibleIndex(e):void 0);this.#a.clear(),this.#o.clear(),this.#l=r.paths.length>=n,this.#u=r.getParentIndex,this.#b=r.paths,this.#x=r.posInSetByIndex,this.#S=r.setSizeByIndex,this.#ye(),this.#s=e==null?this.#be().length>0?0:-1:this.#Z(e),this.#c=this.#s<0?null:this.#Me(this.#s)}#Me(e){return this.#be()[e]??(this.#F==null?this.#H.getVisibleRowContext(e)?.row.path??null:null)}#Ne(e){return this.#H.getPathInfo(e)?.path??null}#Pe(e){if(e==null)return;let t=this.#Z(e);t>=0&&this.#Fe(t,!1)}#Fe(e,t=!0){let n=this.#Me(e);n!=null&&(this.#s!==e||this.#c!==n)&&(this.#s=e,this.#c=n,t&&this.#De())}#Ie(){this.#l||this.#je(this.#c,!0)}#Le(e){let t=Oa(this.#T,e);t==null&&this.#T!=null&&(this.#E=``),this.#T=t;let n=Oa(this.#c,e,!0),r=[...this.#B].map(t=>Oa(t,e)).filter(e=>e!=null).map(e=>this.#H.getPathInfo(e)?.path??null).filter(e=>e!=null),i=Oa(this.#z,e),a=i==null?null:this.#H.getPathInfo(i)?.path??null,o=[...new Set(r)];return Na(this.#B,o)||(this.#B=new Set(o),this.#V+=1),this.#z=a,n}#Re(){return this.#H.on(`*`,e=>{if(this.#W)return;e.canonicalChanged&&(this.#d.clear(),this.#he()),this.#i!=null&&Ta(e)&&(this.#i=null);let t=Ta(e)?this.#Le(e):this.#c,n=this.#M!=null&&this.#M.length>0?this.#Ee():this.#M===``?this.#c:t,r=this.#M!=null||e.operation!==`expand`&&e.operation!==`collapse`;this.#je(n,r),this.#De(e);let i=Ma(e);i!=null&&this.#Oe(i)})}#ze(e){if(this.#H.isExpanded(e)){this.#ie(e);return}this.#ke(e)}},Xa=e=>{if(e==null||e.length===0)return`0`;let t=`${e.length}`;for(let n of e)t+=`\0${n.path}\0${n.status}`;return t};function Za(e){let t=e.endsWith(`/`),n=``,r=-1;for(let t=0;t<=e.length;t+=1){if(e[t]!==`/`&&t!==e.length){r===-1&&(r=t);continue}r!==-1&&(n!==``&&(n+=`/`),n+=e.slice(r,t),r=-1)}return n===``?null:{isDirectory:t,path:n}}function Qa(e){let t=e.endsWith(`/`)?e.slice(0,-1):e;if(t.length===0)return[];let n=t.split(`/`);return n.slice(0,-1).map((e,t)=>`${n.slice(0,t+1).join(`/`)}/`)}function $a(e,t){return t?`${e}/`:e}function eo(e,t=null){let n=Xa(e==null?void 0:[...e]);if(n===`0`)return null;if(t?.signature===n)return t;let r=new Map,i=new Set,a=new Set;for(let t of e??[]){let e=Za(t.path);if(e==null)continue;let n=$a(e.path,e.isDirectory);r.set(n,t.status),t.status===`ignored`&&e.isDirectory?a.add(n):e.isDirectory&&a.delete(n);for(let t of Qa(e.path))i.add(t)}return{directoriesWithChanges:i,ignoredDirectoryPaths:a,signature:n,statusByPath:r}}var q,to,no,ro,io,ao,oo,so,co,lo,uo={},fo=[],po=Array.isArray,mo=fo.slice,ho=Object.assign;function go(e){e&&e.parentNode&&e.remove()}function _o(e,t,n){var r,i,a,o={};for(a in t)a==`key`?r=t[a]:a==`ref`&&typeof e!=`function`?i=t[a]:o[a]=t[a];return arguments.length>2&&(o.children=arguments.length>3?mo.call(arguments,2):n),vo(e,o,r,i,null)}function vo(e,t,n,r,i){var a={type:e,props:t,key:n,ref:r,__k:null,__:null,__b:0,__e:null,__c:null,constructor:void 0,__v:i??++to,__i:-1,__u:0};return i==null&&q.vnode!=null&&q.vnode(a),a}function yo(e){return e.children}function bo(e,t){this.props=e,this.context=t,this.__g=0}function xo(e,t){if(t==null)return e.__?xo(e.__,e.__i+1):null;for(var n;t<e.__k.length;t++)if((n=e.__k[t])!=null&&n.__e!=null)return n.__e;return typeof e.type==`function`?xo(e):null}function So(e){var t,n;if((e=e.__)!=null&&e.__c!=null){for(e.__e=null,t=0;t<e.__k.length;t++)if((n=e.__k[t])!=null&&n.__e!=null){e.__e=n.__e;break}return So(e)}}function Co(e){(8&e.__g||!(e.__g|=8)||!no.push(e)||io++)&&ro==q.debounceRendering||((ro=q.debounceRendering)||queueMicrotask)(wo)}function wo(){for(var e,t,n,r,i,a,o,s,c=1;no.length;)no.length>c&&no.sort(ao),e=no.shift(),c=no.length,8&e.__g&&(n=void 0,i=(r=(t=e).__v).__e,a=[],o=[],(s=t.__P)&&((n=ho({},r)).__v=r.__v+1,q.vnode&&q.vnode(n),Mo(s,n,r,t.__n,s.namespaceURI,32&r.__u?[i]:null,a,i??xo(r),!!(32&r.__u),o,s.ownerDocument),n.__v=r.__v,n.__.__k[n.__i]=n,Po(a,n,o),n.__e!=i&&So(n)));io=0}function To(e,t,n,r,i,a,o,s,c,l,u,d){var f,p,m,h,g,_,v,y=r&&r.__k||fo,b=t.length;for(c=Eo(n,t,y,c,b),f=0;f<b;f++)(m=n.__k[f])!=null&&(p=m.__i==-1?uo:y[m.__i]||uo,m.__i=f,_=Mo(e,m,p,i,a,o,s,c,l,u,d),h=m.__e,m.ref&&p.ref!=m.ref&&(p.ref&&Lo(p.ref,null,m),u.push(m.ref,m.__c||h,m)),g==null&&h!=null&&(g=h),(v=!!(4&m.__u))||p.__k===m.__k?c=Do(m,c,e,v):typeof m.type==`function`&&_!==void 0?c=_:h&&(c=h.nextSibling),m.__u&=-7);return n.__e=g,c}function Eo(e,t,n,r,i){var a,o,s,c,l,u=n.length,d=u,f=0;for(e.__k=Array(i),a=0;a<i;a++)(o=t[a])!=null&&typeof o!=`boolean`&&typeof o!=`function`?(c=a+f,(o=e.__k[a]=typeof o==`string`||typeof o==`number`||typeof o==`bigint`||o.constructor==String?vo(null,o,null,null,null):po(o)?vo(yo,{children:o},null,null,null):o.constructor==null&&o.__b>0?vo(o.type,o.props,o.key,o.ref?o.ref:null,o.__v):o).__=e,o.__b=e.__b+1,s=null,(l=o.__i=Oo(o,n,c,d))!=-1&&(d--,(s=n[l])&&(s.__u|=2)),s==null||s.__v==null?(l==-1&&(i>u?f--:i<u&&f++),typeof o.type!=`function`&&(o.__u|=4)):l!=c&&(l==c-1?f--:l==c+1?f++:(l>c?f--:f++,o.__u|=4))):e.__k[a]=null;if(d)for(a=0;a<u;a++)(s=n[a])!=null&&!(2&s.__u)&&(s.__e==r&&(r=xo(s)),Ro(s,s));return r}function Do(e,t,n,r){var i,a;if(typeof e.type==`function`){for(i=e.__k,a=0;i&&a<i.length;a++)i[a]&&(i[a].__=e,t=Do(i[a],t,n,r));return t}e.__e!=t&&(r&&(t&&e.type&&!t.parentNode&&(t=xo(e)),n.insertBefore(e.__e,t||null)),t=e.__e);do t&&=t.nextSibling;while(t!=null&&t.nodeType==8);return t}function Oo(e,t,n,r){var i,a,o,s=e.key,c=e.type,l=t[n],u=l!=null&&!(2&l.__u);if(l===null&&e.key==null||u&&s==l.key&&c==l.type)return n;if(r>+!!u){for(i=n-1,a=n+1;i>=0||a<t.length;)if((l=t[o=i>=0?i--:a++])!=null&&!(2&l.__u)&&s==l.key&&c==l.type)return o}return-1}function ko(e,t,n){t[0]==`-`?e.setProperty(t,n??``):e[t]=n??``}function Ao(e,t,n,r,i){var a;n:if(t==`style`){if(typeof n==`string`)e.style.cssText=n;else{if(typeof r==`string`&&(e.style.cssText=r=``),r)for(t in r)n&&t in n||ko(e.style,t,``);if(n)for(t in n)r&&n[t]==r[t]||ko(e.style,t,n[t])}}else if(t[0]==`o`&&t[1]==`n`)a=t!=(t=t.replace(oo,`$1`)),(t=t.slice(2))[0].toLowerCase()!=t[0]&&(t=t.toLowerCase()),e.__l||={},e.__l[t+a]=n,n?r?n.l=r.l:(n.l=so,e.addEventListener(t,a?lo:co,a)):e.removeEventListener(t,a?lo:co,a);else{if(i==`http://www.w3.org/2000/svg`)t=t.replace(/xlink(H|:h)/,`h`).replace(/sName$/,`s`);else if(t!=`width`&&t!=`height`&&t!=`href`&&t!=`list`&&t!=`form`&&t!=`tabIndex`&&t!=`download`&&t!=`rowSpan`&&t!=`colSpan`&&t!=`role`&&t!=`popover`&&t in e)try{e[t]=n??``;break n}catch{}typeof n==`function`||(n==null||!1===n&&t[4]!=`-`?e.removeAttribute(t):e.setAttribute(t,t==`popover`&&n==1?``:n))}}function jo(e){return function(t){if(this.__l){var n=this.__l[t.type+e];if(t.u==null)t.u=so++;else if(t.u<n.l)return;return n(q.event?q.event(t):t)}}}function Mo(e,t,n,r,i,a,o,s,c,l,u){var d,f,p,m,h,g,_,v,y,b,x,S,C,ee,w,T,E,te,ne,re,D,O=t.type;if(t.constructor!=null)return null;128&n.__u&&(c=!!(32&n.__u),n.__c.__z&&(s=t.__e=n.__e=(a=n.__c.__z)[0],n.__c.__z=null)),(d=q.__b)&&d(t);n:if(typeof O==`function`)try{if(v=t.props,y=`prototype`in O&&O.prototype.render,b=(d=O.contextType)&&r[d.__c],x=d?b?b.props.value:d.__:r,n.__c?2&(f=t.__c=n.__c).__g&&(f.__g|=1,_=!0):(y?t.__c=f=new O(v,x):(t.__c=f=new bo(v,x),f.constructor=O,f.render=zo),b&&b.sub(f),f.props=v,f.state||(f.state={}),f.context=x,f.__n=r,p=!0,f.__g|=8,f.__h=[],f._sb=[]),y&&f.__s==null&&(f.__s=f.state),y&&O.getDerivedStateFromProps!=null&&(f.__s==f.state&&(f.__s=ho({},f.__s)),ho(f.__s,O.getDerivedStateFromProps(v,f.__s))),m=f.props,h=f.state,f.__v=t,p)y&&O.getDerivedStateFromProps==null&&f.componentWillMount!=null&&f.componentWillMount(),y&&f.componentDidMount!=null&&f.__h.push(f.componentDidMount);else{if(y&&O.getDerivedStateFromProps==null&&v!==m&&f.componentWillReceiveProps!=null&&f.componentWillReceiveProps(v,x),!(4&f.__g)&&f.shouldComponentUpdate!=null&&!1===f.shouldComponentUpdate(v,f.__s,x)||t.__v==n.__v){for(t.__v!=n.__v&&(f.props=v,f.state=f.__s,f.__g&=-9),t.__e=n.__e,t.__k=n.__k,t.__k.some(function(e){e&&(e.__=t)}),S=0;S<f._sb.length;S++)f.__h.push(f._sb[S]);f._sb=[],f.__h.length&&o.push(f);break n}f.componentWillUpdate!=null&&f.componentWillUpdate(v,f.__s,x),y&&f.componentDidUpdate!=null&&f.__h.push(function(){f.componentDidUpdate(m,h,g)})}if(f.context=x,f.props=v,f.__P=e,f.__g&=-5,C=q.__r,ee=0,y){for(f.state=f.__s,f.__g&=-9,C&&C(t),d=f.render(f.props,f.state,f.context),w=0;w<f._sb.length;w++)f.__h.push(f._sb[w]);f._sb=[]}else do f.__g&=-9,C&&C(t),d=f.render(f.props,f.state,f.context),f.state=f.__s;while(8&f.__g&&++ee<25);f.state=f.__s,f.getChildContext!=null&&(r=ho({},r,f.getChildContext())),y&&!p&&f.getSnapshotBeforeUpdate!=null&&(g=f.getSnapshotBeforeUpdate(m,h)),T=d,d!=null&&d.type===yo&&d.key==null&&(T=Fo(d.props.children)),s=To(e,po(T)?T:[T],t,n,r,i,a,o,s,c,l,u),t.__u&=-161,f.__h.length&&o.push(f),_&&(f.__g&=-4)}catch(e){if(t.__v=null,c||a!=null){if(e.then){for(E=0,te=!1,t.__u|=c?160:128,t.__c.__z=[],ne=0;ne<a.length;ne++)(re=a[ne])==null||te||(re.nodeType==8&&re.data==`$s`?(E>0&&t.__c.__z.push(re),E++,a[ne]=null):re.nodeType==8&&re.data==`/$s`?(--E>0&&t.__c.__z.push(re),te=E===0,s=a[ne],a[ne]=null):E>0&&(t.__c.__z.push(re),a[ne]=null));if(!te){for(;s&&s.nodeType==8&&s.nextSibling;)s=s.nextSibling;a[a.indexOf(s)]=null,t.__c.__z=[s]}t.__e=s}else{for(D=a.length;D--;)go(a[D]);No(t)}}else t.__e=n.__e,t.__k=n.__k,e.then||No(t);q.__e(e,t,n)}else s=t.__e=Io(n.__e,t,n,r,i,a,o,c,l,u);return(d=q.diffed)&&d(t),128&t.__u?void 0:s}function No(e){e&&e.__c&&(e.__c.__g|=4),e&&e.__k&&e.__k.forEach(No)}function Po(e,t,n){for(var r=0;r<n.length;r++)Lo(n[r],n[++r],n[++r]);q.__c&&q.__c(t,e),e.some(function(t){try{e=t.__h,t.__h=[],e.some(function(e){e.call(t)})}catch(e){q.__e(e,t.__v)}})}function Fo(e){return typeof e!=`object`||!e||e.__b&&e.__b>0?e:po(e)?e.map(Fo):ho({},e)}function Io(e,t,n,r,i,a,o,s,c,l){var u,d,f,p,m,h,g,_,v=n.props,y=t.props,b=t.type;if(b==`svg`?i=`http://www.w3.org/2000/svg`:b==`math`?i=`http://www.w3.org/1998/Math/MathML`:i||=`http://www.w3.org/1999/xhtml`,a!=null){for(u=0;u<a.length;u++)if((m=a[u])&&`setAttribute`in m==!!b&&(b?m.localName==b:m.nodeType==3)){e=m,a[u]=null;break}}if(e==null){if(b==null)return l.createTextNode(y);e=l.createElementNS(i,b,y.is&&y),s&&=(q.__m&&q.__m(t,a),!1),a=null}if(b==null)v===y||s&&e.data==y||(e.data=y);else{if(a&&=mo.call(e.childNodes),v=n.props||uo,!s&&a!=null)for(v={},u=0;u<e.attributes.length;u++)v[(m=e.attributes[u]).name]=m.value;for(u in v)if(m=v[u],u!=`children`){if(u==`dangerouslySetInnerHTML`)f=m;else if(!(u in y)){if(u==`value`&&`defaultValue`in y||u==`checked`&&`defaultChecked`in y)continue;Ao(e,u,null,m,i)}}for(u in _=1&n.__u,y)m=y[u],u==`children`?p=m:u==`dangerouslySetInnerHTML`?d=m:u==`value`?h=m:u==`checked`?g=m:s&&typeof m!=`function`||v[u]===m&&!_||Ao(e,u,m,v[u],i);if(d)s||f&&(d.__html==f.__html||d.__html==e.innerHTML)||(e.innerHTML=d.__html),t.__k=[];else if(f&&(e.innerHTML=``),To(b==`template`?e.content:e,po(p)?p:[p],t,n,r,b==`foreignObject`?`http://www.w3.org/1999/xhtml`:i,a,o,a?a[0]:n.__k&&xo(n,0),s,c,l),a!=null)for(u=a.length;u--;)go(a[u]);s||(u=`value`,b==`progress`&&h==null?e.removeAttribute(`value`):h==null||h===e[u]&&(b!==`progress`||h)||Ao(e,u,h,v[u],i),u=`checked`,g!=null&&g!=e[u]&&Ao(e,u,g,v[u],i))}return e}function Lo(e,t,n){try{if(typeof e==`function`){var r=typeof e.__u==`function`;r&&e.__u(),r&&t==null||(e.__u=e(t))}else e.current=t}catch(e){q.__e(e,n)}}function Ro(e,t,n){var r,i;if(q.unmount&&q.unmount(e),(r=e.ref)&&(r.current&&r.current!=e.__e||Lo(r,null,t)),(r=e.__c)!=null){if(r.componentWillUnmount)try{r.componentWillUnmount()}catch(e){q.__e(e,t)}r.__P=null}if(r=e.__k)for(i=0;i<r.length;i++)r[i]&&Ro(r[i],t,n||typeof e.type!=`function`);n||go(e.__e),e.__e&&e.__e.__l&&(e.__e.__l=null),e.__e=e.__c=e.__=null}function zo(e,t,n){return this.constructor(e,n)}function Bo(e,t){var n,r,i,a;t==document&&(t=document.documentElement),q.__&&q.__(e,t),r=(n=!!(e&&32&e.__u))?null:t.__k,e=t.__k=_o(yo,null,[e]),i=[],a=[],Mo(t,e,r||uo,uo,t.namespaceURI,r?null:t.firstChild?mo.call(t.childNodes):null,i,r?r.__e:t.firstChild,n,a,t.ownerDocument),Po(i,e,a)}function Vo(e,t){e.__u|=32,Bo(e,t)}q={__e:function(e,t,n,r){for(var i,a,o;t=t.__;)if((i=t.__c)&&!(1&i.__g)){i.__g|=4;try{if((a=i.constructor)&&a.getDerivedStateFromError!=null&&(i.setState(a.getDerivedStateFromError(e)),o=8&i.__g),i.componentDidCatch!=null&&(i.componentDidCatch(e,r||{}),o=8&i.__g),o)return void(i.__g|=2)}catch(t){e=t}}throw io=0,e}},to=0,bo.prototype.setState=function(e,t){var n=this.__s!=null&&this.__s!=this.state?this.__s:this.__s=ho({},this.state);typeof e==`function`&&(e=e(ho({},n),this.props)),e&&ho(n,e),e!=null&&this.__v&&(t&&this._sb.push(t),Co(this))},bo.prototype.forceUpdate=function(e){this.__v&&(this.__g|=4,e&&this.__h.push(e),Co(this))},bo.prototype.render=yo,no=[],io=0,ao=function(e,t){return e.__v.__b-t.__v.__b},oo=/(PointerCapture)$|Capture$/i,so=0,co=jo(!1),lo=jo(!0);var Ho=0;Array.isArray;function J(e,t,n,r,i,a){t||={};var o,s,c=t;if(`ref`in c&&typeof e!=`function`)for(s in c={},t)s==`ref`?o=t[s]:c[s]=t[s];var l={type:e,props:c,key:n,ref:o,__k:null,__:null,__b:0,__e:null,__c:null,constructor:void 0,__v:--Ho,__i:-1,__u:0,__source:i,__self:a};return q.vnode&&q.vnode(l),l}var Uo=16,Wo=16,Go={};function Ko({name:e,remappedFrom:t,token:n,width:r,height:i,viewBox:a,label:o,alignCapitals:s=!1}){"use no memo";let c=`#${e.replace(/^#/,``)}`,{width:l,height:u,viewBox:d}=Go[e]??{width:Uo,height:Wo},f=r??l,p=i??u,m=a??d??`0 0 ${l} ${u}`;return J(`svg`,{"data-icon-name":t??e,"data-icon-token":n,"data-align-capitals":s,...o==null?{"aria-hidden":!0}:{"aria-label":o,role:`img`},viewBox:m,width:f,height:p,children:J(`use`,{href:c})})}var qo=e=>{if(e.length<2)return[e,``];let t=Math.ceil(e.length/2);return[e.slice(0,t),e.slice(t)]},Jo=e=>{if(e.length<4)return[e,``];let t=e.lastIndexOf(`.`)+1,n=e.length-t>10,r=t>=1&&!n?t:Math.ceil(e.length/2);return[e.slice(0,r),e.slice(r)]},Yo=e=>{if(e.length<4)return[e,``];let t=e.lastIndexOf(`/`)+1,n=e.length-t>25,r=t>=1&&!n?t:Math.ceil(e.length/2);return[e.slice(0,r),e.slice(r)]},Xo=(e,{splitIndex:t}={})=>{if(typeof t!=`number`){let t=Math.ceil(e.length/2);return[e.slice(0,t),e.slice(t)]}return[e.slice(0,t),e.slice(t)]},Zo=(e,{splitOffset:t}={})=>{if(typeof t!=`number`||t<=0||t>=e.length){let t=Math.ceil(e.length/2);return[e.slice(0,t),e.slice(t)]}let n=e.length-t;return[e.slice(0,n),e.slice(n)]},Qo=(e,{splitOffset:t}={})=>{if(typeof t!=`number`||t<=0||t>=e.length){let t=Math.ceil(e.length/2);return[e.slice(0,t),e.slice(t)]}let n=t;return[e.slice(0,n),e.slice(n)]};function $o({children:e,marker:t,variant:n=`default`}){"use no memo";return J(`div`,{"aria-hidden":!0,"data-truncate-marker-cell":!0,children:J(`div`,{"data-truncate-marker":!0,children:typeof t==`function`?t({children:e}):n===`fade`?J(`span`,{"data-truncate-fade":!0}):t})})}function es(e){"use no memo";let{mode:t,children:n}=e;return J(`div`,{children:[J(`div`,{"data-truncate-content":`visible`,children:t===`fruncate`?J(`span`,{children:n}):n}),J(`div`,{"data-truncate-content":`overflow`,"aria-hidden":!0,children:t===`fruncate`?J(`span`,{children:n}):n})]})}function ts({children:e,mode:t=`truncate`,marker:n=`…`,variant:r=`default`,...i}){"use no memo";let a=J(es,{mode:t,children:e},`content`),o=J($o,{marker:n,mode:t,variant:r},`marker`),s=J(`div`,{"data-truncate-fill":!0},`fill`);return J(`div`,{"data-truncate-container":t,"data-truncate-variant":r,...i,children:J(`div`,{"data-truncate-grid":!0,children:t===`truncate`?[a,o]:[o,a,s]})})}function ns({children:e,...t}){"use no memo";return J(ts,{mode:`truncate`,...t,children:e})}function rs({children:e,...t}){"use no memo";return J(ts,{mode:`fruncate`,...t,children:e})}function is({children:e,contents:t,priority:n=`end`,split:r=`center`,minimumLength:i=12,className:a,style:o,...s}){"use no memo";let c=null,l=null;if(Array.isArray(t)){if(t.length!==2)return console.error(`MiddleTruncate: contents must be an array of two items`),null;c=J(ns,{...s,children:t[0]}),l=J(rs,{...s,children:t[1]})}else{if(typeof e!=`string`)return console.error(`MiddleTruncate: children must be a string`),null;if(e.length===0)return J(`div`,{className:a,style:o});if(e.length<i)return J(n===`end`?rs:ns,{...s,className:a,style:o,children:e});let t=null,u=null,d=null;if(typeof r==`string`)r===`center`?t=qo:r===`extension`?t=Jo:r===`leaf-path`&&(t=Yo);else if(typeof r==`number`)t=Xo,u=r;else if(Array.isArray(r)){let[e,n]=r;d=n,e===`last`?t=Zo:e===`first`&&(t=Qo)}else typeof r==`function`&&(t=r);t??=qo;let[f,p]=t(e,{priority:n,variant:s.variant,splitIndex:typeof u==`number`?u:void 0,splitOffset:typeof d==`number`?d:void 0}),m=f.length>=p.length,h=n===`equal`&&!m,g=n===`equal`&&m,_={},v={};h&&(_.marker=``),g&&(v.marker=``),c=J(ns,{...s,..._,children:f}),l=J(rs,{...s,...v,children:p})}return J(`div`,{"data-truncate-group-container":`middle`,className:a,style:o,children:[J(`div`,{"data-truncate-segment-priority":n===`start`||n===`equal`?`1`:`2`,children:c}),J(`div`,{"data-truncate-segment-priority":n===`end`||n===`equal`?`1`:`2`,children:l})]})}var as={endIndex:-1,startIndex:-1};function os(e,t,n){return Math.min(Math.max(e,t),n)}function ss(e,t){return e<0||t<e?as:{endIndex:t,startIndex:e}}function cs(e){return e.startIndex<0||e.endIndex<e.startIndex}function ls(e,t){return cs(e)?0:(e.endIndex-e.startIndex+1)*t}function us(e,t,n){if(t<=0)return-1;let r=t*n;return e<=0?0:e>=r?t:Math.floor(e/n)}function ds(e,t,n){return t<=0||e<=0?-1:e>=t*n?t-1:Math.ceil(e/n)-1}function fs(e){let t=new Map;return e.forEach((e,n)=>{if(e.kind!==`directory`||!e.isExpanded)return;let r=e.ancestorPaths.length,i=t.get(r);if(i==null){t.set(r,[n]);return}i.push(n)}),t}function ps(e,t){let n=0,r=e.length-1,i=-1;for(;n<=r;){let a=Math.floor((n+r)/2),o=e[a];if(o==null)break;if(o<=t){i=a,n=a+1;continue}r=a-1}return i}function ms(e){let t=new Map,n=[];for(let r=0;r<e.length;r+=1){let i=e[r];if(i==null)continue;let a=i.kind===`directory`&&i.isExpanded?[...i.ancestorPaths,i.path]:i.ancestorPaths,o=0;for(;o<n.length&&o<a.length&&n[o]===a[o];)o+=1;for(let e=n.length-1;e>=o;--e){let i=n[e];i!=null&&t.set(i,r-1)}n.length=o;for(let e=o;e<a.length;e+=1){let t=a[e];t!=null&&n.push(t)}}let r=e.length-1;for(let e of n)t.set(e,r);return t}function hs(e,t,n){if(e.length===0||t<=0)return[];let r=ms(e),i=fs(e),a=[];for(let r=0;r<e.length;r+=1){let o=i.get(r);if(o==null||o.length===0)break;let s=t+r*n,c=ps(o,Math.min(e.length-1,Math.floor(s/n))),l=null;for(;c>=0;){let t=o[c],n=t==null?null:e[t]??null;if(n!=null&&(r===0||n.ancestorPaths[r-1]===a[r-1]?.path)){l=n;break}--c}if(l==null)break;a.push(l)}return a.map((i,a)=>{let o=a*n,s=(r.get(i.path)??e.length-1)+1;if(s>=e.length)return{row:i,top:o};let c=s*n-t;return{row:i,top:Math.min(o,c-n)}}).filter(e=>e.top+n>0)}function gs(e,t){let n=t.totalRowCount??e.length,r=n*t.itemHeight,i=Math.max(0,t.viewportHeight),a=Math.max(0,Math.floor(t.overscan)),o=Math.max(0,r-i),s=os(t.scrollTop,0,o),c=t.stickyRows??hs(e,s,t.itemHeight),l=c.reduce((e,n)=>Math.max(e,n.top+t.itemHeight),0),u=Math.min(r,s+l),d=Math.max(0,i-l),f=Math.max(0,r-u),p=us(s,n,t.itemHeight),m=us(u,n,t.itemHeight),h=l<=0||p<0||p>=n?-1:p,g=h===-1?-1:Math.min(n-1,m-1),_=h===-1||g<h?0:g-h+1,v=d<=0||m>=n?as:ss(m,ds(u+d,n,t.itemHeight)),y=g+1,b=cs(v)?as:ss(Math.max(y,v.startIndex-a),Math.min(n-1,v.endIndex+a)),x=ls(b,t.itemHeight);return{occlusion:{firstOccludedIndex:h,lastOccludedIndex:g,occludedCount:_},physical:{itemHeight:t.itemHeight,maxScrollTop:o,overscan:a,scrollTop:s,totalHeight:r,totalRowCount:n,viewportHeight:i},projected:{contentHeight:f,paneHeight:d,paneTop:u},sticky:{height:l,rows:c},visible:v,window:{endIndex:b.endIndex,height:x,offsetTop:cs(b)?0:b.startIndex*t.itemHeight,startIndex:b.startIndex}}}var _s={added:`A`,deleted:`D`,ignored:null,modified:`M`,renamed:`R`,untracked:`U`},vs={added:`Git status: added`,deleted:`Git status: deleted`,ignored:`Git status: ignored`,modified:`Git status: modified`,renamed:`Git status: renamed`,untracked:`Git status: untracked`},ys=`Contains git status items`;function bs(e){let{currentScrollTop:t,focusedIndex:n,itemHeight:r,topInset:i=0,viewportHeight:a}=e;if(n<0)return null;let o=Math.max(0,i),s=n*r,c=s+r;if(s<t+o){let e=Math.max(0,s-o);return e===t?null:e}if(c>t+a){let e=c-a;return e===t?null:e}return null}function xs(e){let{currentScrollTop:t,focusedIndex:n,itemHeight:r,offset:i,topInset:a=0,totalHeight:o,viewportHeight:s}=e;if(i===`nearest`)return bs({currentScrollTop:t,focusedIndex:n,itemHeight:r,topInset:a,viewportHeight:s});if(n<0)return null;let c=Math.max(0,a),l=n*r,u=Math.max(0,s-c),d=i===`center`?c+Math.max(0,(u-r)/2):c,f=Math.max(0,o-s),p=Math.max(0,Math.min(l-d,f));return p===t?null:p}function Ss(e){let{currentScrollTop:t,focusedIndex:n,itemHeight:r,targetViewportOffset:i,totalHeight:a,viewportHeight:o}=e;if(n<0)return null;let s=Math.max(0,i),c=n*r,l=c+r,u=t+s,d=t+o;if(c>=u&&l<=d)return null;let f=Math.max(0,a-o),p=Math.max(0,Math.min(c-s,f));return p===t?null:p}function Cs(e){if(e==null||!e.isConnected||e===document.body||e===document.documentElement)return!1;e.focus({preventScroll:!0});let t=e.getRootNode();return t instanceof ShadowRoot?t.activeElement===e:document.activeElement===e}function ws(e){let t=e.getRootNode();if(t instanceof ShadowRoot){let e=t.activeElement;return e instanceof HTMLElement?e:null}let n=document.activeElement;return n instanceof HTMLElement&&e.contains(n)?n:null}function Ts(e,t){if(e==null)return t;let n=e.getBoundingClientRect().height;return n>0?n:e.clientHeight>0?e.clientHeight:t}function Es(e,t){return e!=null&&e>0?e:t}function Ds(e){let t=e.borderBoxSize,n=Array.isArray(t)?t[0]:t;return n!=null&&Number.isFinite(n.blockSize)&&n.blockSize>0?n.blockSize:e.contentRect.height>0?e.contentRect.height:null}function Os(e,t,n,r,i=0){let a=bs({currentScrollTop:e.scrollTop,focusedIndex:t,itemHeight:n,topInset:i,viewportHeight:r});return a!=null&&(e.scrollTop=a,!0)}function ks(e,t,n,r,i,a,o=0){let s=xs({currentScrollTop:e.scrollTop,focusedIndex:t,itemHeight:n,offset:a,topInset:o,totalHeight:i,viewportHeight:r});return s!=null&&(e.scrollTop=s,!0)}function As(e,t,n,r,i,a){let o=Ss({currentScrollTop:e.scrollTop,focusedIndex:t,itemHeight:n,targetViewportOffset:a,totalHeight:i,viewportHeight:r});return o!=null&&(e.scrollTop=o,!0)}function js(e,t,n,r){return n.end<n.start?null:e<n.start?-t:e>n.end?r:null}function Ms(e){let{renamingPath:t,previousRenamingPath:n,hasRenderedInput:r}=e;return t==null?`reset`:r?n===t?`ignore`:`focus-input`:`reveal-canonical`}function Ns({ariaLabel:e,isFlattened:t=!1,ref:n,value:r,onBlur:i,onInput:a}){return J(`input`,{ref:n,"data-item-rename-input":!0,...t?{"data-item-flattened-rename-input":!0}:{},"aria-label":e,value:r,onBlur:i,onInput:a,onClick:e=>e.stopPropagation(),onMouseDown:e=>e.stopPropagation(),onPointerDown:e=>e.stopPropagation()})}function Ps(e){let{row:t,mode:n,targetPath:r,ariaLabel:i,domId:a,isParked:o,itemHeight:s,features:c,state:l,extraStyle:u}=e,d=n===`sticky`,f=t.ancestorPaths.at(-1)??``,p={};return l.isFocusRinged&&(p[`data-item-focused`]=!0),t.isSelected&&(p[`data-item-selected`]=!0),l.isContextHovered&&(p[`data-item-context-hover`]=`true`),l.isDragTarget&&(p[`data-item-drag-target`]=!0),l.isDragging&&(p[`data-item-dragging`]=!0),l.effectiveGitStatus!=null&&(p[`data-item-git-status`]=l.effectiveGitStatus),l.containsGitChange&&(p[`data-item-contains-git-change`]=`true`),{"aria-expanded":!d&&t.kind===`directory`?t.isExpanded:void 0,"aria-haspopup":c.contextMenuEnabled?`menu`:void 0,"aria-label":i,"aria-level":d?void 0:t.level+1,"aria-posinset":d?void 0:t.posInSet+1,"aria-selected":d?void 0:t.isSelected?`true`:`false`,"aria-setsize":d?void 0:t.setSize,"data-file-tree-sticky-path":d?r:void 0,"data-file-tree-sticky-row":d?`true`:void 0,"data-item-context-menu-button-visibility":c.actionLaneEnabled?c.contextMenuButtonVisibility:void 0,"data-item-context-menu-trigger-mode":c.contextMenuEnabled?c.contextMenuTriggerMode:void 0,"data-item-has-context-menu-action-lane":c.actionLaneEnabled?`true`:void 0,"data-item-has-git-lane":c.gitLaneActive?`true`:void 0,"data-item-parent-path":f.length>0?f:void 0,"data-item-parked":o?`true`:void 0,"data-item-path":r,"data-item-type":t.kind===`directory`?`folder`:`file`,"data-type":`item`,id:d?void 0:a,role:d?void 0:`treeitem`,style:{minHeight:`${s}px`,...u},tabIndex:!d&&t.isFocused?0:-1,...p}}function Fs(e){let{event:t,mode:n,isSearchOpen:r,isDirectory:i}=e,a=t.ctrlKey||t.metaKey,o=t.shiftKey||a,s=t.shiftKey?{additive:a,kind:`range`}:a?{kind:`toggle`}:{kind:`single`};return{closeSearch:r,revealCanonical:n===`sticky`,selection:s,toggleDirectory:!o&&i}}var Is,Y,Ls,Rs,zs=Object.is,Bs=0,Vs=[],X=q,Hs=X.__b,Us=X.__r,Ws=X.diffed,Gs=X.__c,Ks=X.unmount,qs=X.__;function Js(e,t){X.__h&&X.__h(Y,e,Bs||t),Bs=0;var n=Y.__H||(Y.__H={__:[],__h:[]});return e>=n.__.length&&n.__.push({}),n.__[e]}function Ys(e){return Bs=1,Xs(oc,e)}function Xs(e,t,n){var r=Js(Is++,2);if(r.t=e,!r.__c&&(r.__=[n?n(t):oc(void 0,t),function(e){var t=r.__N?r.__N[0]:r.__[0],n=r.t(t,e);zs(t,n)||(r.__N=[n,r.__[1]],r.__c.setState({}))}],r.__c=Y,!Y.__f)){var i=function(e,t,n){if(!r.__c.__H)return!0;var i=r.__c.__H.__.filter(function(e){return!!e.__c});if(i.every(function(e){return!e.__N}))return!a||a.call(this,e,t,n);var o=r.__c.props!==e;return i.forEach(function(e){if(e.__N){var t=e.__[0];e.__=e.__N,e.__N=void 0,zs(t,e.__[0])||(o=!0)}}),a&&a.call(this,e,t,n)||o};Y.__f=!0;var a=Y.shouldComponentUpdate,o=Y.componentWillUpdate;Y.componentWillUpdate=function(e,t,n){if(4&this.__g){var r=a;a=void 0,i(e,t,n),a=r}o&&o.call(this,e,t,n)},Y.shouldComponentUpdate=i}return r.__N||r.__}function Zs(e,t){var n=Js(Is++,3);!X.__s&&ac(n.__H,t)&&(n.__=e,n.u=t,Y.__H.__h.push(n))}function Qs(e,t){var n=Js(Is++,4);!X.__s&&ac(n.__H,t)&&(n.__=e,n.u=t,Y.__h.push(n))}function Z(e){return Bs=5,$s(function(){return{current:e}},[])}function $s(e,t){var n=Js(Is++,7);return ac(n.__H,t)&&(n.__=e(),n.__H=t,n.__h=e),n.__}function Q(e,t){return Bs=8,$s(function(){return e},t)}function ec(){for(var e;e=Vs.shift();)if(e.__P&&e.__H)try{e.__H.__h.forEach(rc),e.__H.__h.forEach(ic),e.__H.__h=[]}catch(t){e.__H.__h=[],X.__e(t,e.__v)}}X.__b=function(e){Y=null,Hs&&Hs(e)},X.__=function(e,t){e&&t.__k&&t.__k.__m&&(e.__m=t.__k.__m),qs&&qs(e,t)},X.__r=function(e){Us&&Us(e),Is=0;var t=(Y=e.__c).__H;t&&(Ls===Y?(t.__h=[],Y.__h=[],t.__.forEach(function(e){e.__N&&(e.__=e.__N),e.u=e.__N=void 0})):(t.__h.forEach(rc),t.__h.forEach(ic),t.__h=[],Is=0)),Ls=Y},X.diffed=function(e){Ws&&Ws(e);var t=e.__c;t&&t.__H&&(t.__H.__h.length&&(Vs.push(t)!==1&&Rs===X.requestAnimationFrame||((Rs=X.requestAnimationFrame)||nc)(ec)),t.__H.__.forEach(function(e){e.u&&(e.__H=e.u),e.u=void 0})),Ls=Y=null},X.__c=function(e,t){t.some(function(e){try{e.__h.forEach(rc),e.__h=e.__h.filter(function(e){return!e.__||ic(e)})}catch(n){t.some(function(e){e.__h&&=[]}),t=[],X.__e(n,e.__v)}}),Gs&&Gs(e,t)},X.unmount=function(e){Ks&&Ks(e);var t,n=e.__c;n&&n.__H&&(n.__H.__.forEach(function(e){try{rc(e)}catch(e){t=e}}),n.__H=void 0,t&&X.__e(t,n.__v))};var tc=typeof requestAnimationFrame==`function`;function nc(e){var t,n=function(){clearTimeout(r),tc&&cancelAnimationFrame(t),setTimeout(e)},r=setTimeout(n,35);tc&&(t=requestAnimationFrame(n))}function rc(e){var t=Y,n=e.__c;typeof n==`function`&&(e.__c=void 0,n()),Y=t}function ic(e){var t=Y;e.__c=e.__(),Y=t}function ac(e,t){return!e||e.length!==t.length||t.some(function(t,n){return!zs(t,e[n])})}function oc(e,t){return typeof t==`function`?t(e):t}function sc(e,t=null,n=null){"use no memo";let r=e.flattenedSegments;return r==null||r.length===0?t??e.name:J(`span`,{"data-item-flattened-subitems":!0,children:r.map((e,i)=>{let a=i===r.length-1;return J(yo,{children:[J(`span`,{"data-item-flattened-subitem":e.path,"data-item-flattened-subitem-drag-target":n===e.path?`true`:void 0,children:a&&t!=null?t:J(ns,{children:e.name})}),i<r.length-1?` / `:``]},e.path)})})}function cc(e){return e.isFlattened?e.flattenedSegments?.findLast(e=>e.isTerminal)?.path??e.path:e.path}function lc(e){let t=e.flattenedSegments;return t==null||t.length===0?e.name:t.map(e=>e.name).join(` / `)}function uc(e,t,n,r){return e.map((e,i)=>{let a=i*n,o=e.subtreeEndIndex+1;if(o>=r)return{row:e.row,top:a};let s=o*n-t;return{row:e.row,top:Math.min(a,s-n)}}).filter(e=>e.top+n>0)}function dc({controller:e,itemHeight:t,overscan:n,scrollTop:r,stickyFolders:i,viewportHeight:a}){let o=e.getVisibleCount(),s=i&&o>0?e.getStickyRowCandidates(r,t):[],c=s==null&&i&&o>0?e.getVisibleRows(0,o-1):[],l=gs(c,{itemHeight:t,overscan:n,scrollTop:r,stickyRows:s==null?void 0:uc(s,r,t,o),totalRowCount:o,viewportHeight:a}),u=i&&r<=0&&o>0?e.getStickyRowCandidates(1,t):[],d=u!=null&&r<=0?uc(u,1,t,o):i&&r<=0&&c.length>0?hs(c,1,t):l.sticky.rows;return{overlayHeight:d.reduce((e,n)=>Math.max(e,n.top+t),0),overlayRows:d,snapshot:l,visibleRows:c}}var fc=400,pc=40,mc=18;function hc(e,t,n){let r=e,i=document.elementFromPoint?.bind(document)??null,a=r.elementFromPoint?.(t,n)??i?.(t,n)??null;return e instanceof ShadowRoot&&(a==null||!e.contains(a))?gc(e,t,n):a instanceof HTMLElement?a:null}function gc(e,t,n){let r=Array.from(e.querySelectorAll(`[data-type="item"], [data-item-flattened-subitem]`));for(let e=r.length-1;e>=0;e--){let i=r[e],a=i.getBoundingClientRect();if(t>=a.left&&t<=a.right&&n>=a.top&&n<=a.bottom)return i}return null}function _c(e){let t=e?.closest?.(`[data-type="item"]`);if(!(t instanceof HTMLElement))return null;let n=t.dataset.itemPath??null;if(n==null)return null;let r=e?.closest?.(`[data-item-flattened-subitem]`),i=r instanceof HTMLElement?r.getAttribute(`data-item-flattened-subitem`)??null:null;if(i!=null&&i.endsWith(`/`))return{directoryPath:i,flattenedSegmentPath:i,hoveredPath:n,kind:`directory`};if(t.dataset.itemType===`folder`)return{directoryPath:n,flattenedSegmentPath:null,hoveredPath:n,kind:`directory`};let a=t.dataset.itemParentPath??null;return a==null||a.length===0?{directoryPath:null,flattenedSegmentPath:null,hoveredPath:n,kind:`root`}:{directoryPath:a,flattenedSegmentPath:null,hoveredPath:n,kind:`directory`}}function vc(e){let t=e.cloneNode(!0);return t.removeAttribute(`id`),t.dataset.fileTreeDragPreview=`true`,t.setAttribute(`aria-hidden`,`true`),t.tabIndex=-1,Object.assign(t.style,{boxShadow:`0 4px 12px rgba(0, 0, 0, 0.15)`,left:`0px`,margin:`0`,pointerEvents:`none`,position:`fixed`,top:`0px`,willChange:`transform`,zIndex:`10000`}),t}function yc(){return navigator.vendor!==`Apple Computer, Inc.`}function bc(e,t){let n=e-t.top;if(n<pc)return-Math.ceil((pc-Math.max(0,n))/pc*mc);let r=t.bottom-e;return r<pc?Math.ceil((pc-Math.max(0,r))/pc*mc):0}function xc(e,t){if(e!=null){let t=_s[e];return t==null?null:{text:t,title:vs[e]}}return t?{icon:{name:`file-tree-icon-dot`,width:6,height:6},title:ys}:null}function Sc(e,t,n){if(t==null||t.size===0)return null;let r=[];for(let i=e.length-1;i>=0;--i){let a=e[i],o=n.get(a);if(o!=null){for(let e of r)n.set(e,o);return o?`ignored`:null}if(t.has(a)){n.set(a,!0);for(let e of r)n.set(e,!0);return`ignored`}r.push(a)}for(let e of r)n.set(e,!1);return null}function Cc(e){return e!=null&&`toggle`in e}function wc(e){return e.code===`Space`||e.key===` `||e.key===`Spacebar`}function Tc(e){return e.key.length===1&&/^[\p{L}\p{N}]$/u.test(e.key)&&!e.ctrlKey&&!e.metaKey&&!e.altKey}function Ec(e){return e==null?``:`[data-item-section="spacing-item"][data-ancestor-path="${e.replaceAll(`\\`,`\\\\`).replaceAll(`"`,`\\"`)}"] { opacity: 1; }`}function Dc(e){return e.shiftKey&&e.key===`F10`||e.key===`ContextMenu`}function Oc(e,t){return t&&Dc(e)||(e.ctrlKey||e.metaKey)&&wc(e)?!0:e.key===`ArrowDown`||e.key===`ArrowLeft`||e.key===`ArrowRight`||e.key===`ArrowUp`}var kc=new Set([`ArrowDown`,`ArrowLeft`,`ArrowRight`,`ArrowUp`,`End`,`Home`,`PageDown`,`PageUp`]);function Ac(e){for(let t of e.composedPath())if(t instanceof HTMLElement&&(t.dataset.fileTreeContextMenuRoot===`true`||t.dataset.type===`context-menu-anchor`||t.dataset.type===`context-menu-trigger`||t.getAttribute(`slot`)===`context-menu`))return!0;return!1}function jc(e){return{bottom:e.bottom,height:e.height,left:e.left,right:e.right,top:e.top,width:e.width,x:e.x,y:e.y}}function Mc(e,t){return{bottom:t,height:0,left:e,right:e,top:t,width:0,x:e,y:t}}function Nc(e,t){if(e==null)return t.offsetTop;let n=t.getBoundingClientRect(),r=e.getBoundingClientRect();return n.top-r.top}function Pc(e,t,n){if(n==null){e.delete(t);return}e.set(t,n)}function Fc(e,t,n){if(e==null)return null;let r=t.get(e)??null;if(r!=null)return r;let i=n.get(e)??null;return i?.dataset.itemParked===`true`?null:i}function Ic(e){if(e==null)return[];let t=[];for(let n of e.querySelectorAll(`button[data-file-tree-sticky-row="true"]`)){if(!(n instanceof HTMLElement))continue;let e=n.dataset.fileTreeStickyPath;e!=null&&t.push(e)}return t}function Lc(e,t){if(e==null||t==null)return null;for(let n of e.querySelectorAll(`button[data-item-focused="true"][data-item-parked="true"]`))if(n instanceof HTMLElement&&n.dataset.itemPath===t)return n;return null}function Rc(e,t,n,r,i,a,o){let s=Math.max(0,a-i),c=t?.getBoundingClientRect()??null,l=c==null||n==null?null:n.getBoundingClientRect().top-c.top,u=Lc(e,r),d=c==null||u==null?null:u.getBoundingClientRect().top-c.top;return Math.max(0,Math.min(d??Math.max(l??0,s),Math.max(0,o-i)))}function zc(e,t){return{kind:e.kind,name:lc(e),path:t}}function Bc(e){return e==null?void 0:`${e}__tree`}function Vc(e,t,n){if(e!=null)return`${e}__focused-item-${encodeURIComponent(t)}${n?`__parked`:``}`}function Hc(e){return e===`file-tree-icon-chevron`||e===`file-tree-icon-dot`||e===`file-tree-icon-file`||e===`file-tree-icon-lock`}function Uc(e,t){if(e==null)return null;if(`text`in e)return J(`span`,{title:e.title,children:e.text});let n=typeof e.icon==`string`?Hc(e.icon)?t(e.icon):{name:e.icon}:Hc(e.icon.name)?(()=>{let n=t(e.icon.name),{name:r,...i}=e.icon;return{...n,...i}})():e.icon;return J(`span`,{title:e.title,children:J(Ko,{...n})})}function Wc(e){e!=null&&Cs(e.querySelector([`button:not([disabled])`,`[href]`,`input:not([disabled])`,`select:not([disabled])`,`textarea:not([disabled])`,`[tabindex]:not([tabindex="-1"])`].join(`, `))??e)}function Gc(e,t,{actionLaneEnabled:n=!1,customDecoration:r=null,decorationLaneEnabled:i=!1,dragTargetFlattenedSegmentPath:a=null,gitDecoration:o=null,gitLaneActive:s=!1,renameInput:c=null,showDecorativeActionAffordance:l=!1}={}){let u=cc(e);return J(yo,{children:[e.depth>0?J(`div`,{"data-item-section":`spacing`,children:Array.from({length:e.depth}).map((t,n)=>J(`div`,{"data-item-section":`spacing-item`,"data-ancestor-path":e.ancestorPaths[n]},n))}):null,J(`div`,{"data-item-section":`icon`,children:e.kind===`directory`?J(Ko,{...t(`file-tree-icon-chevron`)}):J(Ko,{...t(`file-tree-icon-file`,u)})}),J(`div`,{"data-item-section":`content`,children:e.isFlattened?sc(e,c,a):c??J(is,{minimumLength:5,split:`extension`,children:e.name})}),i?J(`div`,{"data-item-section":`decoration`,children:r==null?null:Uc(r,t)}):null,s?J(`div`,{"data-item-section":`git`,children:Uc(o,t)}):null,n?J(`div`,{"data-item-section":`action`,children:l?J(`span`,{"aria-hidden":`true`,"data-item-action-affordance":`decorative`,children:J(Ko,{...t(`file-tree-icon-ellipsis`)})}):null}):null]})}function Kc(e,t,n,r={}){let{controller:i,renameView:a,visualFocusPath:o,contextHoverPath:s,draggedPathSet:c,dragTarget:l,dragAndDropEnabled:u,shouldSuppressContextMenu:d,handleRowDragStart:f,handleRowDragEnd:p,handleRowTouchStart:m,instanceId:h,itemHeight:g,gitStatusByPath:_,ignoredGitDirectories:v,ignoredInheritanceCache:y,directoriesWithGitChanges:b,gitLaneActive:x,contextMenuEnabled:S,contextMenuTriggerMode:C,contextMenuButtonTriggerEnabled:ee,contextMenuButtonVisibility:w,contextMenuRightClickEnabled:T,registerRenameInput:E,registerButton:te,resolveIcon:ne,renderDecorationForRow:re,openContextMenuForRow:D,onRowClick:O,onKeyDown:ie}=e,k=cc(t),{isParked:A=!1,mode:ae=`flow`,style:oe}=r,se=ae===`sticky`,ce=_?.get(k)??null??Sc(t.ancestorPaths,v,y),le=t.kind===`directory`&&(b?.has(k)??!1),ue=re(t,k),de=xc(ce,le),fe=S&&ee,pe=ue!=null||x||fe,me=fe&&w===`always`,he=a.getPath()===k,ge=he?a.getValue():``,_e=se||!he?null:J(Ns,{ref:E,ariaLabel:`Rename ${lc(t)}`,isFlattened:t.isFlattened,value:ge,onBlur:()=>{a.commit()},onInput:e=>{a.setValue(e.currentTarget.value)}}),j=Gc(t,ne,{actionLaneEnabled:fe,customDecoration:ue,decorationLaneEnabled:pe,dragTargetFlattenedSegmentPath:l?.flattenedSegmentPath??null,gitDecoration:de,gitLaneActive:x,renameInput:_e,showDecorativeActionAffordance:me}),ve={...Ps({ariaLabel:lc(t),domId:t.isFocused?Vc(h,k,A):void 0,extraStyle:oe,features:{actionLaneEnabled:fe,contextMenuButtonVisibility:fe?w:null,contextMenuEnabled:S,contextMenuTriggerMode:S?C:null,gitLaneActive:x},isParked:A,itemHeight:g,mode:ae,row:t,state:{containsGitChange:le,effectiveGitStatus:ce,isContextHovered:s===k,isDragTarget:l?.kind===`directory`&&l.directoryPath===k,isDragging:c?.has(k)===!0,isFocusRinged:t.isFocused&&o===k},targetPath:k}),key:n,onContextMenu:S||u?e=>{if(d()){e.preventDefault();return}S&&(e.preventDefault(),T&&(i.focusMountedPathFromInput(k),D(t,k,{anchorRect:Mc(e.clientX,e.clientY),source:`right-click`})))}:void 0,onFocus:se?void 0:()=>{i.focusMountedPathFromInput(k)},onKeyDown:se?void 0:ie,ref:e=>{te(k,e)}};return!se&&he?J(`div`,{...ve,children:j}):J(`button`,{...ve,type:`button`,draggable:u&&!A,onDragEnd:u&&!A?p:void 0,onDragStart:u&&!A?e=>{f(e,t,k)}:void 0,onMouseDown:e=>{if(se){e.preventDefault();return}i.isSearchOpen()&&e.preventDefault()},onTouchStart:u&&!A?e=>{m(e,t,k)}:void 0,onClick:e=>{O(e,t,k,ae)},children:j})}function qc(e,t,n){return t.end<t.start?[]:e.controller.getVisibleRows(t.start,t.end).filter(e=>!n.has(cc(e))).map((n,r)=>Kc(e,n,t.start+r))}function Jc({composition:e,controller:t,gitStatusByPath:n,ignoredGitDirectories:r,directoriesWithGitChanges:i,icons:a,instanceId:o,itemHeight:s=la,overscan:c=10,renamingEnabled:l=!1,renderRowDecoration:u,searchBlurBehavior:d=`close`,searchEnabled:f=!1,searchFakeFocus:p=!1,slotHost:m,stickyFolders:h=!1,initialViewportHeight:g=420}){"use no memo";let _=Z(null),v=Z(null),y=Z(!1),b=Z(null),x=Z(null),S=Z(null),C=Z(null),ee=Z(null),w=Z(new Map),T=Z(new Map),E=Z(()=>{}),te=Z(null),ne=Z(0),re=Z(!1),D=Z(null);D.current!==t&&(re.current=!1,D.current=t);let O=Z(!1),ie=Z(null),k=Z(null),A=Z(!1),ae=Z(null),oe=Z(null),se=Z(null),de=Z(null),fe=Z(null),pe=Z(null),me=Z(null),he=Z(null),ge=Z(!1),_e=Z(null),j=Z(null),ve=Z(null),M=Z(null),ye=$s(()=>new Map,[]),[,be]=Ys(0),[xe,Se]=Ys(null),[N,Ce]=Ys(null),[we,Te]=Ys(null),[Ee,De]=Ys(null),[Oe,ke]=Ys(0),[P,Ae]=Ys(null),je=Z(P);je.current=P;let Me=Z(null),Ne=Z(null),Pe=Z(null),Fe=Z(null),Ie=Z(null),Le=Z(!1),Re=()=>{Ne.current=null,Pe.current=null,Fe.current=null},ze=(e,t)=>{Ne.current=e,Pe.current=null,Fe.current=t==null?null:{path:e,scrollTop:t}},Be=(e,t)=>{Ne.current=null,Pe.current={path:e,viewportOffset:t},Fe.current=null},Ve=Z(d===`retain`&&t.isSearchOpen()),[He,Ue]=Ys(p);Zs(()=>{p||Ue(!1)},[p]);let We=Z(!1),Ge=Q(()=>{We.current=!0,Ue(e=>e&&!1)},[]),[Ke,qe]=Ys(()=>dc({controller:t,itemHeight:s,overscan:c,scrollTop:0,stickyFolders:h,viewportHeight:g})),[Je,Ye]=Ys(!1);Zs(()=>{Ye(!0)},[]);let F=e?.contextMenu?.enabled===!0||e?.contextMenu?.render!=null||e?.contextMenu?.onOpen!=null||e?.contextMenu?.onClose!=null,Xe=e?.contextMenu?.triggerMode??(F?`right-click`:`both`),Ze=Xe===`both`||Xe===`button`,Qe=e?.contextMenu?.buttonVisibility??`when-needed`,$e=Xe===`both`||Xe===`right-click`;Qs(()=>{let e=S.current;if(e==null)return;let t=e=>{if(!(e instanceof CustomEvent))return;let t=e.detail?.path??null;Ie.current=t,Ce(t),De(t==null?null:`pointer`)},n=e=>{e instanceof CustomEvent&&(Le.current=e.detail?.disabled===!0)};return e.addEventListener(`file-tree-debug-set-context-menu-trigger`,t),e.addEventListener(`file-tree-debug-set-scroll-suppression`,n),()=>{e.removeEventListener(`file-tree-debug-set-context-menu-trigger`,t),e.removeEventListener(`file-tree-debug-set-scroll-suppression`,n)}},[]);let et=Q((e,t)=>{Pc(w.current,e,t)},[]),tt=Q((e,t)=>{Pc(T.current,e,t)},[]),nt=Q(e=>{x.current=e},[]),rt=Q(e=>Fc(e,T.current,w.current),[]),it=n!=null||r!=null||i!=null,{resolveIcon:at}=$s(()=>Mt(a),[a]),ot=t[Ua](),st=ot.getPath(),ct=st!=null,lt=t.isSearchOpen(),ut=t.getSearchValue(),I=t.getFocusedPath(),L=t.getFocusedIndex(),dt=t.getScrollRequest(),ft=t.isDragAndDropEnabled(),pt=t.getDragSession(),mt=$s(()=>pt==null?null:new Set(pt.draggedPaths),[pt]),ht=pt?.target??null,gt=pt?.primaryPath??null,_t=Bc(o),{overlayHeight:vt,overlayRows:yt,snapshot:R,visibleRows:bt}=Ke,z=R.physical.viewportHeight,xt=$s(()=>({end:R.window.endIndex,start:R.window.startIndex}),[R.window.endIndex,R.window.startIndex]),St=yt,Ct=R.sticky.rows,wt=R.physical.totalHeight,Tt=R.sticky.height,Et=$s(()=>new Set(Ct.map(e=>cc(e.row))),[Ct]),Dt=L>=0&&L>=xt.start&&L<=xt.end,Ot=Q((e,t)=>u?.({item:zc(e,t),row:e})??null,[u]),kt=Q(e=>Cs(e==null?null:w.current.get(e)??null)?!0:Cs(S.current),[]),At=Q(e=>{kt(t.focusNearestPath(e))},[t,kt]),jt=Z(At);jt.current=At;let Nt=Z(!0),Pt=Z(()=>{}),Ft=Q((t=!0)=>{let n=je.current;n!=null&&(Nt.current=Nt.current&&t,Ae(null),e?.contextMenu?.onClose?.(),Nt.current&&At(n.path))},[e?.contextMenu,At]);Pt.current=Ft;let It=Q(e=>{let t=e==null?null:Nc(S.current,e);Te(e=>e===t?e:t)},[]),Lt=Q((e,n,r)=>{let i=t.getItem(n);if(i==null)return;let a=rt(n);if(a?.dataset.fileTreeStickyRow===`true`){let e=C.current;ze(n,e?.scrollTop??null),O.current=!0,Se(e=>e===n?e:n)}i.focus(),It(a),Nt.current=!0,Ae({anchorRect:r?.anchorRect??null,item:zc(e,n),path:n,source:r?.source??`keyboard`})},[t,rt,It]),Rt=Q(e=>{if(l){if(t.isSearchOpen()){let e=C.current,t=Ts(e,z);ae.current=L<0||e==null?null:Math.max(0,Math.min(L*s-e.scrollTop,Math.max(0,t-s))),A.current=!0}t.startRenaming(e)!==!1&&(De(`focus`),be(e=>e+1))}},[t,L,s,l,z]),zt=Q((e,{restoreTreeFocus:n=!0,targetOffset:r=`live-overlay`}={})=>{let i=C.current;if(i==null)return!1;t.focusPath(e);let a=t.getFocusedIndex();if(a<0)return!1;let o=t.getVisibleRows(a,a)[0]??null;if(o==null)return!1;let l=Ts(i,z),u=t.getVisibleCount()*s,d=r===`sticky-parents`?o.ancestorPaths.length*s:dc({controller:t,itemHeight:s,overscan:c,scrollTop:i.scrollTop,stickyFolders:h,viewportHeight:l}).snapshot.sticky.height;return O.current=!0,As(i,a,s,l,u,d),E.current(),Me.current=n?e:null,!0},[t,s,c,z,h]),Bt=()=>y.current===!0||M.current!=null||ge.current===!0,Vt=e=>typeof window.requestAnimationFrame==`function`?window.requestAnimationFrame(()=>{e()}):window.setTimeout(e,16),Ht=e=>{if(e!=null){if(typeof window.cancelAnimationFrame==`function`){window.cancelAnimationFrame(e);return}window.clearTimeout(e)}},Ut=()=>{de.current!=null&&(clearTimeout(de.current),de.current=null),se.current=null},Wt=()=>{pe.current?.remove(),pe.current=null},Gt=()=>{Ht(oe.current),oe.current=null,fe.current=null},Kt=e=>{let t=S.current?.getRootNode();if(t instanceof ShadowRoot){t.append(e);return}document.body.append(e)},qt=()=>{he.current?.(),he.current=null,M.current!=null&&(clearTimeout(M.current),M.current=null),ge.current=!1,_e.current=null,ve.current=null,j.current!=null&&(j.current.setAttribute(`draggable`,`true`),j.current.style.removeProperty(`touch-action`),j.current=null),Wt(),Ut(),Gt(),me.current=null},Jt=(e,n)=>{let r=S.current?.getRootNode(),i=_c(hc(r instanceof ShadowRoot?r:document,e,n));return t.setDragTarget(i),t.getDragSession()?.target??null},Yt=e=>{let n=t.getDragAndDropConfig()?.openOnDropDelay??800;if(e==null||e.kind!==`directory`||e.directoryPath==null||n<=0){Ut();return}let r=t.getItem(e.directoryPath),i=Cc(r)?r:null;if(i==null||i.isExpanded()){Ut();return}let a=`${e.directoryPath}::${e.flattenedSegmentPath??``}`;se.current!==a&&(Ut(),se.current=a,de.current=setTimeout(()=>{let n=t.getDragSession()?.target;n?.kind===`directory`&&n.directoryPath===e.directoryPath&&n.flattenedSegmentPath===e.flattenedSegmentPath&&i.expand()},n))},Xt=()=>{oe.current=null;let e=fe.current,n=C.current;if(e==null||n==null||t.getDragSession()==null)return;let r=n.getBoundingClientRect(),i=bc(e.clientY,r);if(i===0)return;let a=Math.max(0,n.scrollHeight-n.clientHeight),o=Math.max(0,Math.min(a,n.scrollTop+i));o!==n.scrollTop&&(n.scrollTop=o,E.current()),Yt(Jt(e.clientX,e.clientY)),oe.current=Vt(Xt)},Zt=(e,t)=>{fe.current={clientX:e,clientY:t},oe.current??=Vt(Xt)},Qt=(e,n,r)=>{let i=e.currentTarget;if(i!=null){if(qt(),Wt(),Ut(),Gt(),t.startDrag(r)===!1){e.preventDefault();return}if(me.current=n,e.dataTransfer!=null&&(e.dataTransfer.effectAllowed=`move`,e.dataTransfer.dropEffect=`move`,e.dataTransfer.setData(`text/plain`,r),yc())){let t=vc(i),n=i.getBoundingClientRect();Object.assign(t.style,{height:`${n.height}px`,opacity:`0.85`,transform:`translate3d(-9999px, 0px, 0)`,width:`${n.width}px`}),Kt(t),pe.current=t,e.dataTransfer.setDragImage(t,Math.max(0,e.clientX-n.left),Math.max(0,e.clientY-n.top))}}},$t=()=>{Wt(),Ut(),Gt(),me.current=null,t.cancelDrag()},en=(e,n,r)=>{if(M.current!=null||ge.current)return;let i=e.touches[0],a=e.currentTarget;if(i==null||a==null)return;ve.current={clientX:i.clientX,clientY:i.clientY},j.current=a,a.setAttribute(`draggable`,`false`);let o=(e={})=>{let t=e.restoreNativeDraggable??!ge.current;M.current!=null&&(clearTimeout(M.current),M.current=null),document.removeEventListener(`touchmove`,s),document.removeEventListener(`touchend`,c),document.removeEventListener(`touchcancel`,c),he.current===o&&(he.current=null),t&&(a.setAttribute(`draggable`,`true`),j.current===a&&(j.current=null),ve.current=null)},s=e=>{let t=e.touches[0],n=ve.current;if(t==null||n==null)return;let r=t.clientX-n.clientX,i=t.clientY-n.clientY;r*r+i*i<=100||o()},c=()=>{o()};document.addEventListener(`touchmove`,s,{passive:!0}),document.addEventListener(`touchend`,c),document.addEventListener(`touchcancel`,c),he.current=o,M.current=setTimeout(()=>{if(o({restoreNativeDraggable:!1}),t.startDrag(r)===!1){a.setAttribute(`draggable`,`true`),j.current===a&&(j.current=null),ve.current=null;return}ge.current=!0,j.current=a,a.setAttribute(`draggable`,`false`),a.style.setProperty(`touch-action`,`none`),me.current=n;let e=a.getBoundingClientRect(),s=vc(a);Object.assign(s.style,{height:`${e.height}px`,opacity:`0.85`,transform:`translate3d(${e.left}px, ${e.top}px, 0)`,width:`${e.width}px`}),Kt(s),pe.current=s,_e.current={x:i.clientX-e.left,y:i.clientY-e.top};let c=e=>{let t=e.touches[0];if(t==null)return;e.preventDefault();let n=_e.current;n!=null&&pe.current!=null&&(pe.current.style.transform=`translate3d(${t.clientX-n.x}px, ${t.clientY-n.y}px, 0)`),Yt(Jt(t.clientX,t.clientY)),Zt(t.clientX,t.clientY)},l=e=>{let n=e.changedTouches[0];n!=null&&Jt(n.clientX,n.clientY),t.completeDrag(),qt()},u=()=>{t.cancelDrag(),qt()};he.current=()=>{document.removeEventListener(`touchmove`,c),document.removeEventListener(`touchend`,l),document.removeEventListener(`touchcancel`,u)},document.addEventListener(`touchmove`,c,{passive:!1}),document.addEventListener(`touchend`,l),document.addEventListener(`touchcancel`,u)},fc)},B=e=>{if(P!=null){if(e.key===`Escape`){Ft(),e.preventDefault(),e.stopPropagation();return}kc.has(e.key)&&(e.preventDefault(),e.stopPropagation());return}if(ot.isActive()){if(e.key===`Escape`)ot.cancel();else if(e.key===`Enter`)ot.commit();else return;De(`focus`),be(e=>e+1),e.preventDefault(),e.stopPropagation();return}if(l&&e.key===`F2`){Rt(I??void 0),e.preventDefault(),e.stopPropagation();return}if(lt){if(e.key===`Escape`)A.current=!1,ae.current=null,t.closeSearch();else if(e.key===`Enter`){let e=t.getFocusedPath();e!=null&&t.selectOnlyPath(e);let n=C.current,r=Ts(n,z);ae.current=L<0||n==null?null:Math.max(0,Math.min(L*s-n.scrollTop,Math.max(0,r-s))),A.current=!0,t.closeSearch()}else if(e.key===`ArrowDown`)t.focusNextSearchMatch();else if(e.key===`ArrowUp`)t.focusPreviousSearchMatch();else return;De(`focus`),be(e=>e+1),e.preventDefault(),e.stopPropagation();return}if(f&&Tc(e)){t.openSearch(e.key),be(e=>e+1),e.preventDefault(),e.stopPropagation();return}let n=F&&Dc(e),r=Oc(e,F),i=r&&S.current!=null?ws(S.current):null,a=r?new Set(Ic(S.current)):new Set,o=i?.dataset.fileTreeStickyPath??null,c=i?.dataset.fileTreeStickyRow===`true`&&o!=null;if(c&&o!==I&&a.has(o)){let e=C.current;ze(o,e?.scrollTop??null),t.focusPath(o)}let u=t.getFocusedPath(),d=t.getFocusedIndex(),p=t.getFocusedItem();if(p==null)return;let m=Cc(p)?p:null,h=u!=null&&(Et.has(u)||c&&o===u&&a.has(u)),g=e.key===`ArrowDown`||e.key===`ArrowUp`||e.key===`ArrowRight`&&m!=null&&m.isExpanded(),_=e.key===`ArrowLeft`&&h&&m!=null&&m.isExpanded(),v=C.current,y=!0;if(e.shiftKey&&e.key===`ArrowDown`)t.extendSelectionFromFocused(1);else if(e.shiftKey&&e.key===`ArrowUp`)t.extendSelectionFromFocused(-1);else if(n&&u!=null&&d>=0){let e=t.getVisibleRows(d,d)[0]??null,n=Fc(u,T.current,w.current);e==null||n==null?y=!1:Lt(e,u)}else if((e.ctrlKey||e.metaKey)&&wc(e))t.toggleFocusedSelection();else if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()===`a`)t.selectAllVisiblePaths();else switch(e.key){case`ArrowDown`:t.focusNextItem();break;case`ArrowUp`:t.focusPreviousItem();break;case`ArrowRight`:m==null||m.isExpanded()?t.focusNextItem():m.expand();break;case`ArrowLeft`:m!=null&&m.isExpanded()?m.collapse():t.focusParentItem();break;case`Home`:t.focusFirstItem();break;case`End`:t.focusLastItem();break;default:y=!1}if(!y)return;De(`focus`);let b=t.getFocusedPath(),x=b!=null&&(Et.has(b)||a.has(b)),ee=g&&b!==u,E=n&&c&&o===u&&b===u;if((h||E)&&b!=null&&(ee&&x||E))ze(b,v?.scrollTop??null),O.current=!0,Se(e=>e===b?e:b);else{let t=e.key===`ArrowUp`&&h&&b!==u;b!=null&&(t||_&&b===u)?(Be(b,Rc(S.current,v,i,u,s,Tt,z)),O.current=!0,Se(e=>e===b?e:b)):Re()}be(e=>e+1),e.preventDefault(),e.stopPropagation()};Qs(()=>{if(!(!f||!lt)){if(Ve.current){Ve.current=!1;return}Cs(ee.current)}},[lt,f]),Qs(()=>{let e=x.current;switch(Ms({hasRenderedInput:e!=null,previousRenamingPath:k.current,renamingPath:st})){case`reset`:k.current=null;return;case`reveal-canonical`:st!=null&&zt(st,{restoreTreeFocus:!1,targetOffset:`live-overlay`});return;case`ignore`:return;case`focus-input`:e!=null&&(Me.current=null,k.current=st,Cs(e),e.select());return}},[xt.end,xt.start,st,zt,Et]),Qs(()=>{let e=S.current;if(e==null)return;let t=null,n=()=>{t!=null&&(clearTimeout(t),t=null)},r=()=>{let t=ws(e)?.dataset.itemPath??null;Se(e=>e===t?e:t)},i=()=>{n(),O.current=!0,r()},a=i=>{let a=i.relatedTarget;if(a==null){n(),t=setTimeout(()=>{if(t=null,ws(e)!=null){r();return}O.current=!1,Se(null)},0);return}if(!(a instanceof Node)||!e.contains(a)){n(),O.current=!1,Se(null);return}let o=a instanceof HTMLElement?a.dataset.itemPath??null:null;Se(e=>e===o?e:o)};return e.addEventListener(`focusin`,i),e.addEventListener(`focusout`,a),()=>{n(),e.removeEventListener(`focusin`,i),e.removeEventListener(`focusout`,a)}},[]),Qs(()=>{let e=S.current;e!=null&&(R.physical.scrollTop<=0?e.dataset.scrollAtTop=`true`:delete e.dataset.scrollAtTop)},[R.physical.scrollTop]),Qs(()=>{let e=null,n=C.current,r=b.current,i=S.current;if(n==null)return;te.current=Ts(n,g);let a=()=>{let e=t.getVisibleCount(),r=Es(te.current,g),i=Math.max(0,e*s-r);n.scrollTop>i&&(n.scrollTop=i),qe(dc({controller:t,itemHeight:s,overscan:c,scrollTop:Math.min(n.scrollTop,i),stickyFolders:h,viewportHeight:r}))};if(!re.current){re.current=!0;let e=t.getFocusedIndex();if(e>=0){let r=Es(te.current,g),i=t.getVisibleRows(e,e)[0]??null;Os(n,e,s,r,h&&i!=null?Math.max(0,Math.min(i.ancestorPaths.length*s,Math.max(0,r-s))):0)}}E.current=a;let o=!1,l=t.subscribe(()=>{o?be(e=>e+1):o=!0,a()}),u=()=>{Le.current!==!0&&(r!=null&&(r.dataset.isScrolling??=``),i!=null&&(i.dataset.isScrolling??=``),y.current=!0,e!=null&&clearTimeout(e),e=setTimeout(()=>{r!=null&&delete r.dataset.isScrolling,i!=null&&delete i.dataset.isScrolling,y.current=!1,ke(e=>e+1),e=null},50))},d=null,f=()=>{i!=null&&delete i.dataset.overlayReveal,d!=null&&(clearTimeout(d),d=null)},p=()=>{i!=null&&Le.current!==!0&&(n.scrollTop>0||(i.dataset.overlayReveal=`true`,d!=null&&clearTimeout(d),d=setTimeout(()=>{f()},200)))},m=()=>{if(a(),n.scrollTop>0&&f(),je.current!=null&&y.current&&Pt.current(),Le.current===!0){y.current=!1;return}Ce(e=>e==null?e:null),u()},_=()=>{u(),p()},v=new Set([`ArrowUp`,`ArrowDown`,`ArrowLeft`,`ArrowRight`,`PageUp`,`PageDown`,`Home`,`End`,` `,`Spacebar`]),x=e=>{v.has(e.key)&&_()};n.addEventListener(`scroll`,m,{passive:!0}),n.addEventListener(`wheel`,_,{passive:!0}),n.addEventListener(`touchmove`,_,{passive:!0}),n.addEventListener(`keydown`,x);let ee=typeof ResizeObserver<`u`?new ResizeObserver(e=>{te.current=(e[0]==null?null:Ds(e[0]))??Ts(n,g),a()}):null;return ee?.observe(n),()=>{E.current=()=>{},l(),n.removeEventListener(`scroll`,m),n.removeEventListener(`wheel`,_),n.removeEventListener(`touchmove`,_),n.removeEventListener(`keydown`,x),e!=null&&clearTimeout(e),d!=null&&clearTimeout(d),r!=null&&delete r.dataset.isScrolling,i!=null&&(delete i.dataset.isScrolling,delete i.dataset.overlayReveal),y.current=!1,te.current=null,ee?.disconnect()}},[t,g,s,c,h]),Qs(()=>{F||P==null||Ft(!1)},[Ft,F,P]);let tn=$s(()=>P==null?null:`${P.path}::${P.source}`,[P]);Qs(()=>{if(tn==null){m?.clearSlotContent(le);return}let t=je.current;if(t==null)return;let n=v.current??_.current;if(n==null)return;let r={anchorElement:n,anchorRect:t.anchorRect??jc(n.getBoundingClientRect()),close:e=>{Pt.current(e?.restoreFocus??!0)},restoreFocus:()=>{Nt.current&&jt.current(je.current?.path??null)}},i=e?.contextMenu?.render?.(t.item,r)??null;return m?.setSlotContent(le,i),e?.contextMenu?.onOpen?.(t.item,r),Wc(i),queueMicrotask(()=>{i==null||!i.isConnected||document.activeElement===i&&Wc(i)}),()=>{m?.clearSlotContent(le)}},[tn,e?.contextMenu,m]),Qs(()=>{P!=null&&t.getItem(P.path)==null&&Ft()},[Ft,P,t]),Qs(()=>{if(P==null)return;let e=S.current?.getRootNode(),t=e instanceof ShadowRoot?e.host:S.current,n=e=>{let n=e.target;n instanceof Node&&(Ac(e)||_.current?.contains(n)!==!0&&t?.contains(n)!==!0&&Ft())},r=e=>{e.key===`Escape`&&(e.preventDefault(),e.stopPropagation(),Ft())};return document.addEventListener(`mousedown`,n,!0),document.addEventListener(`keydown`,r,!0),()=>{document.removeEventListener(`mousedown`,n,!0),document.removeEventListener(`keydown`,r,!0)}},[Ft,P]),Qs(()=>{let e=C.current,n=S.current;if(e==null||n==null){ie.current=I;return}let r=I==null?null:w.current.get(I)??null,i=ws(n),a=i?.dataset.itemPath??null,o=ct&&x.current===i,c=f&&ee.current===i,l=A.current&&!lt,u=ae.current??0,d=Me.current,p=Ne.current,m=Pe.current,g=Fe.current,_=i!=null,v=O.current||_,y=ie.current!==I,b=p!=null&&p===I&&I!=null,T=!1,te=!1;if(dt!=null&&dt.id!==ne.current){ne.current=dt.id;let n=dt.visibleIndex,r=t.getVisibleRows(n,n)[0]??null;if(r!=null){let t=h?Math.max(0,Math.min(r.ancestorPaths.length*s,Math.max(0,z-s))):Tt;T=!0,te=ks(e,n,s,z,wt,dt.offset,t)}t.clearScrollRequest(dt.id)}let re=!T&&l&&As(e,L,s,z,wt,u),D=!T&&d!=null&&d===I&&As(e,L,s,z,wt,Tt),k=!T&&m!=null&&m.path===I&&As(e,L,s,z,wt,m.viewportOffset),oe=!T&&g!=null&&g.path===I&&e.scrollTop!==g.scrollTop;if(oe&&(e.scrollTop=g.scrollTop),(oe||te||D||k||re||v&&y&&d!==I&&!b&&Os(e,L,s,z,Tt))&&E.current(),T){ie.current=I;return}if(!v){ie.current=I;return}if(o){ie.current=I;return}if(c&&!l){ie.current=I;return}if(r==null){l&&L>=0&&(As(e,L,s,z,wt,u),E.current()),ie.current=I;return}(y||l||d===I||p===I||m?.path===I||g?.path===I||a==null||a!==I)&&(Cs(r),d===I&&(Me.current=null),p===I&&(Ne.current=null),m?.path===I&&(Pe.current=null),g?.path===I&&(Fe.current=null),A.current=!1,ae.current=null),ie.current=I},[t,L,I,Dt,s,ct,lt,xt,z,f,dt,h,Tt,wt,bt]);let V=L>=0&&L>=R.visible.startIndex&&L<=R.visible.endIndex,nn=I!=null&&St.some(e=>cc(e.row)===I),rn=V||nn,an=Ze&&O.current===!0&&rn?I:null,on=Ee===`pointer`?N:null,sn=P?.path??Ie.current??on??an??N,H=P?.source===`right-click`;Qs(()=>{y.current&&P==null||It(rt(sn))},[P,rt,xt,z,Oe,St,sn,It,bt]);let cn=Q(e=>{if(y.current||Ac(e))return;let t=e.target;if(!(t instanceof HTMLElement)||t.closest?.(`[data-type="context-menu-trigger"]`)!=null)return;let n=t.closest?.(`[data-file-tree-sticky-row="true"]`),r=t.closest?.(`[data-type="item"]`),i=n instanceof HTMLElement?n.dataset.fileTreeStickyPath??null:r instanceof HTMLElement?r.dataset.itemPath??null:null;i!=null&&De(e=>e===`pointer`?e:`pointer`),Ce(e=>e===i?e:i)},[]),ln=Q(()=>{Ce(null)},[]);Qs(()=>{if(!ft)return;let e=()=>{qt(),t.cancelDrag()};return window.addEventListener(`dragend`,e),()=>{window.removeEventListener(`dragend`,e),qt(),t.cancelDrag()}},[t,ft]);let un=e=>{if(!ft||t.getDragSession()==null||ge.current)return;let n=_c(e.target instanceof HTMLElement?e.target:null);t.setDragTarget(n),Yt(t.getDragSession()?.target??null),Zt(e.clientX,e.clientY),e.dataTransfer!=null&&(e.dataTransfer.dropEffect=`move`),e.preventDefault()},dn=e=>{if(!ft||t.getDragSession()==null||ge.current)return;let n=e.relatedTarget;n instanceof Node&&S.current?.contains(n)===!0||(Ut(),Gt(),t.setDragTarget(null))},fn=e=>{!ft||t.getDragSession()==null||ge.current||(e.preventDefault(),Jt(e.clientX,e.clientY),t.completeDrag(),Wt(),Ut(),Gt(),me.current=null)},pn=R.window.height,mn=R.window.offsetTop,hn=Math.min(0,z-pn),gn=Math.min(0,z-pn-Tt),_n=xe===I||A.current,vn=I!=null&&_n&&!Dt&&L>=0?bt[L]??t.getVisibleRows(L,L)[0]??null:null,yn=vn==null?null:js(L,s,xt,pn),bn=me.current,xn=gt!=null&&bn!=null&&bn.path===gt&&bn.index>=xt.start&&bn.index<=xt.end,Sn=gt!=null&&bn!=null&&bn.path===gt&&!xn&&bn.path!==vn?.path?bn:null,Cn=Sn==null?null:js(Sn.index,s,xt,pn),wn=Ec((L>=0?bt[L]??t.getVisibleRows(L,L)[0]??null:null)?.ancestorPaths.at(-1)??null),Tn=lt&&I!=null?Vc(o,I,!Dt):void 0,En=P?.path??(lt?I:xe),Dn=P?.path??N,On=rt(sn),kn=F&&Ze&&!H&&!ct&&On!=null&&we!=null&&sn!=null,An=F&&(kn||P!=null),jn=P?.anchorRect,Mn=jn==null&&On!=null&&we!=null&&(P!=null||kn)?we:null,Nn=jn==null?Mn==null?void 0:{top:`${Mn}px`}:{left:`${jn.left}px`,position:`fixed`,right:`auto`,top:`${jn.top}px`},Pn=H?{opacity:`0`}:void 0,Fn=Q((e,n,r,i)=>{let a=Fs({event:{ctrlKey:e.ctrlKey,metaKey:e.metaKey,shiftKey:e.shiftKey},isDirectory:n.kind===`directory`,isSearchOpen:lt,mode:i}),o=a.toggleDirectory&&n.kind===`directory`,s=o?t.resolveMountedDirectoryPathFromInput(r):null;if(o&&s==null)return;let c=s??r;switch(a.selection.kind){case`range`:t.selectPathRange(c,a.selection.additive);break;case`toggle`:t.togglePathSelectionFromInput(c);break;case`single`:t.selectOnlyMountedPathFromInput(c)}let l=e.currentTarget instanceof HTMLElement?e.currentTarget:null,u=n.index>=R.visible.startIndex&&n.index<=R.visible.endIndex,d=i===`flow`&&u&&l!=null&&l.dataset.itemParked!==`true`;t.focusMountedPathFromInput(c),d&&(O.current=!0,Se(e=>e===c?e:c),De(`focus`)),o&&t.toggleMountedDirectoryFromInput(c),a.closeSearch&&t.closeSearch(),a.revealCanonical&&zt(c,{targetOffset:`sticky-parents`})},[t,lt,R.visible.endIndex,R.visible.startIndex,zt]),In=()=>{if(y.current||!Ze||sn==null||On==null)return;let e=t.getItem(sn);e!=null&&(It(On),Nt.current=!0,Ae({anchorRect:null,item:{kind:e.isDirectory()?`directory`:`file`,name:On.getAttribute(`aria-label`)??sn,path:e.getPath()},path:e.getPath(),source:`button`}))},Ln={contextHoverPath:Dn,contextMenuButtonTriggerEnabled:Ze,contextMenuButtonVisibility:Qe,contextMenuEnabled:F,contextMenuRightClickEnabled:$e,contextMenuTriggerMode:Xe,controller:t,directoriesWithGitChanges:i,dragAndDropEnabled:ft,draggedPathSet:mt,dragTarget:ht,gitLaneActive:it,gitStatusByPath:n,handleRowDragEnd:$t,handleRowDragStart:Qt,handleRowTouchStart:en,ignoredGitDirectories:r,ignoredInheritanceCache:ye,instanceId:o,itemHeight:s,onKeyDown:B,onRowClick:Fn,openContextMenuForRow:Lt,registerButton:et,registerRenameInput:nt,renameView:ot,renderDecorationForRow:Ot,resolveIcon:at,shouldSuppressContextMenu:Bt,visualFocusPath:En},Rn={...Ln,registerButton:tt};return J(`div`,{ref:S,id:_t,"data-file-tree-context-menu-button-visibility":F&&Ze?Qe:void 0,"data-file-tree-context-menu-trigger-mode":F?Xe:void 0,"data-file-tree-has-context-menu-action-lane":F&&Ze?`true`:void 0,"data-file-tree-has-git-lane":it?`true`:void 0,"data-file-tree-virtualized-root":`true`,onDragLeave:ft?dn:void 0,onDragOver:ft?un:void 0,onDrop:ft?fn:void 0,onKeyDown:B,onPointerLeave:F?ln:void 0,onPointerOver:F?cn:void 0,role:`tree`,tabIndex:-1,style:{outline:`none`,position:`relative`},children:[J(`style`,{"data-file-tree-guide-style":`true`,dangerouslySetInnerHTML:{__html:wn}}),J(`slot`,{name:ce,"data-type":`header-slot`}),f?J(`div`,{"data-file-tree-search-container":!0,"data-open":lt?`true`:`false`,children:J(`input`,{ref:ee,"aria-activedescendant":Tn,"aria-controls":_t,placeholder:`Search…`,"data-file-tree-search-input":!0,"data-file-tree-search-input-fake-focus":He?`true`:void 0,value:ut,onBlur:()=>{d===`retain`&&!We.current||t.closeSearch()},onFocus:Ge,onPointerDown:Ge,onInput:e=>{Ge();let n=e.currentTarget;t.setSearch(n.value)}})}):null,J(`div`,{ref:C,"data-file-tree-virtualized-scroll":`true`,children:[h&&Je&&St.length>0?J(`div`,{"aria-hidden":`true`,"data-file-tree-sticky-overlay":`true`,children:J(`div`,{"data-file-tree-sticky-overlay-content":`true`,style:{height:`${vt}px`},children:St.map((e,t)=>Kc(Rn,e.row,`sticky:${cc(e.row)}`,{mode:`sticky`,style:{left:`0`,position:`absolute`,right:`0`,top:`${e.top}px`,zIndex:`${St.length-t}`}}))})}):null,J(`div`,{ref:b,"data-file-tree-virtualized-list":`true`,style:{height:`${wt}px`},children:[J(`div`,{"data-file-tree-virtualized-sticky-offset":`true`,"aria-hidden":`true`,style:{height:`${mn}px`}}),J(`div`,{"data-file-tree-virtualized-sticky":`true`,style:{height:`${pn}px`,top:`${hn}px`,bottom:`${gn}px`},children:[qc(Ln,xt,Et),vn!=null&&yn!=null?Kc(Ln,vn,`parked:${vn.path}`,{isParked:!0,style:{left:`0`,opacity:`0`,pointerEvents:gt===vn.path?`none`:void 0,position:`absolute`,right:`0`,top:`${yn}px`}}):null,Sn!=null&&Cn!=null?Kc(Ln,Sn,`parked-drag:${Sn.path}`,{isParked:!0,style:{left:`0`,opacity:`0`,pointerEvents:`none`,position:`absolute`,right:`0`,top:`${Cn}px`}}):null]})]})]}),F?J(`div`,{ref:_,"data-type":`context-menu-anchor`,"data-visible":An?`true`:`false`,style:Nn,children:[J(`button`,{ref:v,type:`button`,"data-type":ue,"aria-label":`Options`,"aria-haspopup":`menu`,"aria-expanded":P==null?`false`:`true`,"data-visible":kn?`true`:`false`,onMouseDown:e=>{e.preventDefault()},onClick:e=>{if(e.preventDefault(),e.stopPropagation(),P!=null){Ft();return}In()},tabIndex:-1,style:Pn,children:J(Ko,{...at(`file-tree-icon-ellipsis`)})}),P==null?null:J(`slot`,{name:le})]}):null,P==null?null:J(`div`,{"data-type":`context-menu-wash`,"aria-hidden":`true`,onMouseDownCapture:e=>{e.preventDefault(),Ft()},onTouchStartCapture:e=>{e.preventDefault(),e.stopPropagation(),Ft()},onTouchMoveCapture:e=>{e.preventDefault(),e.stopPropagation()},onWheelCapture:e=>{e.preventDefault(),e.stopPropagation()}})]})}var Yc={hydrateRoot:(e,t)=>{Vo(_o(Jc,t),e)},renderRoot:(e,t)=>{Bo(_o(Jc,t),e)},unmountRoot:e=>{Bo(null,e)}};function Xc(e,t){Yc.renderRoot(e,t)}function Zc(e,t){Yc.hydrateRoot(e,t)}function Qc(e){Yc.unmountRoot(e)}var $c=class{#e=new Map;#t=null;clearAll(){for(let e of this.#e.values())e.remove();this.#e.clear()}clearSlotContent(e){let t=this.#n(e);t!=null&&(t.remove(),this.#e.delete(e))}setHost(e){if(this.#t=e,e!=null){this.#i(e);for(let[e,t]of this.#e)this.#r(e,t)}}setSlotContent(e,t){let n=this.#n(e);if(n===t){t!=null&&(this.#e.set(e,t),this.#r(e,t));return}if(n?.remove(),t==null){this.#e.delete(e);return}this.#e.set(e,t),this.#r(e,t)}setSlotHtml(e,t){let n=t?.trim()??``;if(n.length===0){this.setSlotContent(e,null);return}let r=this.#n(e);if(r!=null&&r.innerHTML===n){this.#e.set(e,r),this.#r(e,r);return}let i=document.createElement(`div`);i.innerHTML=n,this.setSlotContent(e,i)}#n(e){let t=this.#e.get(e)??null;if(t!=null)return t;let n=this.#t;if(n==null)return null;for(let t of Array.from(n.children))if(t instanceof HTMLElement&&t.dataset.fileTreeManagedSlot===e)return t;return null}#r(e,t){t.slot=e,t.dataset.fileTreeManagedSlot=e,this.#t!=null&&t.parentNode!==this.#t&&this.#t.appendChild(t)}#i(e){for(let t of Array.from(e.children)){if(!(t instanceof HTMLElement))continue;let e=t.dataset.fileTreeManagedSlot;e==null||this.#e.has(e)||this.#e.set(e,t)}}};Array.isArray;var el=0;function tl(e){return e!=null&&e.length>0?e:(el+=1,`pst_ft_${el}`)}function nl({initialVisibleRowCount:e,itemHeight:t}){return e==null?420:Math.max(0,e)*(t??la)}function rl(e){if(typeof document>`u`)return;let t=document.createElement(`div`);t.innerHTML=e;let n=t.querySelector(`svg`);return n instanceof SVGElement?n:void 0}function il(e){return e.querySelector(`#file-tree-icon-chevron`)instanceof SVGElement&&e.querySelector(`#file-tree-icon-file`)instanceof SVGElement&&e.querySelector(`#file-tree-icon-dot`)instanceof SVGElement&&e.querySelector(`#file-tree-icon-lock`)instanceof SVGElement}function al(e){return Array.from(e.children).filter(e=>e instanceof SVGElement)}var ol=class{static LoadedCustomComponent=!0;#e;#t;#n;#r;#i;#a;#o;#s;#c;#l;#u=new $c;#d;#f;#p;#m;#h;#g;#_;#v;#y;#b=null;#x=null;#S;#C=!1;#w=!1;constructor(e){let{composition:t,density:n,fileTreeSearchMode:r,gitStatus:i,id:a,initialSearchQuery:o,icons:s,itemHeight:c,onExpansionChange:l,onSearchChange:u,onSelectionChange:d,overscan:f,renderRowDecoration:p,renaming:m,search:h,searchBlurBehavior:g,searchFakeFocus:_,stickyFolders:v,unsafeCSS:y,initialVisibleRowCount:b,...x}=e;this.#e=t,this.#n=tl(a),this.#m=eo(i),this.#h=s,this.#g=y,this.#r=l,this.#i=d,this.#a=p,this.#o=m!=null&&m!==!1,this.#s=g,this.#c=h===!0,this.#l=_===!0,this.#d=ca(n,c),this.#f={itemHeight:this.#d.itemHeight,overscan:f,stickyFolders:v,initialVisibleRowCount:b},this.#t=new Ya({...x,fileTreeSearchMode:r,initialSearchQuery:o,onSearchChange:u,renaming:m}),this.#y=this.#t.getSelectionVersion(),this.#b=this.#i==null?null:this.subscribe(()=>{this.#k()}),this.#x=this.#r==null?null:this.#t.subscribe(e=>{(e?.operation===`expand`||e?.operation===`collapse`)&&this.#r?.({path:e.path,expanded:e.operation===`expand`})})}unmount(){this.#S!=null&&(Qc(this.#S),delete this.#S.dataset.fileTreeVirtualizedWrapper,this.#S=void 0),this.#u.clearAll(),this.#u.setHost(null),this.#p!=null&&(delete this.#p.dataset.fileTreeVirtualized,this.#R(this.#p),this.#p=void 0)}cleanUp(){this.unmount(),this.#b?.(),this.#b=null,this.#x?.(),this.#x=null,this.#t.destroy()}getFileTreeContainer(){return this.#p}getItem(e){return this.#t.getItem(e)}getFocusedItem(){return this.#t.getFocusedItem()}getFocusedPath(){return this.#t.getFocusedPath()}getSelectedPaths(){return this.#t.getSelectedPaths()}getComposition(){return this.#e}getItemHeight(){return this.#d.itemHeight}getDensityFactor(){return this.#d.factor}subscribe(e){let t=!1;return this.#t.subscribe(()=>{if(!t){t=!0;return}e()})}focusPath(e){this.#t.focusPath(e)}scrollToPath(e,t){this.#t.scrollToPath(e,t)}focusNearestPath(e){return this.#t.focusNearestPath(e)}add(e){this.#t.add(e)}batch(e){this.#t.batch(e)}move(e,t,n){this.#t.move(e,t,n)}onMutation(e,t){return this.#t.onMutation(e,t)}setSearch(e){this.#t.setSearch(e)}openSearch(e){this.#t.openSearch(e)}closeSearch(){this.#t.closeSearch()}isSearchOpen(){return this.#t.isSearchOpen()}getSearchValue(){return this.#t.getSearchValue()}getSearchMatchingPaths(){return this.#t.getSearchMatchingPaths()}focusNextSearchMatch(){this.#t.focusNextSearchMatch()}focusPreviousSearchMatch(){this.#t.focusPreviousSearchMatch()}startRenaming(e,t){return this.#t.startRenaming(e,t)}remove(e,t){this.#t.remove(e,t)}resetPaths(e,t){this.#t.resetPaths(e,t)}setComposition(e){this.#e=e;let t=this.#D();t!=null&&(this.#A(),Xc(t.wrapper,this.#E()))}setGitStatus(e){this.#m=eo(e,this.#m);let t=this.#D();t!=null&&Xc(t.wrapper,this.#E())}setIcons(e){this.#h=e;let t=this.#D();t!=null&&(this.#O(t.host,t.wrapper),Xc(t.wrapper,this.#E()))}hydrate({fileTreeContainer:e}){let t=this.#I(e),n=this.#F(t);this.#A(),Zc(n,this.#E())}render({containerWrapper:e,fileTreeContainer:t}){let n=this.#I(t??this.#p,e),r=this.#F(n);this.#A(),Xc(r,this.#E())}#T(){return{initialViewportHeight:nl({initialVisibleRowCount:this.#f.initialVisibleRowCount,itemHeight:this.#f.itemHeight}),itemHeight:this.#f.itemHeight,overscan:this.#f.overscan,stickyFolders:this.#f.stickyFolders}}#E(){return{composition:this.#e,controller:this.#t,gitStatusByPath:this.#m?.statusByPath,ignoredGitDirectories:this.#m?.ignoredDirectoryPaths,directoriesWithGitChanges:this.#m?.directoriesWithChanges,icons:this.#h,instanceId:this.#n,renamingEnabled:this.#o,renderRowDecoration:this.#a,searchBlurBehavior:this.#s,searchEnabled:this.#c,searchFakeFocus:this.#l,slotHost:this.#u,...this.#T()}}#D(){let e=this.#p,t=this.#S;return e==null||t==null?null:{host:e,wrapper:t}}#O(e,t){let n=e.shadowRoot;n!=null&&(this.#j(n),this.#M(n)),this.#N(t)}#k(){let e=this.#i;if(e==null)return;let t=this.#t.getSelectionVersion();t!==this.#y&&(this.#y=t,e(this.#t.getSelectedPaths()))}#A(){let e=this.#e?.header?.render;if(e!=null){this.#u.setSlotContent(ce,e());return}this.#u.setSlotHtml(ce,this.#e?.header?.html??null)}#j(e){let t=al(e).find(e=>il(e)),n=rl(St(Dt(this.#h).set));n!=null&&(t==null||t.outerHTML!==n.outerHTML)&&(t==null?e.prepend(n):t.replaceWith(n))}#M(e){let t=al(e),n=t.find(e=>il(e)),r=t.filter(e=>e!==n),i=Dt(this.#h).spriteSheet?.trim()??``;if(i.length===0){for(let e of r)e.remove();return}let a=rl(i);if(a==null){for(let e of r)e.remove();return}if(r.length!==1||r[0].outerHTML!==a.outerHTML){for(let e of r)e.remove();e.appendChild(a)}}#N(e){let t=Dt(this.#h);t.colored&&wt(t.set)?e.dataset.fileTreeColoredIcons=`true`:delete e.dataset.fileTreeColoredIcons}#P(e){let t=e.querySelector(`style[${A}]`);if(this.#_==null&&t instanceof HTMLStyleElement&&(this.#_=t),this.#g==null||this.#g===``){this.#_?.remove(),this.#_=void 0,this.#v=void 0;return}(this.#_?.parentNode!==e||this.#v!==this.#g)&&(this.#_??=document.createElement(`style`),this.#_.setAttribute(A,``),this.#_.parentNode!==e&&e.appendChild(this.#_),this.#_.textContent=me(this.#g),this.#v=this.#g)}#F(e){if(this.#S!=null)return this.#S;let t=e.shadowRoot;if(t==null)throw Error(`FileTree requires a shadow root`);let n=Array.from(t.children).filter(e=>e instanceof HTMLDivElement&&typeof e.dataset.fileTreeId==`string`&&e.dataset.fileTreeId.length>0),r=n.find(e=>e.dataset.fileTreeId===this.#n)??n[0];return r!=null&&(this.#n=r.dataset.fileTreeId??this.#n),this.#S=r??document.createElement(`div`),this.#S.dataset.fileTreeId=this.#n,this.#S.dataset.fileTreeVirtualizedWrapper=`true`,this.#O(e,this.#S),this.#S.parentNode!==t&&t.appendChild(this.#S),this.#S}#I(e,t){let n=e??this.#p??document.createElement(`file-tree-container`);t!=null&&n.parentNode!==t&&t.appendChild(n);let r=n.shadowRoot??n.attachShadow({mode:`open`});return M(n,r),this.#P(r),n.dataset.fileTreeVirtualized=`true`,n.style.display=`flex`,this.#L(n),this.#u.setHost(n),this.#p=n,n}#L(e){e.style.getPropertyValue(`--trees-item-height`)===``&&(e.style.setProperty(`--trees-item-height`,`${String(this.#d.itemHeight)}px`),this.#C=!0),e.style.getPropertyValue(`--trees-density-override`)===``&&(e.style.setProperty(`--trees-density-override`,String(this.#d.factor)),this.#w=!0)}#R(e){this.#C&&=(e.style.removeProperty(`--trees-item-height`),!1),this.#w&&=(e.style.removeProperty(`--trees-density-override`),!1)}},sl=e(D(),1);function cl(e,t){let n=e.replace(/^\/+|\/+$/g,``);return t.map(e=>{let t=n?`${n}/${e.name}`:e.name;return e.type===`directory`?t+`/`:t})}function ll(e,t){return e===`file`?{includeFiles:!0,action:`file`,entries(e,t){return cl(e,t)},navigation(e){return pl(t,e)?e:void 0},result(e,t){return t||void 0},selection(e,n){if(pl(t,e))return wl(e,n,`file`,t)}}:{includeFiles:!1,action:`directory`,entries(e,t){return cl(e,t.filter(e=>e.type===`directory`))},navigation(e){return e},result(e,t,n=!0){if(n)return t||(e?Tl(e):void 0)},selection(e,t){return wl(e,t,`directory`)}}}function ul(e,t,n){let r=t.replace(/\\/g,`/`).replace(/^~(?=\/|$)/,n).replace(/\/+$/,``),i=e.replace(/\\/g,`/`).replace(/\/+$/,``);return r===i?``:r.startsWith(i+`/`)?r.slice(i.length+1):r}function dl(e,t,n){return fl(Ol(e).replace(/^~(?=\/|$)/,Ol(t)),n)}function fl(e,t){let n=Ol(e);return ml(Al(n)?n:kl(t,n))}function pl(e,t){return hl(e,t)!==void 0}function ml(e){let t=Ol(e),n=Al(t);return kl(n,t.slice(n.length).split(`/`).reduce((e,t)=>!t||t===`.`?e:t===`..`?(e.pop(),e):(e.push(t),e),[]).join(`/`))}function hl(e,t){if(!e)return;let n=ml(e),r=ml(t),i=/^[A-Za-z]:\//.test(n)||n.startsWith(`//`),a=i?n.toLowerCase():n,o=i?r.toLowerCase():r;if(o===a)return``;let s=a.endsWith(`/`)?a:a+`/`;if(o.startsWith(s))return r.slice(s.length)}function gl(e,t){return e?.query===t?e.items:[]}function _l(e,t){return cl(e,t.filter(e=>e.type===`directory`))}function vl(e,t){return!e.has(t)&&(e.add(t),!0)}function yl(e,t){return e===t}function bl(e){let t=new Map,n=[],r=[],i=0,a=()=>{for(;i<e;){let e=n.pop()??r.shift();if(!e)return;i++,e.run()}},o=(e,o,c)=>{let l=t.get(e);if(l)return o===`user`&&s(e),l.promise;let u=Promise.withResolvers(),d={key:e,priority:o,promise:u.promise,run:()=>{let n=()=>{i--,t.delete(e),a()};Promise.resolve().then(c).then(e=>{n(),u.resolve(e)},e=>{n(),u.reject(e)})}};return t.set(e,d),(o===`user`?n:r).push(d),a(),d.promise},s=e=>{let i=t.get(e);if(!i||i.priority===`user`)return;let a=r.indexOf(i);a!==-1&&(r.splice(a,1),i.priority=`user`,n.push(i))};return{schedule:o,promote:s}}function xl(e,t,n,r){return Math.min(Math.max(0,n-r),Math.max(0,e+t))}function Sl(e,t,n){return n===0?-1:(e+t+n)%n}function Cl(e,t){let n=$(e),r=t.replace(/\\/g,`/`).replace(/^\/+|\/+$/g,``);return r?!n||n===`/`?`/`+r:n.endsWith(`/`)?n+r:`${n}/${r}`:n||`/`}function wl(e,t,n,r){let i=t.endsWith(`/`);return n===`file`?i?void 0:r?hl(r,Cl(e,t)):t:i?Tl(Cl(e,t)):void 0}function Tl(e){let t=$(e);return/^[A-Za-z]:\//.test(t)||t.startsWith(`//`)?t.replaceAll(`/`,`\\`):t}function El(e){return((e??``).split(/\r?\n/)[0]??``).replace(/[\u0000-\u001F\u007F]/g,``).trim()}function Dl(e){let t=e.replaceAll(`\\`,`/`);return t.startsWith(`//`)&&!t.startsWith(`///`)?`//`+t.slice(2).replace(/\/+/g,`/`):t.replace(/\/+/g,`/`)}function Ol(e){let t=Dl(e);return/^[A-Za-z]:$/.test(t)?t+`/`:t}function $(e){let t=Ol(e);return t===`/`||t===`//`||/^[A-Za-z]:\/$/.test(t)?t:t.replace(/\/+$/,``)}function kl(e,t){let n=$(e??``),r=$(t).replace(/^\/+/,``);return n?r?n.endsWith(`/`)?n+r:n+`/`+r:n:r}function Al(e){let t=Ol(e);if(t.startsWith(`//`)){let[e,n]=t.slice(2).split(`/`);return e&&n?`//${e}/${n}`:`//`}return t.startsWith(`/`)?`/`:/^[A-Za-z]:\//.test(t)?t.slice(0,3):``}function jl(e){let t=$(e),n=Al(t);if(t===n||t===`/`||t===`//`||/^[A-Za-z]:\/$/.test(t))return t;let r=t.lastIndexOf(`/`);return r<n.length?n:r<=0?`/`:r===2&&/^[A-Za-z]:/.test(t)?t.slice(0,3):t.slice(0,r)}function Ml(e,t){let n=$(e);if(!t)return``;let r=$(t);return/^[A-Za-z]:\//.test(r)?``:n===r?`~`:n.startsWith(r+`/`)?`~`+n.slice(r.length):``}function Nl(e,t,n){let r=$(e);return/^[A-Za-z]:\//.test($(n))||/^[A-Za-z]:\//.test(r)?r.replaceAll(`/`,`\\`):Ml(r,n)||r}async function Pl(e,t,n){let r=await e.api.file.list({location:t,path:n});return r.data.map(e=>{let t=fl(e.path,r.location.directory);return{name:ee(t),type:e.type,absolute:t}})}function Fl(e){let t=new Map,n=0,r=t=>{let n=Ol(t),r=Al(n);if(r)return{directory:$(r),path:n.slice(r.length)};let i=e.base();if(!i)return;if(!n)return{directory:$(i),path:``};let a=e.home();return n===`~`?{directory:$(a||i),path:``}:n.startsWith(`~/`)?{directory:$(a||i),path:n.slice(2)}:{directory:$(i),path:n}},i=async n=>{let r=e.location();if(!r)return[];let i=JSON.stringify([r,$(n)]),a=t.get(i);if(a)return a;let o=Pl(e.sdk,r,n).catch(()=>[]).then(e=>e.filter(e=>e.type===`directory`));return t.set(i,o),o},a=async(e,t,n)=>{let r=await i(e);return t?sl.default.go(t,r,{key:`name`,limit:n}).map(e=>e.obj.absolute):r.slice(0,n).map(e=>e.absolute)};return async t=>{let o=++n,s=()=>o===n,c=El(t),l=r(c),u=e.location();if(!l||!u)return[];let d=Ol(c),f=d.startsWith(`~`)||!!Al(d)||d.includes(`/`),p=Ol(l.path);if(!f){let t=hl(u.directory,l.directory),n=t===void 0?[]:await e.sdk.api.file.find({location:u,query:kl(t,p),type:`directory`,limit:50}).then(e=>e.data.map(t=>fl(t.path,e.location.directory)).filter(e=>pl(l.directory,e))).catch(()=>[]);if(!s())return[];if(n.length)return n.slice(0,50);let r=p?await a(l.directory,p,50):(await i(l.directory)).map(e=>e.absolute);return s()?r:[]}let m=p.replace(/^\/+/,``).split(`/`),h=m.slice(0,-1).filter(e=>e&&e!==`.`),g=m.at(-1)??``,_=[l.directory];for(let e of h){if(!s())return[];if(e===`..`){_=_.map(jl);continue}if(_=Array.from(new Set((await Promise.all(_.map(t=>a(t,e,4)))).flat())).slice(0,12),!s()||_.length===0)return[]}let v=Array.from(new Set((await Promise.all(_.map(e=>a(e,g,50)))).flat()));if(!s())return[];let y=d.startsWith(`~`)?$(l.directory):``;if(d.endsWith(`/`)||!g)return Array.from(new Set([y,...v].filter(Boolean))).slice(0,50);let b=v.find(e=>ee(e).toLowerCase()===g.toLowerCase());if(!b)return v.slice(0,50);let x=await a(b,``,30);return s()?Array.from(new Set([y,...v,...x].filter(Boolean))).slice(0,50):[]}}var Il=i(`<div>`);function Ll(e){let[t,n]=m(e,[`class`,`classList`]);return(()=>{var e=Il();return o(e,v(n,{role:`separator`,"aria-orientation":`horizontal`,"data-component":`divider-v2`,get classList(){return{...t.classList,[t.class??``]:!!t.class}}}),!1,!1),e})()}var Rl=i(`<div id=directory-picker-suggestions role=listbox class=directory-picker-suggestions>`),zl=i(`<div class=directory-picker-path><div class=directory-picker-actions>`),Bl=i(`<div class=directory-picker-state>`),Vl=i(`<div class=directory-picker-browser>`),Hl=i(`<div class=directory-picker-selection>`),Ul=i(`<button role=option>`);function Wl(e){let{sync:r,sdk:i}=x().ensureServerCtx(e.server),o=S(),m=C(),v=ll(e.mode??`directory`,e.start),ee={file:m.t(`dialog.directory.action.selectFile`),directory:m.t(`dialog.directory.action.selectFolder`)},[D,ie]=h(``),[k,A]=h(``),[ae,oe]=h(``),[se,ce]=h(!1),[le,ue]=h(-1),[de,fe]=h(!1),[pe,me]=h(!1),[he,ge]=h(!1),_e=new Map,j=bl(3),ve=new Set,M,ye,be,xe=0,[Se]=y(()=>!e.location||void 0,()=>i.api.location.get().catch(()=>void 0),{initialValue:void 0}),N=f(()=>r.data.path.home||``),Ce=f(()=>{let t=e.location??Se.latest;return t?{directory:t.directory}:void 0}),we=f(()=>e.start||r.data.path.home||e.location?.directory||r.data.path.directory||Se.latest?.directory),Te=Fl({sdk:i,home:N,location:Ce,base:()=>D()||we()}),[Ee]=y(k,async e=>{let t=El(e),n=t.replace(/\/+$/,``),r=Nl(D(),e,N()).replace(/\/+$/,``);if(!t||D()&&n===r)return{query:e,items:[]};let a=(await Te(e)).map(e=>({absolute:e,type:`directory`}));if(!v.includeFiles)return{query:e,items:a.slice(0,5)};let o=Ce()?.directory;if(!o)return{query:e,items:a.slice(0,5)};let s=hl(o,dl(t,N(),D()||o));if(s===void 0)return{query:e,items:a.slice(0,5)};let c=await i.api.file.find({location:Ce(),query:s,type:`file`,limit:20}).then(e=>e.data).catch(()=>[]),l=[...a,...c.map(e=>({absolute:fl(e.path,o),type:`file`}))];return{query:e,items:Array.from(new Map(l.map(e=>[e.absolute,e])).values()).slice(0,8)}}),De=f(()=>gl(Ee.latest,k()));async function Oe(e,t,n=!1){let r=e.replace(/\/+$/,``);me(!1);let a=Cl(D(),r),o=_e.get(r);o&&!n&&j.promote(`${t}:${r}`);let s=o??j.schedule(`${t}:${r}`,n?`background`:`user`,()=>{if(!yl(t,xe))return Promise.resolve(void 0);let e=Ce();return e?Pl(i,e,a).catch(()=>void 0):Promise.resolve(void 0)});_e.set(r,s);let c=await s;if(!yl(t,xe))return!1;if(!c)return _e.delete(r),r||me(!0),!1;if(M?.batch(v.entries(r,c).map(e=>({type:`add`,path:e}))),!n&&vl(ve,r))for(let e of _l(r,c))Oe(e,t,!0);return!0}async function ke(e){let t=v.navigation(dl(El(e),N(),D()||we()||N()));if(!t)return;let n=++xe;fe(!0),ge(!1),oe(``),ce(!1),ue(-1),ie(t),A(Nl(t,t,N())),_e.clear(),ve.clear(),M?.resetPaths([]);let r=await Oe(``,n);yl(n,xe)&&(ge(r),fe(!1))}function P(){let e=De(),t=e[le()]??e[0];if(!t)return;let n=Nl(t.absolute,k(),N());A(t.type===`directory`&&!n.endsWith(`/`)?n+`/`:n),t.type===`file`&&(oe(v.selection(D(),ul(D(),t.absolute,N()))??``),ce(!1),ue(-1))}function Ae(e){if(e.type===`directory`){ke(e.absolute);return}A(Nl(e.absolute,k(),N())),oe(v.selection(D(),ul(D(),e.absolute,N()))??``),ce(!1),ue(-1)}function je(e){ce(!0),ue(t=>Sl(t,e,De().length))}function Me(){let e=De();return e[le()]??e[0]}let Ne=new Map([[`ArrowDown`,()=>je(1)],[`ArrowUp`,()=>je(-1)],[`Enter`,()=>{let e=Me();e&&Ae(e),e||ke(k())}],[`Tab`,P]]);function Pe(e){let t=Ne.get(e.key);t&&(e.key===`Tab`&&e.shiftKey||(e.preventDefault(),t()))}function Fe(){let t=v.result(D(),ae(),he());t&&(e.onSelect(e.multiple?[t]:t),o.close())}return p(()=>{let e=e=>{e.target instanceof Node&&be?.contains(e.target)||(ce(!1),ue(-1))};document.addEventListener(`pointerdown`,e),d(()=>document.removeEventListener(`pointerdown`,e)),M=new ol({paths:[],flattenEmptyDirectories:!1,initialExpansion:`closed`,stickyFolders:!0,unsafeCSS:`
        button[data-type="item"] {
          background: transparent !important;
          box-shadow: none !important;
        }
        button[data-type="item"]:hover {
          background: var(--v2-overlay-simple-overlay-hover) !important;
        }
        button[data-type="item"]:focus-visible {
          outline: none !important;
          box-shadow: none !important;
        }
        [data-file-tree-virtualized-scroll] {
          overscroll-behavior: contain;
          scrollbar-width: thin;
        }
      `,onExpansionChange(e){e.expanded&&Oe(e.path,xe)},onSelectionChange(e){let t=e.at(-1);oe(t?v.selection(D(),t)??``:``)}}),ye&&(M.render({containerWrapper:ye}),M.getFileTreeContainer()?.classList.add(`directory-picker-tree`))}),t(()=>{let e=we();!e||!Ce()||D()||ke(e)}),d(()=>M?.cleanUp()),u(re,{size:`large`,class:`directory-picker`,get children(){return[u(E,{get children(){return u(T,{get children(){return e.title??m.t(`command.project.open`)}})}}),u(Ll,{}),u(te,{class:`directory-picker-body pt-4!`,get children(){return[(()=>{var e=zl(),t=e.firstChild,n=be;return typeof n==`function`?a(n,e):be=e,s(e,u(O,{get value(){return k()},autofocus:!0,autocomplete:`off`,spellcheck:!1,class:`!w-full`,onInput:e=>{A(El(e.currentTarget.value)),oe(``),ce(!0),ue(-1)},role:`combobox`,"aria-autocomplete":`list`,get"aria-expanded"(){return se()},"aria-controls":`directory-picker-suggestions`,get"aria-activedescendant"(){return _(()=>le()>=0)()?`directory-picker-suggestion-${le()}`:void 0},onKeyDown:Pe}),t),s(t,u(w,{size:`small`,variant:`ghost`,onClick:()=>void ke(N()),children:`~`}),null),s(t,u(w,{size:`small`,variant:`ghost`,onClick:()=>void ke(Al(D())||D()),get children(){return m.t(`dialog.directory.root`)}}),null),s(t,u(w,{size:`small`,variant:`ghost`,onClick:()=>void ke(jl(D())),get children(){return m.t(`dialog.directory.parent`)}}),null),s(e,u(l,{get when(){return _(()=>!!se())()&&De().length>0},get children(){var e=Rl();return s(e,u(c,{get each(){return De()},children:(e,t)=>(()=>{var n=Ul();return n.$$click=()=>Ae(e),n.$$pointermove=()=>ue(t()),s(n,()=>Nl(e.absolute,k(),N()),null),s(n,()=>e.type===`directory`?`/`:``,null),b(r=>{var i=`directory-picker-suggestion-${t()}`,a=e.absolute,o=t()===le(),s=t()===le()?``:void 0;return i!==r.e&&g(n,`id`,r.e=i),a!==r.t&&g(n,`data-directory-path`,r.t=a),o!==r.a&&g(n,`aria-selected`,r.a=o),s!==r.o&&g(n,`data-active`,r.o=s),r},{e:void 0,t:void 0,a:void 0,o:void 0}),n})()})),e}}),null),e})(),(()=>{var e=Vl();e.addEventListener(`wheel`,e=>{let t=M?.getFileTreeContainer()?.shadowRoot?.querySelector(`[data-file-tree-virtualized-scroll]`);if(!t)return;let n=xl(t.scrollTop,e.deltaY,t.scrollHeight,t.clientHeight);n!==t.scrollTop&&(e.preventDefault(),t.scrollTop=n,t.dispatchEvent(new Event(`scroll`)))}),n(e,`touchmove`,e=>e.stopPropagation());var t=ye;return typeof t==`function`?a(t,e):ye=e,s(e,u(l,{get when(){return de()},get children(){var e=Bl();return s(e,()=>m.t(`common.loading`)),e}}),null),s(e,u(l,{get when(){return _(()=>!de())()&&pe()},get children(){var e=Bl();return s(e,()=>m.t(`dialog.directory.readError`)),e}}),null),e})(),(()=>{var e=Hl();return s(e,()=>v.result(D(),ae(),he())),e})()]}}),u(ne,{get children(){return[u(w,{variant:`neutral`,onClick:()=>o.close(),get children(){return m.t(`common.cancel`)}}),u(w,{variant:`contrast`,get disabled(){return!v.result(D(),ae(),he())},onClick:Fe,get children(){return ee[v.action]}})]}})]}})}r([`pointermove`,`click`]);export{Wl as DirectoryPickerDialog};