import{i as e,n as t,r as n,t as r}from"./iconResolver-LI_36__T.js";var i=`file-tree-container`,a=`data-file-tree-style`,o=`data-file-tree-unsafe-css`,s=`data-file-tree-scrollbar-measure`,c=`data-file-tree-scrollbar-gutter-measured`,l=`--trees-scrollbar-gutter-measured`,u=`header`,d=`context-menu`,f=`context-menu-trigger`,p=`@layer base {
  :host {
    --trees-accent: var(--trees-accent-override, #009fff);
    --trees-fg: var(--trees-fg-override, var(--trees-theme-sidebar-fg, light-dark(#6c6c71, #adadb1)));
    --trees-fg-muted: var(--trees-fg-muted-override, var(--trees-theme-sidebar-header-fg, light-dark(#84848a, #84848a)));
    --trees-bg: var(--trees-bg-override, var(--trees-theme-sidebar-bg, light-dark(#f8f8f8, #141415)));
    --trees-bg-muted: var(--trees-bg-muted-override, var(--trees-theme-list-hover-bg, light-dark(color-mix(in lab,
            var(--trees-accent) var(--trees-bg-alpha-light, 8%),
            var(--trees-bg)), color-mix(in lab,
            var(--trees-accent) var(--trees-bg-alpha-dark, 10%),
            var(--trees-bg)))));
    --trees-input-bg: var(--trees-input-bg-override, light-dark(#f8f8f8, #070707));
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
    --trees-border-color: var(--trees-border-color-override, var(--trees-theme-sidebar-border, light-dark(#eeeeef, #070707)));
    --trees-indent-guide-bg: var(--trees-indent-guide-bg-override, color-mix(in lab, var(--trees-fg-muted) 25%, transparent));
    --trees-density: var(--trees-density-override, 1);
    --trees-border-radius: var(--trees-border-radius-override, calc(6px * var(--trees-density)));
    --trees-font-family: var(--trees-font-family-override, system-ui);
    --trees-font-size: var(--trees-font-size-override, 13px);
    --trees-font-weight-regular: var(--trees-font-weight-regular-override, 400);
    --trees-font-weight-semibold: var(--trees-font-weight-semibold-override, 600);
    --trees-focus-ring-color: var(--trees-focus-ring-color-override, var(--trees-theme-focus-ring, var(--trees-accent)));
    --trees-focus-ring-width: var(--trees-focus-ring-width-override, 1px);
    --trees-focus-ring-offset: var(--trees-focus-ring-offset-override, -1px);
    --trees-search-fg: var(--trees-search-fg-override, var(--trees-theme-input-fg, var(--trees-fg)));
    --trees-search-font-weight: var(--trees-search-font-weight-override, 600);
    --trees-search-bg: var(--trees-search-bg-override, var(--trees-theme-input-bg, var(--trees-input-bg)));
    --trees-scrollbar-thumb: var(--trees-scrollbar-thumb-override, var(--trees-theme-scrollbar-thumb, color-mix(in lab, var(--trees-fg) 25%, var(--trees-bg))));
    --trees-selected-fg: var(--trees-selected-fg-override, var(--trees-theme-list-active-selection-fg, var(--trees-fg)));
    --trees-selected-bg: var(--trees-selected-bg-override, var(--trees-theme-list-active-selection-bg, light-dark(color-mix(in lab, var(--trees-accent) 12%, var(--trees-bg)), color-mix(in lab, var(--trees-accent) 15%, var(--trees-bg)))));
    --trees-selected-focused-border-color: var(--trees-selected-focused-border-color-override, var(--trees-theme-focus-ring, var(--trees-accent)));
    --trees-status-added: var(--trees-status-added-override, var(--trees-theme-git-added-fg, light-dark(var(--trees-added-light), var(--trees-added-dark))));
    --trees-status-ignored: var(--trees-status-ignored-override, var(--trees-theme-git-ignored-fg, light-dark(var(--trees-ignored-light), var(--trees-ignored-dark))));
    --trees-status-modified: var(--trees-status-modified-override, var(--trees-theme-git-modified-fg, light-dark(var(--trees-modified-light), var(--trees-modified-dark))));
    --trees-status-renamed: var(--trees-status-renamed-override, var(--trees-theme-git-renamed-fg, light-dark(var(--trees-renamed-light), var(--trees-renamed-dark))));
    --trees-status-untracked: var(--trees-status-untracked-override, var(--trees-theme-git-untracked-fg, light-dark(var(--trees-untracked-light), var(--trees-untracked-dark))));
    --trees-status-deleted: var(--trees-status-deleted-override, var(--trees-theme-git-deleted-fg, light-dark(var(--trees-deleted-light), var(--trees-deleted-dark))));
    --trees-git-modified-color: var(--trees-git-modified-color-override, var(--trees-status-modified));
    --trees-git-added-color: var(--trees-git-added-color-override, var(--trees-status-added));
    --trees-git-ignored-color: var(--trees-git-ignored-color-override, var(--trees-status-ignored));
    --trees-git-deleted-color: var(--trees-git-deleted-color-override, var(--trees-status-deleted));
    --trees-git-renamed-color: var(--trees-git-renamed-color-override, var(--trees-status-renamed));
    --trees-git-untracked-color: var(--trees-git-untracked-color-override, var(--trees-status-untracked));
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
    --trees-file-icon-color-default: var(--trees-file-icon-color, var(--trees-icon-gray));
    --trees-file-icon-color-astro: var(--trees-file-icon-color, var(--trees-icon-purple));
    --trees-file-icon-color-babel: var(--trees-file-icon-color, var(--trees-icon-yellow));
    --trees-file-icon-color-bash: var(--trees-file-icon-color, var(--trees-icon-green));
    --trees-file-icon-color-biome: var(--trees-file-icon-color, var(--trees-icon-blue));
    --trees-file-icon-color-bootstrap: var(--trees-file-icon-color, var(--trees-icon-indigo));
    --trees-file-icon-color-browserslist: var(--trees-file-icon-color, var(--trees-icon-yellow));
    --trees-file-icon-color-bun: var(--trees-file-icon-color, var(--trees-icon-mauve));
    --trees-file-icon-color-c: var(--trees-file-icon-color, var(--trees-icon-blue));
    --trees-file-icon-color-cpp: var(--trees-file-icon-color, var(--trees-icon-blue));
    --trees-file-icon-color-claude: var(--trees-file-icon-color, var(--trees-icon-orange));
    --trees-file-icon-color-css: var(--trees-file-icon-color, var(--trees-icon-indigo));
    --trees-file-icon-color-database: var(--trees-file-icon-color, var(--trees-icon-purple));
    --trees-file-icon-color-docker: var(--trees-file-icon-color, var(--trees-icon-blue));
    --trees-file-icon-color-eslint: var(--trees-file-icon-color, var(--trees-icon-indigo));
    --trees-file-icon-color-git: var(--trees-file-icon-vermilion, var(--trees-icon-vermilion));
    --trees-file-icon-color-go: var(--trees-file-icon-color, var(--trees-icon-cyan));
    --trees-file-icon-color-graphql: var(--trees-file-icon-color, var(--trees-icon-pink));
    --trees-file-icon-color-html: var(--trees-file-icon-color, var(--trees-icon-orange));
    --trees-file-icon-color-image: var(--trees-file-icon-color, var(--trees-icon-pink));
    --trees-file-icon-color-javascript: var(--trees-file-icon-color, var(--trees-icon-yellow));
    --trees-file-icon-color-json: var(--trees-file-icon-color, var(--trees-icon-orange));
    --trees-file-icon-color-markdown: var(--trees-file-icon-color, var(--trees-icon-green));
    --trees-file-icon-color-mcp: var(--trees-file-icon-color, var(--trees-icon-teal));
    --trees-file-icon-color-npm: var(--trees-file-icon-color, var(--trees-icon-red));
    --trees-file-icon-color-oxc: var(--trees-file-icon-cyan, var(--trees-icon-cyan));
    --trees-file-icon-color-postcss: var(--trees-file-icon-color, var(--trees-icon-red));
    --trees-file-icon-color-prettier: var(--trees-file-icon-color, var(--trees-icon-teal));
    --trees-file-icon-color-python: var(--trees-file-icon-color, var(--trees-icon-blue));
    --trees-file-icon-color-react: var(--trees-file-icon-color, var(--trees-icon-cyan));
    --trees-file-icon-color-ruby: var(--trees-file-icon-color, var(--trees-icon-red));
    --trees-file-icon-color-rust: var(--trees-file-icon-color, var(--trees-icon-orange));
    --trees-file-icon-color-sass: var(--trees-file-icon-color, var(--trees-icon-pink));
    --trees-file-icon-color-svg: var(--trees-file-icon-color, var(--trees-icon-orange));
    --trees-file-icon-color-svelte: var(--trees-file-icon-color, var(--trees-icon-red));
    --trees-file-icon-color-svgo: var(--trees-file-icon-color, var(--trees-icon-green));
    --trees-file-icon-color-swift: var(--trees-file-icon-color, var(--trees-icon-orange));
    --trees-file-icon-color-table: var(--trees-file-icon-color, var(--trees-icon-teal));
    --trees-file-icon-color-text: var(--trees-file-icon-color, var(--trees-icon-gray));
    --trees-file-icon-color-tailwind: var(--trees-file-icon-color, var(--trees-icon-cyan));
    --trees-file-icon-color-terraform: var(--trees-file-icon-color, var(--trees-icon-indigo));
    --trees-file-icon-color-typescript: var(--trees-file-icon-color, var(--trees-icon-blue));
    --trees-file-icon-color-vite: var(--trees-file-icon-color, var(--trees-icon-purple));
    --trees-file-icon-color-vscode: var(--trees-file-icon-color, var(--trees-icon-blue));
    --trees-file-icon-color-vue: var(--trees-file-icon-color, var(--trees-icon-green));
    --trees-file-icon-color-wasm: var(--trees-file-icon-color, var(--trees-icon-indigo));
    --trees-file-icon-color-webpack: var(--trees-file-icon-color, var(--trees-icon-blue));
    --trees-file-icon-color-yml: var(--trees-file-icon-color, var(--trees-icon-red));
    --trees-file-icon-color-zig: var(--trees-file-icon-color, var(--trees-icon-orange));
    --trees-file-icon-color-zip: var(--trees-file-icon-color, var(--trees-icon-orange));
    --trees-level-gap: var(--trees-level-gap-override, calc(8px * var(--trees-density)));
    --trees-item-padding-x: var(--trees-item-padding-x-override, calc(8px * var(--trees-density)));
    --trees-item-margin-x: var(--trees-item-margin-x-override, calc(2px * var(--trees-density)));
    --trees-item-row-gap: var(--trees-item-row-gap-override, calc(6px * var(--trees-density)));
    --trees-icon-width: var(--trees-icon-width-override, 16px);
    --trees-icon-nudge: var(--trees-icon-nudge-override, calc(1px * var(--trees-density)));
    --trees-row-height: var(--trees-item-height, 30px);
    --trees-git-lane-width: var(--trees-git-lane-width-override, 12px);
    --trees-action-lane-width: var(--trees-action-lane-width-override, calc(var(--trees-icon-width) + 2px));
    --trees-context-menu-trigger-inline-offset: calc(var(--trees-padding-inline) + var(--trees-item-padding-x) -
        var(--trees-focus-ring-width));
    --trees-scrollbar-gutter: var(--trees-scrollbar-gutter-override, 6px);
    --trees-padding-inline: var(--trees-padding-inline-override, 16px);
    color-scheme: light dark;
    font-size: var(--trees-font-size);
    color: var(--trees-fg);
    background-color: var(--trees-bg);
    --truncate-marker-background-color: var(--trees-bg);
    --truncate-marker-background-overlay-color: transparent;
    font-family: var(--trees-font-family);
    font-weight: var(--trees-font-weight-regular);
    flex-direction: column;
    display: flex;
  }

  :host([data-file-tree-virtualized="true"]) {
    height: 100%;
    overflow: hidden;
  }

  [data-file-tree-virtualized-wrapper="true"], [data-file-tree-virtualized-root="true"] {
    flex-direction: column;
    height: 100%;
    display: flex;
    overflow: hidden;
  }

  [data-file-tree-virtualized-scroll="true"], [data-file-tree-scrollbar-measure="true"] {
    --trees-scrollbar-thumb-current: transparent;
    scrollbar-gutter: stable;
    overflow-y: auto;

    &:hover {
      --trees-scrollbar-thumb-current: var(--trees-scrollbar-thumb);
    }

    &::-webkit-scrollbar {
      width: var(--trees-scrollbar-gutter);
      height: var(--trees-scrollbar-gutter);
    }

    &::-webkit-scrollbar-track {
      background: none;
    }

    &::-webkit-scrollbar-thumb {
      background-color: var(--trees-scrollbar-thumb-current);
      border-radius: calc(var(--trees-scrollbar-gutter) / 2);
      background-clip: content-box;
      border: 1px solid #0000;
    }

    &::-webkit-scrollbar-corner {
      background-color: #0000;
    }
  }

  [data-file-tree-scrollbar-measure="true"] {
    visibility: hidden;
    pointer-events: none;
    width: 100px;
    height: 100px;
    position: absolute;
    top: 0;
    left: 0;
  }

  @supports ((-moz-appearance: none)) {
    [data-file-tree-virtualized-scroll="true"], [data-file-tree-scrollbar-measure="true"] {
      scrollbar-width: thin;
      scrollbar-color: var(--trees-scrollbar-thumb-current) transparent;
    }
  }

  [data-file-tree-virtualized-scroll="true"] {
    min-height: 0;
    padding-inline: max(calc(var(--trees-padding-inline) - var(--trees-item-margin-x)),
        0px)
      /* NOTE(amadeus): We can assume that all Webkit based browser gutters
       * will align to the value of '--trees-scrollbar-gutter', however if not, then
       * \`--trees-scrollbar-gutter-measured\` should correct it. Mostly we are
       * hoping to avoid SSR alignment jumps if possible. In non-SSR'd environments
       * \`--trees-scrollbar-gutter-measured\` should always be immediately available.
       */
      max(calc(var(--trees-padding-inline) - var(--trees-item-margin-x) -
            var(--trees-scrollbar-gutter-measured, var(--trees-scrollbar-gutter))),
        0px);
    flex: 1 1 0;
    position: relative;
    overflow-y: auto;
  }

  @supports ((-moz-appearance: none)) {
    [data-file-tree-virtualized-scroll="true"] {
      padding-inline: max(calc(var(--trees-padding-inline) - var(--trees-item-margin-x)),
          0px)
        /* NOTE(amadeus): However on Firefox it can vary a little bit, but most
         * likely the majority of cases will default to a 0px width scrollbar lets
         * inherit that first to avoid SSR jumps. In non-SSR'd environments
         * \`--trees-scrollbar-gutter-measured\` should always be immediately available.
         */
        max(calc(var(--trees-padding-inline) - var(--trees-item-margin-x) -
              var(--trees-scrollbar-gutter-measured, 0px)),
          0px);
    }
  }

  [data-file-tree-sticky-overlay="true"] {
    z-index: 4;
    pointer-events: none;
    height: 0;
    position: sticky;
    top: 0;
    overflow: visible;
  }

  [data-file-tree-virtualized-root="true"][data-scroll-at-top="true"]:not([data-overlay-reveal]) [data-file-tree-sticky-overlay="true"] {
    visibility: hidden;
  }

  [data-file-tree-sticky-overlay-content="true"] {
    background-color: var(--trees-bg);
    pointer-events: none;
    position: relative;
  }

  [data-file-tree-virtualized-list="true"] {
    background-color: var(--trees-bg);
    overflow-anchor: none;
    width: 100%;
    min-height: 100%;
    position: relative;

    &[data-is-scrolling] {
      pointer-events: none;
    }
  }

  [data-file-tree-virtualized-sticky-offset="true"] {
    contain: layout size;
  }

  [data-file-tree-virtualized-sticky="true"] {
    isolation: isolate;
    will-change: transform;
    flex-direction: column;
    width: 100%;
    display: flex;
    position: sticky;
    top: 0;
  }

  [data-file-tree-search-container] {
    padding: 0;
    padding-inline: var(--trees-padding-inline);
    margin-bottom: var(--trees-item-row-gap);
    display: flex;
  }

  [data-file-tree-search-input] {
    --trees-focus-ring-width: 2px;
    font-family: var(--trees-font-family);
    font-size: var(--trees-font-size);
    height: var(--trees-row-height);
    padding-inline: var(--trees-item-padding-x);
    line-height: var(--trees-row-height);
    color: var(--trees-search-fg);
    background-color: var(--trees-search-bg);
    border: 1px solid var(--trees-border-color);
    border-radius: var(--trees-border-radius);
    outline: none;
    flex: 1;
    margin-block: 1px;

    &::placeholder {
      color: color-mix(in lab,
        var(--trees-search-fg) 65%,
        var(--trees-search-bg));
    }

    &:focus-visible, &[data-file-tree-search-input-fake-focus="true"] {
      outline: var(--trees-focus-ring-width) solid var(--trees-focus-ring-color);
      outline-offset: var(--trees-focus-ring-offset);
    }
  }

  [role="tree"] {
    gap: var(--trees-gap-override, 0);
    flex-direction: column;
    display: flex;
    position: relative;
  }

  [data-type="item"] {
    color: inherit;
    font-family: var(--trees-font-family);
    font-size: var(--trees-font-size);
    text-align: start;
    background-color: var(--trees-bg);
    padding: 0 var(--trees-item-padding-x);
    margin: 0 var(--trees-item-margin-x);
    cursor: pointer;
    user-select: none;
    -webkit-touch-callout: none;
    touch-action: manipulation;
    flex: 0 0 var(--trees-row-height);
    height: var(--trees-row-height);
    line-height: var(--trees-row-height);
    align-items: center;
    gap: var(--trees-item-row-gap);
    border-radius: var(--trees-border-radius);
    --truncate-marker-background-color: var(--trees-bg);
    --truncate-marker-background-overlay-color: transparent;
    --truncate-marker-block-inset: 0px;
    border: none;
    outline: none;
    display: flex;
    position: relative;

    &:hover, &[data-item-context-hover="true"] {
      background-color: var(--trees-bg-muted);
      --truncate-marker-background-overlay-color: var(--trees-bg-muted);
    }

    &[data-item-focused="true"], &:focus-visible {
      z-index: 2;

      & [data-item-flattened-subitems] {
        --truncate-marker-block-inset: var(--trees-focus-ring-width);
      }

      &:before {
        content: "";
        border-radius: var(--trees-border-radius);
        outline: var(--trees-focus-ring-width) solid
          var(--trees-focus-ring-color);
        outline-offset: var(--trees-focus-ring-offset);
        pointer-events: none;
        display: block;
        position: absolute;
        inset: 0;
      }

      &[data-item-selected="true"]:before {
        outline-color: var(--trees-selected-focused-border-color);
      }
    }

    &[data-item-selected="true"] {
      color: var(--trees-selected-fg);
      background-color: var(--trees-selected-bg);
      --truncate-marker-background-overlay-color: var(--trees-selected-bg);
      z-index: 3;

      & [data-item-section="icon"] {
        color: var(--trees-selected-fg);
      }
    }

    &[data-item-search-match="true"] {
      font-weight: var(--trees-search-font-weight);
    }
  }

  [data-type="item"][data-file-tree-sticky-row="true"] {
    pointer-events: auto;
  }

  [data-file-tree-virtualized-root="true"][data-is-scrolling] [data-type="item"][data-file-tree-sticky-row="true"] {
    pointer-events: none;
  }

  [data-file-tree-virtualized-root="true"][data-is-scrolling] [data-type="item"][data-file-tree-sticky-row="true"]:hover:not([data-item-selected="true"]), [data-file-tree-virtualized-root="true"][data-is-scrolling] [data-type="item"][data-file-tree-sticky-row="true"][data-item-context-hover="true"]:not([data-item-selected="true"]) {
    background-color: var(--trees-bg);
    --truncate-marker-background-overlay-color: transparent;
  }

  [data-item-selected="true"]:has( + [data-item-selected="true"]) {
    border-bottom-right-radius: 0;
    border-bottom-left-radius: 0;
  }

  [data-item-selected="true"] + [data-item-selected="true"] {
    border-top-left-radius: 0;
    border-top-right-radius: 0;
  }

  [data-item-flattened-subitems] {
    align-items: center;
    gap: 2px;
    display: inline-flex;
  }

  [data-item-flattened-subitem]:hover, [data-item-flattened-subitem-drag-target="true"] {
    text-decoration: underline;
  }

  [data-item-section="icon"] {
    color: var(--trees-fg-muted);
    fill: currentColor;
    width: var(--trees-icon-width);
    flex-shrink: 0;
    justify-content: center;
    align-items: center;
    display: flex;
  }

  :where([data-item-section="icon"] > [data-icon-token]) {
    color: var(--trees-fg-muted);
  }

  [data-file-tree-colored-icons="true"] {
    & [data-icon-token="astro"] {
      color: var(--trees-file-icon-color-astro);
    }

    & [data-icon-token="babel"] {
      color: var(--trees-file-icon-color-babel);
    }

    & [data-icon-token="bash"] {
      color: var(--trees-file-icon-color-bash);
    }

    & [data-icon-token="biome"] {
      color: var(--trees-file-icon-color-biome);
    }

    & [data-icon-token="bootstrap"] {
      color: var(--trees-file-icon-color-bootstrap);
    }

    & [data-icon-token="browserslist"] {
      color: var(--trees-file-icon-color-browserslist);
    }

    & [data-icon-token="bun"] {
      color: var(--trees-file-icon-color-bun);
    }

    & [data-icon-token="c"] {
      color: var(--trees-file-icon-color-c);
    }

    & [data-icon-token="cpp"] {
      color: var(--trees-file-icon-color-cpp);
    }

    & [data-icon-token="claude"] {
      color: var(--trees-file-icon-color-claude);
    }

    & [data-icon-token="css"] {
      color: var(--trees-file-icon-color-css);
    }

    & [data-icon-token="database"] {
      color: var(--trees-file-icon-color-database);
    }

    & [data-icon-token="default"] {
      color: var(--trees-file-icon-color-default);
    }

    & [data-icon-token="docker"] {
      color: var(--trees-file-icon-color-docker);
    }

    & [data-icon-token="eslint"] {
      color: var(--trees-file-icon-color-eslint);
    }

    & [data-icon-token="git"] {
      color: var(--trees-file-icon-color-git);
    }

    & [data-icon-token="go"] {
      color: var(--trees-file-icon-color-go);
    }

    & [data-icon-token="graphql"] {
      color: var(--trees-file-icon-color-graphql);
    }

    & [data-icon-token="html"] {
      color: var(--trees-file-icon-color-html);
    }

    & [data-icon-token="image"] {
      color: var(--trees-file-icon-color-image);
    }

    & [data-icon-token="javascript"] {
      color: var(--trees-file-icon-color-javascript);
    }

    & [data-icon-token="json"] {
      color: var(--trees-file-icon-color-json);
    }

    & [data-icon-token="markdown"] {
      color: var(--trees-file-icon-color-markdown);
    }

    & [data-icon-token="mcp"] {
      color: var(--trees-file-icon-color-mcp);
    }

    & [data-icon-token="npm"] {
      color: var(--trees-file-icon-color-npm);
    }

    & [data-icon-token="oxc"] {
      color: var(--trees-file-icon-color-oxc);
    }

    & [data-icon-token="postcss"] {
      color: var(--trees-file-icon-color-postcss);
    }

    & [data-icon-token="prettier"] {
      color: var(--trees-file-icon-color-prettier);
    }

    & [data-icon-token="python"] {
      color: var(--trees-file-icon-color-python);
    }

    & [data-icon-token="react"] {
      color: var(--trees-file-icon-color-react);
    }

    & [data-icon-token="ruby"] {
      color: var(--trees-file-icon-color-ruby);
    }

    & [data-icon-token="rust"] {
      color: var(--trees-file-icon-color-rust);
    }

    & [data-icon-token="sass"] {
      color: var(--trees-file-icon-color-sass);
    }

    & [data-icon-token="svg"] {
      color: var(--trees-file-icon-color-svg);
    }

    & [data-icon-token="svelte"] {
      color: var(--trees-file-icon-color-svelte);
    }

    & [data-icon-token="svgo"] {
      color: var(--trees-file-icon-color-svgo);
    }

    & [data-icon-token="swift"] {
      color: var(--trees-file-icon-color-swift);
    }

    & [data-icon-token="table"] {
      color: var(--trees-file-icon-color-table);
    }

    & [data-icon-token="text"] {
      color: var(--trees-file-icon-color-text);
    }

    & [data-icon-token="tailwind"] {
      color: var(--trees-file-icon-color-tailwind);
    }

    & [data-icon-token="terraform"] {
      color: var(--trees-file-icon-color-terraform);
    }

    & [data-icon-token="typescript"] {
      color: var(--trees-file-icon-color-typescript);
    }

    & [data-icon-token="vite"] {
      color: var(--trees-file-icon-color-vite);
    }

    & [data-icon-token="vscode"] {
      color: var(--trees-file-icon-color-vscode);
    }

    & [data-icon-token="vue"] {
      color: var(--trees-file-icon-color-vue);
    }

    & [data-icon-token="wasm"] {
      color: var(--trees-file-icon-color-wasm);
    }

    & [data-icon-token="webpack"] {
      color: var(--trees-file-icon-color-webpack);
    }

    & [data-icon-token="yml"] {
      color: var(--trees-file-icon-color-yml);
    }

    & [data-icon-token="zig"] {
      color: var(--trees-file-icon-color-zig);
    }

    & [data-icon-token="zip"] {
      color: var(--trees-file-icon-color-zip);
    }
  }

  [data-icon-name="file-tree-icon-chevron"] {
    &[data-align-capitals="false"] {
      transform: translate(0, var(--trees-icon-nudge));
    }

    &[data-align-capitals="true"] {
      transform: translate(0);
    }
  }

  [data-item-section="content"] {
    text-align: start;
    text-overflow: ellipsis;
    flex: 0 auto;
    min-width: 0;
    max-width: 100%;
    overflow: hidden;
  }

  [data-item-section="decoration"] {
    text-align: end;
    min-width: 0;
    color: var(--trees-fg-muted);
    flex: 1 1 0;
    justify-content: flex-end;
    display: flex;
    overflow: hidden;
  }

  [data-item-section="decoration"] > span {
    text-overflow: ellipsis;
    white-space: nowrap;
    justify-content: flex-end;
    align-items: center;
    min-width: 0;
    max-width: 100%;
    display: inline-flex;
    overflow: hidden;
  }

  [data-item-section="git"], [data-item-section="action"] {
    flex: none;
    justify-content: center;
    align-items: center;
    display: flex;
  }

  [data-item-section="git"] {
    width: var(--trees-git-lane-width);
  }

  [data-item-section="action"] {
    width: var(--trees-action-lane-width);
    color: var(--trees-fg-muted);
    fill: currentColor;
    pointer-events: none;
  }

  [data-item-section="git"] > span, [data-item-section="action"] > span {
    justify-content: center;
    align-items: center;
    width: 100%;
    display: inline-flex;
  }

  [data-item-action-affordance="decorative"] {
    opacity: .85;
  }

  [data-item-rename-input] {
    appearance: none;
    width: 100%;
    min-width: 0;
    height: calc(var(--trees-row-height) - 4px);
    font-family: inherit;
    font-size: inherit;
    color: inherit;
    box-sizing: border-box;
    background-color: #0000;
    border: 0;
    outline: none;
    padding-inline: 6px;
  }

  [data-item-section="content"]:has([data-item-rename-input]) ~ [data-item-section="action"], [data-item-section="content"]:has([data-item-rename-input]) ~ [data-item-section="decoration"] {
    display: none;
  }

  [aria-expanded="false"][data-item-type="folder"] > [data-item-section="icon"] > [data-icon-name="file-tree-icon-chevron"] {
    &[data-align-capitals="true"] {
      transform: rotate(-90deg)
        translate(calc(var(--trees-icon-nudge) / 2),
          calc(var(--trees-icon-nudge) / 2));
    }

    &[data-align-capitals="false"] {
      transform: rotate(-90deg)
        translate(calc(var(--trees-icon-nudge) / 2 * -1),
          calc(var(--trees-icon-nudge) / 2));
    }
  }

  [data-item-section="spacing"] {
    height: var(--trees-row-height);
    padding-left: calc(calc(var(--trees-icon-width) / 2) - .5px);
    flex-direction: row;
    justify-content: center;
    align-items: center;
    display: flex;

    &:empty {
      padding-left: 0;
    }
  }

  [data-item-section="spacing-item"] {
    border-left: 1px solid var(--trees-indent-guide-bg);
    height: 100%;
    margin-right: calc(var(--trees-level-gap) - 1px);
    opacity: 0;
    transition: opacity .15s;
    display: inline-block;
    transform: translateX(-.25px);

    & + & {
      margin-left: calc(var(--trees-item-row-gap) + calc(var(--trees-icon-width) / 2) - .5px);
    }
  }

  :host(:hover) [data-item-section="spacing-item"] {
    opacity: .75;
  }

  [data-item-contains-git-change="true"] > [data-item-section="git"] {
    color: var(--trees-git-modified-color);
    opacity: .5;
    fill: currentColor;
  }

  [data-item-git-status] {
    & > :where([data-item-section="icon"]) > :where(:not([data-icon-name="file-tree-icon-chevron"])), & > [data-item-section="content"] {
      color: var(--trees-item-git-status-color);
    }

    & > [data-item-section="git"] {
      color: var(--trees-item-git-status-color);
      font-weight: var(--trees-font-weight-semibold);
    }
  }

  [data-item-git-status="added"] {
    --trees-item-git-status-color: var(--trees-git-added-color);
  }

  [data-item-git-status="deleted"] {
    --trees-item-git-status-color: var(--trees-git-deleted-color);
  }

  [data-item-git-status="ignored"] {
    --trees-item-git-status-color: var(--trees-git-ignored-color);

    & > [data-item-section="icon"] {
      opacity: .5;
    }
  }

  [data-item-section="git"] [data-icon-name="file-tree-icon-dot"] {
    transform: translateY(calc(.65ex - 50%));
  }

  [data-item-git-status="modified"] {
    --trees-item-git-status-color: var(--trees-git-modified-color);
  }

  [data-item-git-status="renamed"] {
    --trees-item-git-status-color: var(--trees-git-renamed-color);
  }

  [data-item-git-status="untracked"] {
    --trees-item-git-status-color: var(--trees-git-untracked-color);
  }

  [data-item-drag-target="true"] {
    background-color: var(--trees-selected-bg);
  }

  [data-item-dragging="true"] {
    opacity: .5;
  }

  [data-item-section="lock"] {
    color: var(--trees-fg-muted);
    flex: none;
    align-items: center;
    margin-left: auto;
    display: flex;
  }

  [data-item-section="lock"] svg {
    display: block;
  }

  [data-type="header-slot"] {
    flex: none;
    display: block;
  }

  [data-type="context-menu-wash"] {
    z-index: 3;
    touch-action: none;
    background-color: #0000;
    position: absolute;
    inset: 0;
  }

  [data-type="context-menu-anchor"] {
    top: 0;
    right: var(--trees-context-menu-trigger-inline-offset);
    z-index: 4;
    align-items: center;
    display: none;
    position: absolute;

    &[data-visible="true"] {
      display: flex;
    }
  }

  [data-file-tree-virtualized-root="true"][data-is-scrolling] [data-type="context-menu-anchor"] {
    display: none;
  }

  [data-type="context-menu-anchor"] > slot[name="context-menu"] {
    flex: 0 0 0;
    width: 0;
    min-width: 0;
    display: block;
    overflow: visible;
  }

  [data-type="context-menu-trigger"] {
    all: unset;
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
    justify-content: center;
    align-items: center;
    transition: color .12s;
    display: flex;
  }

  [data-type="context-menu-trigger"]:hover, [data-type="context-menu-trigger"][aria-expanded="true"] {
    color: var(--trees-fg);
  }

  [data-truncate-container] {
    --truncate-internal-marker-fade-width: var(--truncate-marker-fade-width, 2px);
    --truncate-internal-marker-gap: var(--truncate-marker-gap, 0px);
    --truncate-internal-marker-opacity: var(--truncate-marker-opacity, 50%);
    --truncate-internal-middle-marker-opacity: var(--truncate-middle-marker-opacity, 80%);
    --truncate-internal-marker-background-color: var(--truncate-marker-background-color, light-dark(#fff, #000));
    --truncate-internal-marker-background-overlay-color: var(--truncate-marker-background-overlay-color, transparent);
    --truncate-internal-marker-block-inset: var(--truncate-marker-block-inset, 0px);
    --truncate-internal-marker-fade-out-duration: var(--truncate-marker-fade-out-duration, 0s);
    --truncate-internal-marker-fade-in-duration: var(--truncate-marker-fade-in-duration, .1s);
    --truncate-internal-fade-marker-color: var(--truncate-fade-marker-color, #000);
    --truncate-internal-fade-marker-width: var(--truncate-fade-marker-width, .2lh);
    --truncate-internal-single-line-height: 1lh;
    height: var(--truncate-internal-single-line-height);
    min-width: 0;
    margin-top: -1px;
    margin-bottom: -1px;
    overflow: hidden;
  }

  [data-truncate-marker] {
    height: var(--truncate-internal-single-line-height);
    padding-block: var(--truncate-internal-marker-block-inset);
    box-sizing: border-box;
    z-index: 2;
    color: color-mix(in srgb,
      currentColor var(--truncate-internal-marker-opacity),
      transparent);
    opacity: 0;
    transition: opacity var(--truncate-internal-marker-fade-out-duration)
      ease-in-out;
    background-clip: content-box;
    align-items: center;
    display: flex;
    position: absolute;
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

  [data-truncate-content="visible"] {
    white-space: nowrap;
  }

  [data-truncate-content="overflow"] {
    opacity: 0;
    pointer-events: none;
    user-select: none;
    word-break: break-all;
    margin-top: calc(-1 * var(--truncate-internal-single-line-height));
  }

  [data-truncate-marker-cell] {
    user-select: none;
    pointer-events: none;
    overflow: visible;
    container: measure / size;
  }

  [data-truncate-container="truncate"] {
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

  [data-truncate-container="fruncate"] {
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

  [data-truncate-variant="default"] {
    & [data-truncate-marker] {
      background-color: var(--truncate-internal-marker-background-color);
      background-image: linear-gradient(var(--truncate-internal-marker-background-overlay-color),
        var(--truncate-internal-marker-background-overlay-color));
    }

    & [data-truncate-marker]:after, & [data-truncate-marker]:before {
      content: "";
      width: calc(var(--truncate-internal-marker-fade-width) +
          var(--truncate-internal-marker-gap));
      height: max(0px,
        calc(var(--truncate-internal-single-line-height) -
            var(--truncate-internal-marker-block-inset) * 2));
      background-color: var(--truncate-internal-marker-background-color);
      background-image: linear-gradient(var(--truncate-internal-marker-background-overlay-color),
        var(--truncate-internal-marker-background-overlay-color));
      -webkit-mask-image: linear-gradient(var(--truncate-internal-fade-dir),
        #000 0%,
        #000 var(--truncate-internal-marker-gap),
        transparent 100%);
      mask-image: linear-gradient(var(--truncate-internal-fade-dir),
        #000 0%,
        #000 var(--truncate-internal-marker-gap),
        transparent 100%);
      position: absolute;
      inset-block-start: var(--truncate-internal-marker-block-inset);
    }

    & [data-truncate-marker]:after {
      --truncate-internal-fade-dir: to right;
      right: calc(-1 *
          (
            var(--truncate-internal-marker-fade-width) +
              var(--truncate-internal-marker-gap)
          ));
    }

    & [data-truncate-marker]:before {
      --truncate-internal-fade-dir: to left;
      left: calc(-1 *
          (
            var(--truncate-internal-marker-fade-width) +
              var(--truncate-internal-marker-gap)
          ));
    }
  }

  [data-truncate-variant="fade"] {
    & [data-truncate-marker] {
      background: none;
    }
  }

  [data-truncate-fade] {
    box-shadow: 0 0 calc(var(--truncate-internal-fade-marker-width) / 2)
        var(--truncate-internal-fade-marker-color),
      0 0 var(--truncate-internal-fade-marker-width)
        var(--truncate-internal-fade-marker-color);
    width: calc(var(--truncate-internal-fade-marker-width) * 2);
    height: calc(var(--truncate-internal-single-line-height) -
        (var(--truncate-internal-fade-marker-width) * 2));
    margin: var(--truncate-internal-fade-marker-width) 0;
  }

  [data-truncate-group-container="middle"] {
    & [data-truncate-container] {
      --truncate-marker-opacity: var(--truncate-internal-middle-marker-opacity);
    }

    min-width: 0;
    display: flex;

    & > div {
      min-width: 0;
    }

    & > div[data-truncate-segment-priority="1"] {
      flex: 0 1 max-content;
    }

    & > div[data-truncate-segment-priority="2"] {
      flex: 0 999999 max-content;
    }
  }
}

@layer theme, unsafe;
`,m=`@layer base, unsafe;`;function h(e){return`${m}
@layer base {
  ${e}
}`}function g(e){return`${m}
@layer unsafe {
  ${e}
}`}var _=new WeakMap;function v(e){let t=_.get(e);if(t!=null)return t;let n=document.createElement(`div`);n.setAttribute(s,`true`);let r=document.createElement(`div`);r.style.position=`relative`,r.style.height=`200%`,n.appendChild(r),e.appendChild(n);let i=Math.max(n.offsetWidth-n.clientWidth,0);return n.remove(),_.set(e,i),i}function y(e,t){if(!e.isConnected)return;let n=v(t);if(n==null)return;let r=t.querySelector(`style[${c}]`),i=r instanceof HTMLStyleElement?r:document.createElement(`style`);r instanceof HTMLStyleElement||(i.setAttribute(c,``),t.appendChild(i)),i.textContent=`:host { ${l}: ${n}px; }`}var b;function x(e){if(typeof CSSStyleSheet<`u`&&typeof CSSStyleSheet.prototype.replaceSync==`function`&&`adoptedStyleSheets`in e){b??(b=new CSSStyleSheet,b.replaceSync(h(p)));let t=!1;try{e.adoptedStyleSheets=[b],t=!0}catch{}if(t){e.querySelector(`style[${a}]`)?.remove();return}}if(e.querySelector(`style[data-file-tree-style]`)==null){let t=document.createElement(`style`);t.setAttribute(a,``),t.textContent=h(p),e.prepend(t)}}function S(e,t){C(e,t),x(t),y(e,t)}function C(e,t){let n=e.querySelector(`template[shadowrootmode="open"], template[data-file-tree-shadowrootmode="open"]`);n instanceof HTMLTemplateElement&&(t.childNodes.length>0||(t.appendChild(n.content.cloneNode(!0)),n.hasAttribute(`shadowrootmode`)&&n.remove()))}if(typeof HTMLElement<`u`&&customElements.get(`file-tree-container`)==null){class e extends HTMLElement{constructor(){super()}connectedCallback(){let e=this.shadowRoot??this.attachShadow({mode:`open`});S(this,e)}}if(customElements.define(i,e),typeof document<`u`)for(let e of Array.from(document.querySelectorAll(i)))e instanceof HTMLElement&&S(e,e.shadowRoot??e.attachShadow({mode:`open`}))}var w=5,ee=32,te=128;function T(){return{childIdByNameId:new Map,childIds:[],childPositionById:new Map,childVisibleChunkSums:null,totalChildSubtreeNodeCount:0,totalChildVisibleSubtreeCount:0}}function E(){return{childIdByNameId:null,childIds:[],childPositionById:null,childVisibleChunkSums:null,totalChildSubtreeNodeCount:0,totalChildVisibleSubtreeCount:0}}function D(e,t){if(t.childIdByNameId!=null)return t.childIdByNameId;let n=new Map;for(let r of t.childIds){let t=e[r];t!=null&&n.set(t.nameId,r)}return t.childIdByNameId=n,n}function O(e){if(e.childPositionById!=null)return e.childPositionById;let t=new Map;for(let n=0;n<e.childIds.length;n++){let r=e.childIds[n];r!=null&&t.set(r,n)}return e.childPositionById=t,t}function ne(e,t){e.childPositionById!=null&&e.childPositionById.set(t,e.childIds.length),e.childIds.push(t)}function k(e,t){if(e.childPositionById!=null)for(let n=t;n<e.childIds.length;n++){let t=e.childIds[n];t!=null&&e.childPositionById.set(t,n)}}function re(e,t){let n=0,r=0;for(let i of t.childIds){let t=e[i];t!=null&&(n+=t.subtreeNodeCount,r+=t.visibleSubtreeCount)}t.totalChildSubtreeNodeCount=n,t.totalChildVisibleSubtreeCount=r,j(e,t)}function ie(e,t,n,r){if(e.totalChildSubtreeNodeCount+=n,e.totalChildVisibleSubtreeCount+=r,e.childVisibleChunkSums==null||r===0)return;let i=O(e).get(t);if(i===void 0)return;let a=i>>w;e.childVisibleChunkSums[a]+=r}function A(e,t,n){let r=t.childVisibleChunkSums;if(r!=null){let i=n,a=0;for(let o of r){if(i<o){let r=oe(e,t,a,i);return{...r,childVisibleIndex:n-r.localVisibleIndex}}i-=o,a+=ee}throw Error(`Visible child index ${String(n)} is out of range`)}let i=n;for(let r=0;r<t.childIds.length;r++){let a=t.childIds[r];if(a==null)continue;let o=e[a];if(o!=null){if(i<o.visibleSubtreeCount)return{childIndex:r,childVisibleIndex:n-i,localVisibleIndex:i};i-=o.visibleSubtreeCount}}throw Error(`Visible child index ${String(n)} is out of range`)}function ae(e,t,n){let r=0,i=t.childVisibleChunkSums,a=0;if(i!=null){let e=n>>w;for(let t=0;t<e;t+=1)r+=i[t]??0;a=e<<w}for(let i=a;i<n;i+=1){let n=t.childIds[i];if(n==null)continue;let a=e[n];a!=null&&(r+=a.visibleSubtreeCount)}return r}function j(e,t){if(t.childIds.length<te){t.childVisibleChunkSums=null;return}let n=Math.ceil(t.childIds.length/ee),r=new Int32Array(n);for(let n=0;n<t.childIds.length;n++){let i=t.childIds[n];if(i==null)continue;let a=e[i];a!=null&&(r[n>>w]+=a.visibleSubtreeCount)}t.childVisibleChunkSums=r}function oe(e,t,n,r){let i=Math.min(t.childIds.length,n+ee),a=r;for(let r=n;r<i;r++){let n=t.childIds[r];if(n==null)continue;let i=e[n];if(i!=null){if(a<i.visibleSubtreeCount)return{childIndex:r,localVisibleIndex:a};a-=i.visibleSubtreeCount}}throw Error(`Visible child index ${String(r)} is out of range`)}var se=7,ce=3,le=8,ue=4;function M(e,t,n=0){return e<<ue|n<<ce|t}function N(e){return e.depthAndFlags>>>ue}function de(e){return(e.depthAndFlags&le)>>ce}function P(e){return(e.depthAndFlags&le)!==0}function fe(e){return e.depthAndFlags&se}function F(e,t){return(fe(e)&t)!==0}function pe(e,t){e.depthAndFlags|=t}function me(e,t){e.depthAndFlags=M(t,fe(e),de(e))}var he=Symbol(`benchmarkInstrumentation`);function ge(e,t){return t==null||Object.defineProperty(e,he,{configurable:!0,enumerable:!1,value:t,writable:!1}),e}function _e(e){return e==null?null:e[he]??null}function I(e,t,n){return e==null?n():e.measurePhase(t,n)}function ve(e,t,n){!Number.isFinite(n)||e==null||e.setCounter(t,n)}function ye(e){return e>=48&&e<=57}function be(e){let t=[],n=0,r=0;for(;r<e.length;){for(;r<e.length&&!ye(e.charCodeAt(r));)r+=1;if(r>=e.length)break;r>n&&t.push(e.slice(n,r));let i=0;for(;r<e.length&&ye(e.charCodeAt(r));)i=i*10+(e.charCodeAt(r)-48),r+=1;t.push(i),n=r}return(n<e.length||t.length===0)&&t.push(e.slice(n)),t}function xe(e){let t=e.toLowerCase();return{lowerValue:t,tokens:be(t)}}function Se(e,t){let n=Math.min(e.length,t.length);for(let r=0;r<n;r++){let n=e[r],i=t[r];if(n===i)continue;if(typeof n==`number`&&typeof i==`number`)return n<i?-1:1;let a=String(n),o=String(i);if(a!==o)return a<o?-1:1}return e.length===t.length?0:e.length<t.length?-1:1}function Ce(e,t){if(e.tokens.length===1&&t.tokens.length===1&&typeof e.tokens[0]==`string`&&typeof t.tokens[0]==`string`)return e.lowerValue===t.lowerValue?0:e.lowerValue<t.lowerValue?-1:1;let n=Se(e.tokens,t.tokens);return n===0?e.lowerValue===t.lowerValue?0:e.lowerValue<t.lowerValue?-1:1:n}function we(e,t,n){let r=Ce(n(e),n(t));return r===0?e===t?0:e<t?-1:1:r}function Te(e,t){return we(e,t,xe)}function Ee(e,t){return t===e.segments.length-1?+!!e.isDirectory:1}function De(e,t){let n=Math.min(e.segments.length,t.segments.length);for(let r=0;r<n;r++){let n=e.segments[r],i=t.segments[r];if(n===i)continue;let a=Ee(e,r);return a===Ee(t,r)?Te(n,i):a===1?-1:1}return e.segments.length===t.segments.length?e.isDirectory===t.isDirectory?0:e.isDirectory?-1:1:e.segments.length<t.segments.length?-1:1}function Oe(e,t){return De(e,t)}function ke(e,t,n){let r=e=>{let t=n.get(e);if(t!=null)return t;let r=xe(e);return n.set(e,r),r},i=Math.min(e.segments.length,t.segments.length);for(let n=0;n<i;n++){let i=e.segments[n],a=t.segments[n];if(i===a)continue;let o=Ee(e,n);return o===Ee(t,n)?we(i,a,r):o===1?-1:1}return e.segments.length===t.segments.length?e.isDirectory===t.isDirectory?0:e.isDirectory?-1:1:e.segments.length<t.segments.length?-1:1}function L(e,t){let n=e.sortKeyById[t];if(n!==void 0)return n;let r=e.valueById[t],i=xe(r);return e.sortKeyById[t]=i,i}function Ae(e={}){return{flattenEmptyDirectories:e.flattenEmptyDirectories!==!1,sort:e.sort??`default`}}function je(e){let t=e.length>0&&e.charCodeAt(e.length-1)===47,n=t?e.length-1:e.length,r=[],i=0;for(let t=0;t<n;t++)e.charCodeAt(t)===47&&(r.push(e.slice(i,t)),i=t+1);return r.push(e.slice(i,n)),{hasTrailingSlash:t,segments:r}}function Me(e){let{hasTrailingSlash:t,segments:n}=je(e);return{basename:n[n.length-1]??``,isDirectory:t,path:e,segments:n}}function Ne(e){if(e.length===0)return{requiresDirectory:!1,segments:[]};let{hasTrailingSlash:t,segments:n}=je(e);return{requiresDirectory:t,segments:n}}function Pe(){let e=new Map;return e.set(``,0),{idByValue:e,valueById:[``],sortKeyById:[xe(``)]}}function Fe(e,t){let n=e.idByValue.get(t);if(n!==void 0)return n;let r=e.valueById.length;return e.idByValue.set(t,r),e.valueById.push(t),r}function Ie(e,t){let n=e.valueById[t];if(n===void 0)throw Error(`Unknown segment ID: ${String(t)}`);return n}var Le=Symbol(`pathStorePreparedInputKind`);function Re(e,t){return e[Le]=t,e}function ze(e){return{basename:e.basename,depth:e.segments.length,isDirectory:e.isDirectory,path:e.path,segments:e.segments}}function Be(e,t,n){return n==="default"?Oe(e,t):n(ze(e),ze(t))}function Ve(){return{depthAndFlags:M(0,3,1),nameId:0,parentId:0,subtreeNodeCount:1,visibleSubtreeCount:1}}function He(e,t){let n=Math.min(e.length,t.length);for(let r=0;r<n;r++)if(e[r]!==t[r])return r;return n}function Ue(e){return e.isDirectory?e.segments.length:e.segments.length-1}function We(e){return Array.isArray(e)&&e.every(e=>typeof e==`object`&&!!e&&typeof e.path==`string`&&Array.isArray(e.segments)&&typeof e.basename==`string`&&typeof e.isDirectory==`boolean`)}function Ge(e){return Array.isArray(e)&&e.every(e=>typeof e==`string`)}function Ke(e,t={}){return Qe(e,t).map(e=>e.path)}function qe(e,t={}){let n=Qe(e,t);return Re({paths:n.map(e=>e.path),preparedPaths:n},`prepared`)}function Je(e){let t=e.length,n=!1;for(let r=0;r<t;r+=1){let t=e[r];if(t.length>0&&t.charCodeAt(t.length-1)===47){n=!0;break}}return Re({paths:e,presortedPaths:e,presortedPathsContainDirectories:n},`presorted`)}function Ye(e){let t=e,n=t.preparedPaths;if(t[Le]===`prepared`&&n!=null)return n;if(!We(n))throw Error(`preparedInput must come from PathStore.prepareInput()`);return n}function Xe(e){let t=e;return t[Le]===`presorted`&&t.presortedPaths!=null||Ge(t.presortedPaths)?t.presortedPaths:null}function Ze(e){let t=e;return typeof t.presortedPathsContainDirectories==`boolean`?t.presortedPathsContainDirectories:null}function Qe(e,t={}){let n=Ae(t),r=_e(t);ve(r,`workload.inputFiles`,e.length);let i=I(r,`store.preparePathEntries.parse`,()=>e.map(e=>Me(e)));return I(r,`store.preparePathEntries.sort`,()=>i.sort((e,t)=>Be(e,t,n.sort))),i}var $e=class{directories=new Map;directoryStack=[0];presortedDirectoryNodeIds=[];initialExpandedPathSet;createdDirectoriesAllExpanded=!1;createdDirectoryCount=0;lastPreparedPath=null;nodes=[Ve()];options;instrumentation;segmentSortKeyCache=new Map;segmentTable=Pe();hasDeferredDirectoryIndexes=!1;constructor(e={}){this.instrumentation=_e(e),this.options=Ae(e);let t=e.initialExpandedPaths??null;if(t==null||t.length===0)this.initialExpandedPathSet=null;else{let e=new Set,n=t.length;for(let r=0;r<n;r+=1){let n=t[r],i=n.length;e.add(i>0&&n.charCodeAt(i-1)===47?n.slice(0,i-1):n)}this.initialExpandedPathSet=e,this.createdDirectoriesAllExpanded=!0}this.directories.set(0,T())}appendPaths(e){return I(this.instrumentation,`store.builder.appendPaths.parse`,()=>this.appendPreparedPaths(e.map(e=>Me(e))))}appendPreparedPaths(e,t=!0){return this.createdDirectoriesAllExpanded=!1,I(this.instrumentation,`store.builder.appendPreparedPaths`,()=>{for(let n of e)this.appendPreparedPath(n,t)}),this}appendPresortedPaths(e,t=null){return I(this.instrumentation,`store.builder.appendPresortedPaths`,()=>{if(t===!1){this.appendPresortedFilePaths(e);return}this.createdDirectoriesAllExpanded=!1;let n=null,r=0,i=this.nodes,a=this.segmentTable,o=a.idByValue,s=a.valueById,c=this.directoryStack,l=0,u=``,d=0;for(let t of e){if(n===t)throw Error(`Duplicate path: "${t}"`);let e=t.length>0&&t.charCodeAt(t.length-1)===47,a=e?t.length-1:t.length,f=0,p=0;if(n!=null){if(u.length>0&&t.length>u.length&&t.startsWith(u))f=d,p=u.length;else{let r=Math.min(a,n.length),i=!0;for(let e=0;e<r;e++){let r=t.charCodeAt(e);if(r!==n.charCodeAt(e)){i=!1;break}r===47&&(f++,p=e+1)}i&&e&&r===a&&n.length>a&&n.charCodeAt(a)===47&&(f++,p=a+1)}}l=f,r=f;let m=p,h=t.indexOf(`/`,m);for(;h>=0&&h<a;){let e=c[l];if(e===void 0)throw Error(`Directory stack underflow while building the path store`);r++;let n=t.slice(m,h),a=o.get(n);a===void 0&&(a=s.length,o.set(n,a),s.push(n));let u=i.length;i.push({depthAndFlags:M(r,0,1),nameId:a,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),this.recordCreatedDirectoryPath(t.slice(0,h)),l++,c[l]=u,m=h+1,h=t.indexOf(`/`,m)}if(e){if(m<a){let e=c[l];if(e===void 0)throw Error(`Unable to resolve directory parent for "${t}"`);r++;let n=t.slice(m,a),u=o.get(n);u===void 0&&(u=s.length,o.set(n,u),s.push(n));let d=i.length;i.push({depthAndFlags:M(r,0,1),nameId:u,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),l++,c[l]=d,m=a+1}let e=c[l];if(e===void 0)throw Error(`Unable to resolve directory node for "${t}"`);this.promoteDirectoryToExplicit(e,t)}else{let e=c[l];if(e===void 0)throw Error(`Unable to resolve file parent for "${t}"`);let n=t.slice(m),a=o.get(n);a===void 0&&(a=s.length,o.set(n,a),s.push(n)),i.push({depthAndFlags:M(r+1,0),nameId:a,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1})}m!==u.length&&(u=t.substring(0,m),d=r),n=t}c.length=l+1,n!=null&&(this.lastPreparedPath=Me(n)),this.hasDeferredDirectoryIndexes=!0}),this}appendPresortedFilePaths(e){let t=null,n=0,r=this.nodes,i=this.segmentTable,a=i.idByValue,o=i.valueById,s=this.directoryStack,c=0,l=``,u=0;for(let i of e){if(t===i)throw Error(`Duplicate path: "${i}"`);let e=i.length,d=0,f=0;if(t!=null){if(l.length>0&&i.length>l.length&&i.startsWith(l))d=u,f=l.length;else{let n=Math.min(e,t.length);for(let e=0;e<n;e++){let n=i.charCodeAt(e);if(n!==t.charCodeAt(e))break;n===47&&(d++,f=e+1)}}}c=d,n=d;let p=f,m=i.indexOf(`/`,p);for(;m>=0;){let e=s[c];if(e===void 0)throw Error(`Directory stack underflow while building the path store`);n++;let t=i.slice(p,m),l=a.get(t);l===void 0&&(l=o.length,a.set(t,l),o.push(t));let u=r.length;r.push({depthAndFlags:M(n,0,1),nameId:l,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),this.recordCreatedDirectoryPath(i.slice(0,m)),this.presortedDirectoryNodeIds.push(u),c++,s[c]=u,p=m+1,m=i.indexOf(`/`,p)}let h=s[c];if(h===void 0)throw Error(`Unable to resolve file parent for "${i}"`);let g=i.slice(p),_=a.get(g);_===void 0&&(_=o.length,a.set(g,_),o.push(g)),r.push({depthAndFlags:M(n+1,0),nameId:_,parentId:h,subtreeNodeCount:1,visibleSubtreeCount:1}),p!==l.length&&(l=i.substring(0,p),u=n),t=i}s.length=c+1,t!=null&&(this.lastPreparedPath=Me(t)),this.hasDeferredDirectoryIndexes=!0}finish(e={}){let t=e.skipSubtreeCountPass===!0;return this.hasDeferredDirectoryIndexes?(I(this.instrumentation,`store.builder.buildDirectoryIndexes`,()=>this.buildPresortedFinish(t)),this.hasDeferredDirectoryIndexes=!1):t||I(this.instrumentation,`store.builder.computeSubtreeCounts`,()=>this.computeSubtreeCounts(0)),{directories:this.directories,nodes:this.nodes,options:this.options,rootId:0,segmentTable:this.segmentTable,presortedDirectoryNodeIds:this.presortedDirectoryNodeIds.length>0?this.presortedDirectoryNodeIds:null}}didMatchAllInitialExpandedPaths(){return this.createdDirectoriesAllExpanded&&this.initialExpandedPathSet!=null&&this.createdDirectoryCount===this.initialExpandedPathSet.size}appendPreparedPath(e,t){if(this.hasDeferredDirectoryIndexes&&=(this.buildDirectoryIndexes(),!1),this.lastPreparedPath!=null){if(e.path===this.lastPreparedPath.path)throw Error(`Duplicate path: "${e.path}"`);if(t&&(this.options.sort==="default"?ke(this.lastPreparedPath,e,this.segmentSortKeyCache):Be(this.lastPreparedPath,e,this.options.sort))>0)throw Error(`Builder input must be sorted before appendPaths(): "${e.path}"`)}let n=this.lastPreparedPath,r=Ue(e),i=n==null?0:Ue(n),a=n==null?0:He(n.segments,e.segments),o=Math.min(a,r,i);this.directoryStack.length=o+1;for(let n=o;n<r;n++){let r=this.directoryStack[this.directoryStack.length-1];if(r===void 0)throw Error(`Directory stack underflow while building the path store`);let i=t?this.getOrCreateDirectoryChild(r,e.segments[n]):this.createDirectoryChild(r,e.segments[n]);this.directoryStack.push(i)}if(e.isDirectory){let t=this.directoryStack[this.directoryStack.length-1];if(t===void 0)throw Error(`Unable to resolve directory node for "${e.path}"`);this.promoteDirectoryToExplicit(t,e.path),this.lastPreparedPath=e;return}let s=this.directoryStack[this.directoryStack.length-1];if(s===void 0)throw Error(`Unable to resolve file parent for "${e.path}"`);t?this.createFileChild(s,e.basename,e.path):this.createFileChildUnchecked(s,e.basename),this.lastPreparedPath=e}recordCreatedDirectoryPath(e){!this.createdDirectoriesAllExpanded||this.initialExpandedPathSet==null||(this.createdDirectoryCount+=1,this.initialExpandedPathSet.has(e)||(this.createdDirectoriesAllExpanded=!1))}createFileChild(e,t,n){let r=Fe(this.segmentTable,t),i=this.getDirectoryIndex(e),a=i.childIdByNameId;if(a!=null&&a.get(r)!==void 0)throw Error(`Path collides with an existing entry: "${n}"`);let o=this.nodes[e];if(o===void 0)throw Error(`Unknown parent node ID: ${String(e)}`);let s=this.nodes.length;return this.nodes.push({depthAndFlags:M(N(o)+1,0),nameId:r,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),a?.set(r,s),ne(i,s),s}createFileChildUnchecked(e,t){let n=Fe(this.segmentTable,t),r=this.getDirectoryIndex(e),i=this.nodes[e];if(i===void 0)throw Error(`Unknown parent node ID: ${String(e)}`);let a=this.nodes.length;return this.nodes.push({depthAndFlags:M(N(i)+1,0),nameId:n,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),r.childIdByNameId!=null&&r.childIdByNameId.set(n,a),ne(r,a),a}getOrCreateDirectoryChild(e,t){let n=Fe(this.segmentTable,t),r=this.getDirectoryIndex(e);if(r.childIdByNameId!=null){let e=r.childIdByNameId.get(n);if(e!==void 0){let n=this.nodes[e];if(n!=null&&!P(n))throw Error(`Path collides with an existing file while creating directory "${t}"`);return e}}let i=this.nodes[e];if(i===void 0)throw Error(`Unknown parent node ID: ${String(e)}`);let a=this.nodes.length;return this.nodes.push({depthAndFlags:M(N(i)+1,0,1),nameId:n,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),r.childIdByNameId!=null&&r.childIdByNameId.set(n,a),ne(r,a),this.directories.set(a,T()),a}createDirectoryChild(e,t){let n=Fe(this.segmentTable,t),r=this.getDirectoryIndex(e),i=this.nodes[e];if(i===void 0)throw Error(`Unknown parent node ID: ${String(e)}`);let a=this.nodes.length;return this.nodes.push({depthAndFlags:M(N(i)+1,0,1),nameId:n,parentId:e,subtreeNodeCount:1,visibleSubtreeCount:1}),r.childIdByNameId!=null&&r.childIdByNameId.set(n,a),ne(r,a),this.directories.set(a,T()),a}promoteDirectoryToExplicit(e,t){let n=this.nodes[e];if(n===void 0)throw Error(`Unknown directory node ID: ${String(e)}`);if(!P(n))throw Error(`Path is not a directory: "${t}"`);if(F(n,1))throw Error(`Duplicate path: "${t}"`);pe(n,1)}getDirectoryIndex(e){let t=this.directories.get(e);if(t!==void 0)return t;throw Error(`Unknown directory child index for node ${String(e)}`)}buildPresortedFinish(e){let t=this.nodes,n=this.directories;n.set(0,E());let r=-1,i=null;for(let e=1;e<t.length;e++){let a=t[e];if(a==null)continue;if(P(a)){let t=E();n.set(e,t),r=e,i=t}let o;a.parentId===r?o=i:(o=n.get(a.parentId),r=a.parentId,i=o??null),o?.childIds.push(e)}if(!e)for(let e=t.length-1;e>=1;e--){let n=t[e];if(n==null)continue;let r=t[n.parentId];r!=null&&(r.subtreeNodeCount+=n.subtreeNodeCount,r.visibleSubtreeCount+=n.visibleSubtreeCount)}}buildDirectoryIndexes(){let e=this.nodes;for(let t=1;t<e.length;t++){let n=e[t];if(n==null)continue;P(n)&&this.directories.set(t,T());let r=this.directories.get(n.parentId);r!=null&&(r.childIdByNameId!=null&&r.childIdByNameId.set(n.nameId,t),ne(r,t))}}computeSubtreeCounts(e){let t=this.nodes[e];if(t===void 0)throw Error(`Unknown node ID: ${String(e)}`);if(!P(t))return t.subtreeNodeCount=1,t.visibleSubtreeCount=1,1;let n=this.getDirectoryIndex(e),r=1;for(let e of n.childIds)r+=this.computeSubtreeCounts(e);return re(this.nodes,n),t.subtreeNodeCount=r,t.visibleSubtreeCount=r,r}};function et(e,t=`closed`,n=null){let r=nt(t);return{activeNodeCount:e.nodes.length-1,collapsedDirectoryIds:new Set,collapseNewDirectoriesByDefault:!1,defaultExpansion:r,directoriesOpenByDefault:r===`open`,hasCollapsedDirectoryOverrides:!1,directoryLoadInfoById:new Map,expandedDirectoryIds:new Set,instrumentation:n,listeners:new Map,pathCacheByNodeId:new Map([[e.rootId,{path:``,version:0}]]),pathCacheVersion:0,snapshot:e,transactionStack:[]}}function tt(){return{affectedAncestorIds:new Set,affectedNodeIds:new Set,events:[]}}function nt(e){if(typeof e!=`number`)return e;if(!Number.isInteger(e)||e<0)throw Error(`initialExpansion must be "open", "closed", or a non-negative integer depth. Received: ${String(e)}`);return e}function rt(e,t){return F(t,2)||e.defaultExpansion===`open`?!0:e.defaultExpansion!==`closed`&&N(t)<=e.defaultExpansion}function R(e,t,n=e.snapshot.nodes[t]){return n==null||!P(n)?!1:e.directoriesOpenByDefault&&!e.hasCollapsedDirectoryOverrides?!0:e.collapsedDirectoryIds.has(t)?!1:e.expandedDirectoryIds.has(t)?!0:rt(e,n)}function it(e,t,n,r=e.snapshot.nodes[t]){if(r==null||!P(r))return;let i=rt(e,r);if(n){if(i){e.collapsedDirectoryIds.delete(t),e.hasCollapsedDirectoryOverrides=e.collapsedDirectoryIds.size>0;return}e.expandedDirectoryIds.add(t);return}if(i){e.collapsedDirectoryIds.add(t),e.hasCollapsedDirectoryOverrides=!0;return}e.expandedDirectoryIds.delete(t)}function at(e,t){let n=e.directoryLoadInfoById.get(t);if(n!=null)return n;let r={activeAttemptId:null,errorMessage:null,nextAttemptId:1,state:`loaded`};return e.directoryLoadInfoById.set(t,r),r}function ot(e,t){return e.directoryLoadInfoById.get(t)?.state??`loaded`}function st(e,t){let n=at(e,t);if(n.state===`loading`&&n.activeAttemptId!=null)return{attemptId:n.activeAttemptId,nodeId:t,reused:!0};let r=n.nextAttemptId;return n.activeAttemptId=r,n.errorMessage=null,n.nextAttemptId+=1,n.state=`loading`,{attemptId:r,nodeId:t,reused:!1}}function ct(e,t){let n=at(e,t);n.activeAttemptId=null,n.errorMessage=null,n.state=`unloaded`}function lt(e,t,n){let r=e.directoryLoadInfoById.get(t);return r==null||r.activeAttemptId!==n?!1:(r.activeAttemptId=null,r.errorMessage=null,r.state=`loaded`,!0)}function z(e,t,n){return e.directoryLoadInfoById.get(t)?.activeAttemptId===n}function B(e,t,n,r){let i=e.directoryLoadInfoById.get(t);return i==null||i.activeAttemptId!==n?!1:(i.activeAttemptId=null,i.errorMessage=r??null,i.state=`error`,!0)}function ut(e,t){e.directoryLoadInfoById.delete(t)}function dt(e,t,n){let r=n,i=e.listeners.get(t);return i==null?e.listeners.set(t,new Set([r])):i.add(r),()=>{let n=e.listeners.get(t);n!=null&&(n.delete(r),n.size===0&&e.listeners.delete(t))}}function ft(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!0,operation:`add`,path:e.path,projectionChanged:e.projectionChanged,visibleCountDelta:null}}function pt(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!0,operation:`remove`,path:e.path,projectionChanged:e.projectionChanged,recursive:e.recursive,visibleCountDelta:null}}function mt(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!0,from:e.from,operation:`move`,projectionChanged:e.projectionChanged,to:e.to,visibleCountDelta:null}}function ht(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!1,operation:`expand`,path:e.path,projectionChanged:!0,visibleCountDelta:null}}function gt(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!1,operation:`collapse`,path:e.path,projectionChanged:!0,visibleCountDelta:null}}function _t(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],canonicalChanged:!1,operation:`mark-directory-unloaded`,path:e.path,projectionChanged:e.projectionChanged,visibleCountDelta:null}}function vt(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],attemptId:e.attemptId,canonicalChanged:!1,operation:`begin-child-load`,path:e.path,projectionChanged:e.projectionChanged,reused:e.reused,visibleCountDelta:null}}function V(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],attemptId:e.attemptId,canonicalChanged:e.childEvents.some(e=>e.canonicalChanged),childEvents:e.childEvents,operation:`apply-child-patch`,path:e.path,projectionChanged:e.projectionChanged,visibleCountDelta:null}}function yt(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],attemptId:e.attemptId,canonicalChanged:!1,operation:`complete-child-load`,path:e.path,projectionChanged:e.projectionChanged,stale:e.stale,visibleCountDelta:null}}function H(e){return{affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],attemptId:e.attemptId,canonicalChanged:!1,errorMessage:e.errorMessage,operation:`fail-child-load`,path:e.path,projectionChanged:e.projectionChanged,stale:e.stale,visibleCountDelta:null}}function bt(e){return{activeNodeCountAfter:e.activeNodeCountAfter,activeNodeCountBefore:e.activeNodeCountBefore,affectedAncestorIds:e.affectedAncestorIds??[],affectedNodeIds:e.affectedNodeIds??[],cachedPathEntryCountAfter:e.cachedPathEntryCountAfter,cachedPathEntryCountBefore:e.cachedPathEntryCountBefore,canonicalChanged:!1,idsPreserved:e.idsPreserved,loadInfoEntryCountAfter:e.loadInfoEntryCountAfter,loadInfoEntryCountBefore:e.loadInfoEntryCountBefore,mode:e.mode,operation:`cleanup`,projectionChanged:e.projectionChanged,reclaimedCachedPathEntryCount:e.reclaimedCachedPathEntryCount,reclaimedLoadInfoEntryCount:e.reclaimedLoadInfoEntryCount,reclaimedNodeSlotCount:e.reclaimedNodeSlotCount,reclaimedSegmentCount:e.reclaimedSegmentCount,segmentCountAfter:e.segmentCountAfter,segmentCountBefore:e.segmentCountBefore,totalNodeSlotCountAfter:e.totalNodeSlotCountAfter,totalNodeSlotCountBefore:e.totalNodeSlotCountBefore,visibleCountDelta:null}}function U(e,t,n){return{...n,visibleCountDelta:At(e)-t}}function xt(e,t){let n=At(e),r=tt();e.transactionStack.push(r);try{t()}catch(t){throw Ct(e,r,!1),t}Ct(e,r,!0,At(e)-n)}function W(e,t){let n=e.instrumentation;if(n==null){St(e,t);return}I(n,`store.events.record`,()=>St(e,t))}function St(e,t){let n=e.transactionStack[e.transactionStack.length-1]??null;if(n==null){Ot(e,t);return}n.events.push(t),Dt(n,t)}function Ct(e,t,n,r=null){if(e.transactionStack.pop()!==t)throw Error(`Transaction stack underflow`);if(!n)return;let i=e.transactionStack[e.transactionStack.length-1]??null;if(i!=null){let n=e.instrumentation;n==null?Et(i,t):I(n,`store.events.batch.merge`,()=>Et(i,t));return}let a=wt(t,r),o=e.instrumentation;if(o==null){Ot(e,a);return}I(o,`store.events.batch.commit`,()=>Ot(e,a))}function wt(e,t){return{affectedAncestorIds:[...e.affectedAncestorIds],affectedNodeIds:[...e.affectedNodeIds],canonicalChanged:e.events.some(e=>e.canonicalChanged),events:[...e.events],operation:`batch`,projectionChanged:e.events.some(e=>e.projectionChanged),visibleCountDelta:t}}function Tt(e,t){for(let n of t.affectedAncestorIds)e.affectedAncestorIds.add(n);for(let n of t.affectedNodeIds)e.affectedNodeIds.add(n)}function Et(e,t){for(let n of t.events)e.events.push(n);Tt(e,t)}function Dt(e,t){for(let n of t.affectedNodeIds)e.affectedNodeIds.add(n);for(let n of t.affectedAncestorIds)e.affectedAncestorIds.add(n)}function Ot(e,t){let n=e.instrumentation;if(n==null){kt(e,t);return}I(n,`store.events.emit`,()=>kt(e,t))}function kt(e,t){e.listeners.get(t.operation)?.forEach(e=>e(t)),e.listeners.get(`*`)?.forEach(e=>e(t))}function At(e){return e.snapshot.nodes[e.snapshot.rootId]?.visibleSubtreeCount??0}function G(e,t){if(e.snapshot.options.flattenEmptyDirectories!==!0)return null;let n=e.snapshot.nodes[t];if(n==null||!P(n)||F(n,2))return null;let r=e.snapshot.directories.get(t);if(r==null||r.childIds.length!==1)return null;let i=r.childIds[0];if(i==null)return null;let a=e.snapshot.nodes[i];return a==null||!P(a)?null:i}function jt(e,t){let n=t;for(;;){let t=G(e,n);if(t==null)return n;n=t}}function Mt(e,t){let n=[t],r=t;for(;;){let t=G(e,r);if(t==null)return n;n.push(t),r=t}}function Nt(e,t){let n=t==null?e.snapshot.rootId:Ht(e,t);return n==null?[]:Wt(e,n)}function Pt(e,t){let n=Me(t),r=n.isDirectory?n.segments:n.segments.slice(0,-1),i=on(e,an(e,r)),{createdNodeIds:a,directoryId:o}=Gt(e,r),s=new Set(a),c=o;if(n.isDirectory){let n=J(e,o);if(F(n,1))throw Error(`Path already exists: "${t}"`);pe(n,1),e.pathCacheByNodeId.set(o,{path:t,version:e.pathCacheVersion}),s.add(o)}else c=qt(e,o,n.basename),s.add(c);zt(e,o);let l=on(e,o);return ft({affectedAncestorIds:Vt(e,c),affectedNodeIds:[...s],path:t,projectionChanged:sn(i,l)})}function Ft(e,t,n){let r=Ht(e,t);if(r==null)throw Error(`Path does not exist: "${t}"`);let i=J(e,r);if(F(i,2))throw Error(`The root node cannot be removed`);if(P(i)&&q(e,r).childIds.length>0&&n.recursive!==!0)throw Error(`Cannot remove a non-empty directory without recursive: "${t}"`);let a=i.parentId,o=on(e,a),s=nn(e,r);Xt(e,a,r,i.nameId),rn(e,a),zt(e,a);let c=on(e,a);return pt({affectedAncestorIds:Vt(e,a),affectedNodeIds:s,path:t,projectionChanged:sn(o,c),recursive:n.recursive===!0})}function It(e,t,n,r){let i=Ht(e,t);if(i==null)throw Error(`Source path does not exist: "${t}"`);let a=J(e,i);if(F(a,2))throw Error(`The root node cannot be moved`);let o=r.collision??`error`,s=en(e,i,n),c=on(e,a.parentId),l=on(e,s.parentId),u=Ie(e.snapshot.segmentTable,a.nameId),d=Fe(e.snapshot.segmentTable,s.basename);if(s.parentId===a.parentId&&u===s.basename)return null;if(P(a)&&dn(e,i,s.parentId))throw Error(`Cannot move a directory into one of its descendants`);let f=D(e.snapshot.nodes,q(e,s.parentId)).get(d),p=s.existingNodeId??f??null;if(p!=null&&p!==i&&tn(e,p,o,de(a))===`skip`)return null;let m=a.parentId;Xt(e,m,i,a.nameId),a.parentId=s.parentId,a.nameId=d,e.pathCacheByNodeId.delete(i),un(e,i),Yt(e,s.parentId,i),rn(e,m),e.pathCacheVersion++,zt(e,m),s.parentId!==m&&zt(e,s.parentId);let h=on(e,m),g=on(e,s.parentId);return mt({affectedAncestorIds:[...new Set([...Vt(e,m),...Vt(e,s.parentId)])],affectedNodeIds:[i],from:t,projectionChanged:cn([c,l],[h,g]),to:K(e,i)})}function Lt(e,t){let n=e.pathCacheByNodeId.get(t);return n!=null&&n.version===e.pathCacheVersion?n.path:null}function Rt(e,t,n){return e.pathCacheByNodeId.set(t,{path:n,version:e.pathCacheVersion}),n}function K(e,t){let n=J(e,t),r=Lt(e,t);if(r!=null)return r;if(F(n,2))return Rt(e,t,``);let i=K(e,n.parentId),a=Ie(e.snapshot.segmentTable,n.nameId),o=i.length===0?a:`${i}${a}`;return Rt(e,t,P(n)?`${o}/`:o)}function zt(e,t){let n=e.instrumentation;if(n==null){pn(e,t);return}I(n,`store.recomputeCountsUpwardFrom`,()=>pn(e,t))}function Bt(e,t){let n=[[t,0]],{nodes:r,directories:i}=e.snapshot;for(;n.length>0;){let t=n[n.length-1],a=t[0],o=r[a];if(o==null||!P(o)){fn(e,a,o,!0),n.pop();continue}let s=i.get(a);if(s==null||t[1]>=s.childIds.length){fn(e,a,o,!0),n.pop();continue}let c=s.childIds[t[1]++];n.push([c,0])}}function Vt(e,t){let n=[],r=t;for(;r!=null;){let t=J(e,r);if(n.push(r),r===e.snapshot.rootId)break;r=t.parentId}return n}function Ht(e,t){if(t.length===0)return e.snapshot.rootId;let n=Ne(t);return Ut(e,n.segments,n.requiresDirectory)}function Ut(e,t,n){let r=e.snapshot.rootId;for(let n of t){let t=e.snapshot.segmentTable.idByValue.get(n);if(t===void 0)return null;let i=q(e,r),a=D(e.snapshot.nodes,i).get(t);if(a===void 0)return null;r=a}let i=J(e,r);return n&&!P(i)?null:r}function q(e,t){let n=e.snapshot.directories.get(t);if(n===void 0)throw Error(`Unknown directory child index for node ${String(t)}`);return n}function J(e,t){let n=e.snapshot.nodes[t];if(n===void 0||F(n,4))throw Error(`Unknown node ID: ${String(t)}`);return n}function Wt(e,t){let n=e.snapshot.nodes[t];if(n===void 0||F(n,4))return[];if(!P(n))return[K(e,t)];if(q(e,t).childIds.length===0)return F(n,1)&&!F(n,2)?[K(e,t)]:[];let r=[],i=[{childIndex:0,nodeId:t}];for(;i.length>0;){let t=i[i.length-1];if(t==null)break;let n=e.snapshot.nodes[t.nodeId];if(n===void 0||F(n,4)){i.pop();continue}if(!P(n)){r.push(K(e,t.nodeId)),i.pop();continue}let a=q(e,t.nodeId);if(a.childIds.length===0){F(n,1)&&!F(n,2)&&r.push(K(e,t.nodeId)),i.pop();continue}let o=a.childIds[t.childIndex];if(o==null){i.pop();continue}t.childIndex++,i.push({childIndex:0,nodeId:o})}return r}function Gt(e,t){let n=[],r=e.snapshot.rootId;for(let i of t){let t=Fe(e.snapshot.segmentTable,i),a=q(e,r),o=D(e.snapshot.nodes,a).get(t);if(o!==void 0){if(!P(J(e,o)))throw Error(`Cannot create a directory that collides with an existing file: "${i}"`);r=o;continue}r=Kt(e,r,t),n.push(r)}return{createdNodeIds:n,directoryId:r}}function Kt(e,t,n){let r=J(e,t),i=e.snapshot.nodes.length;return e.snapshot.nodes.push({depthAndFlags:M(N(r)+1,0,1),nameId:n,parentId:t,subtreeNodeCount:1,visibleSubtreeCount:1}),e.snapshot.directories.set(i,T()),Yt(e,t,i),e.collapseNewDirectoriesByDefault&&(e.collapsedDirectoryIds.add(i),e.hasCollapsedDirectoryOverrides=!0),e.activeNodeCount++,i}function qt(e,t,n){let r=Fe(e.snapshot.segmentTable,n),i=q(e,t);if(D(e.snapshot.nodes,i).has(r))throw Error(`Path already exists: "${hn(e,t,n)}"`);let a=J(e,t),o=e.snapshot.nodes.length;return e.snapshot.nodes.push({depthAndFlags:M(N(a)+1,0),nameId:r,parentId:t,subtreeNodeCount:1,visibleSubtreeCount:1}),Yt(e,t,o),e.activeNodeCount++,o}function Jt(e,t,n){let r=0,i=t.childIds.length;for(;r<i;){let a=r+i>>>1,o=t.childIds[a];if(o==null){i=a;continue}Zt(e,n,o)<0?i=a:r=a+1}return r}function Yt(e,t,n){let r=q(e,t),i=J(e,n);D(e.snapshot.nodes,r).set(i.nameId,n),ie(r,n,i.subtreeNodeCount,i.visibleSubtreeCount);let a=Jt(e,r,n);r.childIds.splice(a,0,n),k(r,a),j(e.snapshot.nodes,r)}function Xt(e,t,n,r){let i=q(e,t),a=O(i),o=a.get(n)??-1;D(e.snapshot.nodes,i).delete(r),a.delete(n);let s=e.snapshot.nodes[n];s!=null&&ie(i,n,-s.subtreeNodeCount,-s.visibleSubtreeCount),o>=0&&(i.childIds.splice(o,1),k(i,o),j(e.snapshot.nodes,i))}function Zt(e,t,n){let r=e.snapshot.options.sort;return r==="default"?Qt(e,t,n):r($t(e,t),$t(e,n))}function Qt(e,t,n){let r=J(e,t),i=J(e,n),a=P(r);if(a!==P(i))return a?-1:1;let o=Ce(L(e.snapshot.segmentTable,r.nameId),L(e.snapshot.segmentTable,i.nameId));if(o!==0)return o;let s=Ie(e.snapshot.segmentTable,r.nameId),c=Ie(e.snapshot.segmentTable,i.nameId);return s===c?t<n?-1:1:s<c?-1:1}function $t(e,t){let n=J(e,t),r=K(e,t),i=P(n),a=i?r.slice(0,-1):r;return{basename:Ie(e.snapshot.segmentTable,n.nameId),depth:N(n),isDirectory:i,path:r,segments:a.length===0?[]:a.split(`/`)}}function en(e,t,n){let r=J(e,t),i=Ht(e,n);if(i!=null){let t=J(e,i);if(P(t))return{basename:Ie(e.snapshot.segmentTable,r.nameId),existingNodeId:null,parentId:i};let a=Ne(n).segments;return{basename:a[a.length-1]??``,existingNodeId:i,parentId:t.parentId}}let a=Ne(n),o=a.segments[a.segments.length-1]??``,s=a.segments.slice(0,-1),c=s.length===0?e.snapshot.rootId:Ut(e,s,!0);if(c==null)throw Error(`Destination parent does not exist: "${n}"`);return{basename:o,existingNodeId:null,parentId:c}}function tn(e,t,n,r){if(n===`skip`)return`skip`;if(n===`error`)throw Error(`Destination already exists: "${K(e,t)}"`);let i=J(e,t);if(de(i)!==r)throw Error(`replace collision requires the same source and destination kinds`);if(P(i)&&q(e,t).childIds.length>0)throw Error(`replace collision does not support non-empty directories`);let a=i.parentId,o=i.nameId;return nn(e,t),Xt(e,a,t,o),rn(e,a),zt(e,a),`handled`}function nn(e,t){let n=[],r=[{nodeId:t,visitedChildren:!1}];for(;r.length>0;){let t=r.pop();if(t==null)break;let i=J(e,t.nodeId);if(t.visitedChildren||!P(i)){P(i)&&e.snapshot.directories.delete(t.nodeId),pe(i,4),e.pathCacheByNodeId.delete(t.nodeId),e.collapsedDirectoryIds.delete(t.nodeId)&&(e.hasCollapsedDirectoryOverrides=e.collapsedDirectoryIds.size>0),e.expandedDirectoryIds.delete(t.nodeId),ut(e,t.nodeId),e.activeNodeCount--,n.push(t.nodeId);continue}r.push({nodeId:t.nodeId,visitedChildren:!0});let a=q(e,t.nodeId);for(let e=a.childIds.length-1;e>=0;e--){let t=a.childIds[e];t!=null&&r.push({nodeId:t,visitedChildren:!1})}}return n}function rn(e,t){let n=t;for(;n!=null;){let t=J(e,n);if(!P(t)||F(t,2)||q(e,n).childIds.length>0)return;pe(t,1),n=t.parentId===n?null:t.parentId}}function an(e,t){let n=e.snapshot.rootId;for(let r of t){let t=e.snapshot.segmentTable.idByValue.get(r);if(t==null)break;let i=D(e.snapshot.nodes,q(e,n)).get(t);if(i==null||!P(J(e,i)))break;n=i}return n}function on(e,t){let n=ln(e,t);if(n==null)return null;let r=jt(e,n),i=J(e,r),a=n===r?null:Mt(e,n).map(t=>K(e,t));return JSON.stringify({flattenedSegmentPaths:a,hasChildren:q(e,r).childIds.length>0,path:K(e,r),terminalKind:de(i)})}function sn(e,t){return cn([e],[t])}function cn(e,t){for(let n=0;n<e.length;n+=1){let r=e[n],i=t[n];if(r==null||i==null||r!==i)return!0}return!1}function ln(e,t){let n=t;for(;n!=null;){let t=J(e,n);if(!P(t)||F(t,2))return null;if(!R(e,n,t))return n;n=t.parentId}return null}function un(e,t){let n=J(e,t);if(me(n,(t===e.snapshot.rootId?-1:N(J(e,n.parentId)))+1),!P(n))return;let r=q(e,t);for(let t of r.childIds)un(e,t)}function dn(e,t,n){let r=n;for(;r!=null;){if(r===t)return!0;let n=J(e,r);if(r===e.snapshot.rootId)return!1;r=n.parentId}return!1}function fn(e,t,n=J(e,t),r=!1){let i=e.instrumentation;if(i==null){mn(e,t,n,r);return}I(i,`store.recomputeNodeCounts`,()=>mn(e,t,n,r))}function pn(e,t){let n=t;for(;n!=null;){let t=J(e,n),r=t.subtreeNodeCount,i=t.visibleSubtreeCount;if(fn(e,n,t),n===e.snapshot.rootId)return;let a=t.subtreeNodeCount-r,o=t.visibleSubtreeCount-i,s=t.parentId;(a!==0||o!==0)&&ie(q(e,s),n,a,o),n=s}}function mn(e,t,n,r){if(!P(n)){n.subtreeNodeCount=1,n.visibleSubtreeCount=1;return}let i=q(e,t);if(r){let t=e.instrumentation;t==null?re(e.snapshot.nodes,i):I(t,`store.recomputeNodeCounts.rebuildChildAggregates`,()=>re(e.snapshot.nodes,i))}let a=1+i.totalChildSubtreeNodeCount,o=i.totalChildVisibleSubtreeCount;if(n.subtreeNodeCount=a,F(n,2)){n.visibleSubtreeCount=o;return}n.visibleSubtreeCount=G(e,t)==null?R(e,t,n)?1+o:1:o}function hn(e,t,n){let r=K(e,t);return r.length===0?n:`${r}${n}`}function gn(e){return e!=null&&!F(e,4)}function _n(e,t){let n=e.snapshot.nodes[t];return!gn(n)||!P(n)||F(n,2)?null:n}function vn(e){let t=0;for(let[n,r]of e.pathCacheByNodeId)r.version===e.pathCacheVersion&&gn(e.snapshot.nodes[n])&&(t+=1);return t}function yn(e){return Math.max(0,e.valueById.length-1)}function bn(e){return{activeNodeCount:e.activeNodeCount,cachedPathEntryCount:vn(e),loadInfoEntryCount:e.directoryLoadInfoById.size,segmentCount:yn(e.snapshot.segmentTable),totalNodeSlotCount:Math.max(0,e.snapshot.nodes.length-1)}}function xn(e,t,n,r){return{activeNodeCountAfter:r.activeNodeCount,activeNodeCountBefore:n.activeNodeCount,cachedPathEntryCountAfter:r.cachedPathEntryCount,cachedPathEntryCountBefore:n.cachedPathEntryCount,idsPreserved:t,loadInfoEntryCountAfter:r.loadInfoEntryCount,loadInfoEntryCountBefore:n.loadInfoEntryCount,mode:e,reclaimedCachedPathEntryCount:n.cachedPathEntryCount-r.cachedPathEntryCount,reclaimedLoadInfoEntryCount:n.loadInfoEntryCount-r.loadInfoEntryCount,reclaimedNodeSlotCount:n.totalNodeSlotCount-r.totalNodeSlotCount,reclaimedSegmentCount:n.segmentCount-r.segmentCount,segmentCountAfter:r.segmentCount,segmentCountBefore:n.segmentCount,totalNodeSlotCountAfter:r.totalNodeSlotCount,totalNodeSlotCountBefore:n.totalNodeSlotCount}}function Sn(e){let t=[],n=[];for(let n of e.collapsedDirectoryIds)_n(e,n)!=null&&t.push(K(e,n));for(let t of e.expandedDirectoryIds)_n(e,t)!=null&&n.push(K(e,t));return{collapsedPaths:t,expandedPaths:n}}function Cn(e){let t=[];for(let[n,r]of e.directoryLoadInfoById)_n(e,n)!=null&&ot(e,n)!==`loaded`&&t.push({info:{activeAttemptId:null,errorMessage:r.errorMessage,nextAttemptId:r.nextAttemptId,state:r.state},path:K(e,n)});return t}function wn(e,t){e.collapsedDirectoryIds.clear(),e.hasCollapsedDirectoryOverrides=!1,e.expandedDirectoryIds.clear();for(let n of t.expandedPaths){let t=Ht(e,n);t!=null&&it(e,t,!0,J(e,t))}for(let n of t.collapsedPaths){let t=Ht(e,n);t!=null&&it(e,t,!1,J(e,t))}}function Tn(e,t){e.directoryLoadInfoById.clear();for(let n of t){let t=Ht(e,n.path);t!=null&&_n(e,t)!=null&&e.directoryLoadInfoById.set(t,{activeAttemptId:null,errorMessage:n.info.errorMessage,nextAttemptId:n.info.nextAttemptId,state:n.info.state})}}function En(e){e.pathCacheVersion+=1,e.pathCacheByNodeId.clear(),e.pathCacheByNodeId.set(e.snapshot.rootId,{path:``,version:e.pathCacheVersion})}function Dn(e){let t=e.snapshot.segmentTable,n=Pe();for(let r of e.snapshot.nodes)if(gn(r)){if(F(r,2)){r.nameId=0;continue}r.nameId=Fe(n,Ie(t,r.nameId))}e.snapshot.segmentTable=n}function On(e){for(let[t,n]of e.snapshot.directories){let r=e.snapshot.nodes[t];if(!gn(r)||!P(r)){e.snapshot.directories.delete(t);continue}let i=n.childIds.filter(n=>{let r=e.snapshot.nodes[n];return gn(r)&&r.parentId===t});n.childIds=i,n.childIdByNameId=new Map(i.map(t=>[J(e,t).nameId,t])),n.childPositionById=new Map(i.map((e,t)=>[e,t])),re(e.snapshot.nodes,n)}}function kn(e){let t=e.snapshot.nodes.length-1;for(;t>e.snapshot.rootId;){let n=e.snapshot.nodes[t];if(gn(n))break;--t}e.snapshot.nodes.length=t+1}function An(e){let t=Sn(e),n=Cn(e);I(e.instrumentation,`store.cleanup.stable.clearPathCaches`,()=>En(e)),I(e.instrumentation,`store.cleanup.stable.rebuildSegmentTable`,()=>Dn(e)),I(e.instrumentation,`store.cleanup.stable.rebuildDirectoryIndexes`,()=>On(e)),I(e.instrumentation,`store.cleanup.stable.trimTrailingRemovedNodeSlots`,()=>kn(e)),I(e.instrumentation,`store.cleanup.stable.restoreExpansionOverrides`,()=>wn(e,t)),I(e.instrumentation,`store.cleanup.stable.restoreDirectoryLoadInfos`,()=>Tn(e,n)),I(e.instrumentation,`store.cleanup.stable.recomputeCounts`,()=>Bt(e,e.snapshot.rootId))}function jn(e){let t=Sn(e),n=Cn(e),r=I(e.instrumentation,`store.cleanup.aggressive.listPaths`,()=>Nt(e)),i=ge({...e.snapshot.options},e.instrumentation),a=I(e.instrumentation,`store.cleanup.aggressive.rebuildSnapshot`,()=>{let e=new $e(i);return e.appendPaths(r),e.finish()});e.snapshot=a,e.activeNodeCount=a.nodes.length-1,e.pathCacheByNodeId=new Map([[a.rootId,{path:``,version:0}]]),e.pathCacheVersion=0,I(e.instrumentation,`store.cleanup.aggressive.restoreExpansionOverrides`,()=>wn(e,t)),I(e.instrumentation,`store.cleanup.aggressive.restoreDirectoryLoadInfos`,()=>Tn(e,n)),I(e.instrumentation,`store.cleanup.aggressive.recomputeCounts`,()=>Bt(e,e.snapshot.rootId))}function Mn(e){for(let t of e.directoryLoadInfoById.values())if(t.state===`loading`&&t.activeAttemptId!=null)return!0;return!1}function Nn(e,t){let n=bn(e);t===`stable`?I(e.instrumentation,`store.cleanup.stable`,()=>An(e)):I(e.instrumentation,`store.cleanup.aggressive`,()=>jn(e));let r=bn(e);return xn(t,t===`stable`,n,r)}var Pn=64;function Fn(e,t){let n=t+2;if(n<=e.length)return e;let r=e.length;for(;r<n;)r*=2;let i=new Int32Array(r);return i.fill(-1),i.set(e),i}function In(e){return J(e,e.snapshot.rootId).visibleSubtreeCount}function Ln(e,t,n,r){let i=J(e,t.terminalNodeId),a=Math.max(1,i.visibleSubtreeCount);return Math.min(r-1,n+a-1)}function Rn(e,t,n,r){return{ancestorPaths:r,index:t.index,posInSet:t.posInSet,row:rr(e,t.cursor),setSize:t.setSize,subtreeEndIndex:Ln(e,t.cursor,t.index,n)}}function zn(e,t,n,r,i,a){let o=q(e,t),{childIndex:s,childVisibleIndex:c,localVisibleIndex:l}=A(e.snapshot.nodes,o,n),u=o.childIds[s];if(u==null)throw Error(`Visible index ${String(n)} is out of range`);return Bn(e,u,l,r+c,i+1,s,o.childIds.length,a)}function Bn(e,t,n,r,i,a,o,s){if(!P(J(e,t))){if(n===0)return{ancestors:s,cursor:{headNodeId:t,terminalNodeId:t,visibleDepth:i},index:r,posInSet:a,setSize:o};throw Error(`Visible index ${String(n)} is out of range for file`)}let c=Zn(e,t,i);if(n===0)return{ancestors:s,cursor:c,index:r,posInSet:a,setSize:o};let l=J(e,c.terminalNodeId);if(!P(l)||!R(e,c.terminalNodeId,l))throw Error(`Visible index ${String(n)} is out of range for collapsed directory`);return zn(e,c.terminalNodeId,n-1,r+1,c.visibleDepth,[...s,{cursor:c,index:r,posInSet:a,setSize:o}])}function Vn(e,t){let n=In(e);if(t<0||t>=n)return null;let r=zn(e,e.snapshot.rootId,t,0,-1,[]),i=r.ancestors.map(t=>K(e,t.cursor.terminalNodeId)),a=null;return{ancestorPaths:i,get ancestorRows(){if(a!=null)return a;let t=[],i=[];for(let a of r.ancestors){let r=Rn(e,a,n,[...i]);t.push(r),i.push(r.row.path)}return a=t,a},index:r.index,posInSet:r.posInSet,row:rr(e,r.cursor),setSize:r.setSize,subtreeEndIndex:Ln(e,r.cursor,r.index,n)}}function Hn(e,t,n){let r=e.instrumentation,i=In(e);if(i<=0||n<t)return[];let a=Math.max(0,Math.min(t,i-1)),o=Math.max(a,Math.min(n,i-1));if(r==null){if(a===0)return nr(e,o+1);let t=[],n=Jn(e,a);for(let r=a;r<=o&&n!=null;r++){let r=rr(e,n);t.push(r),n=$n(e,n)}return t}let s=[],c=0,l=0,u=I(r,`store.getVisibleSlice.selectFirstRow`,()=>Jn(e,a));for(let t=a;t<=o&&u!=null;t++){let t=I(r,`store.getVisibleSlice.materializeRow`,()=>rr(e,u));s.push(t),t.isFlattened&&(c++,l+=t.flattenedSegments?.length??0),u=I(r,`store.getVisibleSlice.advanceCursor`,()=>$n(e,u))}return ve(r,`workload.visibleRowsRead`,s.length),ve(r,`workload.flattenedRowsRead`,c),ve(r,`workload.flattenedSegmentsRead`,l),s}function Un(e,t=In(e)){let n=e.instrumentation;return n==null?tr(e,t):I(n,`store.getVisibleTreeProjection`,()=>tr(e,t))}function Wn(e){return er(Un(e))}function Gn(e,t){let n=Ht(e,t);if(n==null||n===e.snapshot.rootId||P(J(e,n))&&jt(e,n)!==n)return null;let r=0,i=n,{nodes:a,rootId:o}=e.snapshot;for(;i!==o;){let t=J(e,i).parentId,n=q(e,t),s=O(n).get(i);if(s==null)throw Error(`Child ${String(i)} was not found in its parent index`);if(r+=ae(a,n,s),t!==o){let n=J(e,t),a=G(e,t);if(!R(e,t,n)&&a!==i)return null;jt(e,t)===t&&(r+=1)}i=t}return r}function Kn(e,t){let n=Ht(e,t);if(n==null)throw Error(`Path does not exist: "${t}"`);let r=J(e,n);if(!P(r))throw Error(`Path is not a directory: "${t}"`);return R(e,n,r)?null:(it(e,n,!0,r),zt(e,n),ht({affectedAncestorIds:Vt(e,n),affectedNodeIds:[n],path:t,projectionChanged:!0}))}function qn(e,t){let n=Ht(e,t);if(n==null)throw Error(`Path does not exist: "${t}"`);let r=J(e,n);if(!P(r))throw Error(`Path is not a directory: "${t}"`);return R(e,n,r)?(it(e,n,!1,r),zt(e,n),gt({affectedAncestorIds:Vt(e,n),affectedNodeIds:[n],path:t,projectionChanged:!0})):null}function Jn(e,t){return t<0||t>=In(e)?null:Yn(e,e.snapshot.rootId,t,-1)}function Yn(e,t,n,r){let i=q(e,t),a=e.instrumentation,{childIndex:o,localVisibleIndex:s}=a==null?A(e.snapshot.nodes,i,n):I(a,`store.getVisibleSlice.selectChildIndex`,()=>A(e.snapshot.nodes,i,n)),c=i.childIds[o];if(c!=null)return Xn(e,c,s,r+1);throw Error(`Visible index ${String(n)} is out of range`)}function Xn(e,t,n,r){if(!P(J(e,t))){if(n===0)return{headNodeId:t,terminalNodeId:t,visibleDepth:r};throw Error(`Visible index ${String(n)} is out of range for file`)}let i=Zn(e,t,r);if(n===0)return i;let a=J(e,i.terminalNodeId);if(!P(a)||!R(e,i.terminalNodeId,a))throw Error(`Visible index ${String(n)} is out of range for collapsed directory`);return Yn(e,i.terminalNodeId,n-1,i.visibleDepth)}function Zn(e,t,n){return P(J(e,t))?e.instrumentation==null?{headNodeId:t,terminalNodeId:jt(e,t),visibleDepth:n}:{headNodeId:t,terminalNodeId:I(e.instrumentation,`store.getVisibleSlice.flatten.resolveTerminalDirectory`,()=>jt(e,t)),visibleDepth:n}:{headNodeId:t,terminalNodeId:t,visibleDepth:n}}function Qn(e,t){let n=J(e,t);if(!P(n))return!0;let r=n.parentId;return r===e.snapshot.rootId||G(e,r)!==t}function $n(e,t){let n=J(e,t.terminalNodeId);if(P(n)){let r=q(e,t.terminalNodeId);if(R(e,t.terminalNodeId,n)&&r.childIds.length>0){let n=r.childIds[0];return n==null?null:Xn(e,n,0,t.visibleDepth+1)}}let r=t.terminalNodeId,i=t.visibleDepth;for(;;){let t=J(e,r);if(r===e.snapshot.rootId)return null;let n=t.parentId,a=q(e,n),o=O(a).get(r)??-1;if(o<0)throw Error(`Child ${String(r)} was not found in its parent index`);let s=a.childIds[o+1]??null;if(s!=null)return Xn(e,s,0,i);Qn(e,r)&&i--,r=n}}function er(e){let t=e.paths.length,n=Array(t);for(let r=0;r<t;r+=1){let t=e.getParentIndex(r);n[r]={index:r,parentPath:t>=0?e.paths[t]??null:null,path:e.paths[r]??``,posInSet:e.posInSetByIndex[r]??0,setSize:e.setSizeByIndex[r]??0}}return{getParentIndex:e.getParentIndex,rows:n,get visibleIndexByPath(){return e.visibleIndexByPath}}}function tr(e,t){let n=Array(t),r=new Int32Array(t),i=new Int32Array(t),a=new Int32Array(t),o=new Int32Array(Pn);o.fill(-1);let s=0,{nodes:c,directories:l,segmentTable:u}=e.snapshot,d=[[l.get(e.snapshot.rootId),0,-1,``]],f=e.snapshot.options.flattenEmptyDirectories,p=e.pathCacheByNodeId,m=e.pathCacheVersion,h=u.valueById;for(;d.length>0&&s<t;){let t=d[d.length-1],u=t[0];if(t[1]>=u.childIds.length){d.pop();continue}let g=t[1],_=u.childIds[t[1]++],v=c[_],y=t[2]+1,b=t[3];o=Fn(o,y);let x,S=_;if(P(v))S=f?jt(e,_):_,x=S===_?`${b}${h[v.nameId]}/`:K(e,S);else{let e=p.get(_);x=e!=null&&e.version===m?e.path:`${b}${h[v.nameId]}`}r[s]=o[y],n[s]=x,i[s]=g,a[s]=u.childIds.length,o[y+1]=s,s+=1;let C=c[S];C!=null&&P(C)&&R(e,S,C)&&d.push([l.get(S),0,y,x])}s<t&&(n.length=s);let g=r.subarray(0,s),_=i.subarray(0,s),v=a.subarray(0,s),y=null;return{getParentIndex(e){return e<0||e>=s?-1:g[e]??-1},paths:n,posInSetByIndex:_,setSizeByIndex:v,get visibleIndexByPath(){if(y==null){y=new Map;for(let e=0;e<s;e+=1)y.set(n[e]??``,e)}return y}}}function nr(e,t){let n=Array(t),r=0,{nodes:i,directories:a,segmentTable:o}=e.snapshot,s=[[a.get(e.snapshot.rootId),0,-1]],c=o.valueById,l=e.snapshot.options.flattenEmptyDirectories,u=e.pathCacheByNodeId,d=e.pathCacheVersion;for(;s.length>0&&r<t;){let t=s[s.length-1],o=t[0];if(t[1]>=o.childIds.length){s.pop();continue}let f=o.childIds[t[1]++],p=i[f],m=t[2]+1;if(!P(p)){let t=u.get(f);n[r++]={depth:m,flattenedSegments:void 0,hasChildren:!1,id:f,isExpanded:!1,isFlattened:!1,isLoading:!1,kind:`file`,loadState:void 0,name:c[p.nameId],path:t!=null&&t.version===d?t.path:K(e,f)};continue}let h=l?jt(e,f):f,g={headNodeId:f,terminalNodeId:h,visibleDepth:m};n[r++]=rr(e,g);let _=i[h];_!=null&&P(_)&&R(e,h,_)&&s.push([a.get(h),0,m])}return r<t&&(n.length=r),n}function rr(e,t){let n=J(e,t.terminalNodeId),r=P(n)?ir(e,t):null,i=K(e,t.terminalNodeId),a=Ie(e.snapshot.segmentTable,n.nameId),o=P(n)&&q(e,t.terminalNodeId).childIds.length>0,s=t.headNodeId!==t.terminalNodeId,c=e.instrumentation,l=s?c==null?Mt(e,t.headNodeId).map(n=>{let r=J(e,n);return{isTerminal:n===t.terminalNodeId,name:Ie(e.snapshot.segmentTable,r.nameId),nodeId:n,path:K(e,n)}}):I(c,`store.getVisibleSlice.flatten.collectSegments`,()=>Mt(e,t.headNodeId).map(n=>{let r=J(e,n);return{isTerminal:n===t.terminalNodeId,name:Ie(e.snapshot.segmentTable,r.nameId),nodeId:n,path:K(e,n)}})):void 0;return{depth:t.visibleDepth,flattenedSegments:l,hasChildren:o,id:t.terminalNodeId,isExpanded:P(n)&&R(e,t.terminalNodeId,n),isFlattened:s,isLoading:r===`loading`,kind:P(n)?`directory`:`file`,loadState:r==null||r===`loaded`?void 0:r,name:a,path:i}}function ir(e,t){if(t.headNodeId===t.terminalNodeId)return ot(e,t.terminalNodeId);let n=Mt(e,t.headNodeId),r=!1,i=!1;for(let t of n){let n=ot(e,t);if(n===`loading`)return`loading`;if(n===`error`){i=!0;continue}n===`unloaded`&&(r=!0)}return i?`error`:r?`unloaded`:`loaded`}var ar=8;function or(e){let{nodes:t,directories:n,rootId:r,options:i}=e,a=t.length,o=new Int32Array(a),s=new Int32Array(a),c=new Int32Array(a),l=new Int32Array(a),u=new Int32Array(a),d=new Int32Array(a),f=new Int32Array(a);for(let e=0;e<a;e+=1){let n=t[e];n!=null&&(o[e]=n.parentId,s[e]=n.nameId,c[e]=n.depthAndFlags,l[e]=n.subtreeNodeCount,u[e]=n.visibleSubtreeCount)}let p=0;for(let e of n.values())p+=e.childIds.length;let m=new Int32Array(p),h=0;for(let[e,t]of n){let n=t.childIds;d[e]=h,f[e]=n.length;for(let e=0;e<n.length;e+=1)m[h]=n[e],h+=1}return{nodeCount:a,rootId:r,flattenEmptyDirectories:i.flattenEmptyDirectories===!0,parentId:o,nameId:s,depthAndFlags:c,subtreeNodeCount:l,visibleSubtreeCount:u,childStart:d,childCount:f,childIdsFlat:m}}function sr(e){let{nodeCount:t,rootId:n,flattenEmptyDirectories:r,depthAndFlags:i,subtreeNodeCount:a,visibleSubtreeCount:o,childStart:s,childCount:c,childIdsFlat:l}=e;for(let e=t-1;e>=1;--e){if((i[e]&ar)===0)continue;let t=s[e],n=c[e],u=0,d=0;for(let e=0;e<n;e+=1){let n=l[t+e];u+=a[n],d+=o[n]}a[e]=1+u;let f;f=r&&n===1?(i[l[t]]&ar)===0?1+d:d:1+d,o[e]=f}let u=s[n],d=c[n],f=0,p=0;for(let e=0;e<d;e+=1){let t=l[u+e];f+=a[t],p+=o[t]}a[n]=1+f,o[n]=p}function cr(e){let{directories:t,nodes:n,options:r,rootId:i,presortedDirectoryNodeIds:a}=e.snapshot,o=r.flattenEmptyDirectories===!0,s=e=>{let r=n[e];if(r==null||!P(r))return;let i=t.get(e);if(i==null)throw Error(`Unknown directory child index for node ${String(e)}`);let a=i.childIds,s=a.length,c=0,l=0;for(let e=0;e<s;e++){let t=a[e];if(t==null)continue;let r=n[t];c+=r.subtreeNodeCount,l+=r.visibleSubtreeCount}i.totalChildSubtreeNodeCount=c,i.totalChildVisibleSubtreeCount=l,s>=128&&j(n,i),r.subtreeNodeCount=1+c;let u;if(o&&s===1){let e=n[a[0]];u=e!=null&&P(e)?l:1+l}else u=1+l;r.visibleSubtreeCount=u};if(a!=null)for(let e=a.length-1;e>=0;e--)s(a[e]);else for(let e=n.length-1;e>=1;e--)s(e);let c=n[i],l=t.get(i);if(c==null||l==null)return;let u=l.childIds,d=0,f=0;for(let e=0;e<u.length;e++){let t=u[e];if(t==null)continue;let r=n[t];d+=r.subtreeNodeCount,f+=r.visibleSubtreeCount}l.totalChildSubtreeNodeCount=d,l.totalChildVisibleSubtreeCount=f,j(n,l),c.subtreeNodeCount=1+d,c.visibleSubtreeCount=f}function lr(e){let{directories:t,nodes:n,rootId:r}=e.snapshot,i=or(e.snapshot);sr(i);let a=i.subtreeNodeCount,o=i.visibleSubtreeCount;for(let e=0;e<n.length;e+=1){let t=n[e];t!=null&&(t.subtreeNodeCount=a[e],t.visibleSubtreeCount=o[e])}for(let[e,i]of t){let t=i.childIds,a=t.length,o=0,s=0;for(let e=0;e<a;e++){let r=t[e];if(r==null)continue;let i=n[r];o+=i.subtreeNodeCount,s+=i.visibleSubtreeCount}i.totalChildSubtreeNodeCount=o,i.totalChildVisibleSubtreeCount=s,(e===r||a>=128)&&j(n,i)}}function ur(e){return e.initialExpansion===`open`&&(e.initialExpandedPaths==null||e.initialExpandedPaths.length===0)}var dr=class e{#e;constructor(e={}){let t=_e(e),n=I(t,`store.builder.create`,()=>new $e(e));if(e.preparedInput!=null){let t=Xe(e.preparedInput);t==null?n.appendPreparedPaths(Ye(e.preparedInput),!1):n.appendPresortedPaths(t,Ze(e.preparedInput))}else{let r=e.paths??[];e.presorted===!0?n.appendPaths(r):n.appendPreparedPaths(I(t,`store.preparePathEntries`,()=>Qe(r,e)))}let r=I(t,`store.builder.finish`,()=>n.finish({skipSubtreeCountPass:!0})),i=I(t,`store.state.detectAllDirectoriesExpanded`,()=>(e.initialExpansion??`closed`)===`closed`&&n.didMatchAllInitialExpandedPaths());this.#e=I(t,`store.state.create`,()=>et(r,i?`open`:e.initialExpansion??`closed`,t)),i&&(this.#e.collapseNewDirectoriesByDefault=!0);let a=i?this.#e.snapshot.directories.size-1:I(t,`store.state.initializeExpandedPaths`,()=>this.initializeExpandedPaths(e.initialExpandedPaths));i||ur(e)||(e.initialExpansion??`closed`)===`closed`&&a===this.#e.snapshot.directories.size-1||(e.initialExpandedPaths?.length??0)>0&&I(t,`store.state.checkAllDirectoriesExpanded`,()=>this.hasAllDirectoriesExpanded())?e.useSoaCountSweep===!0?I(t,`store.state.initializeOpenVisibleCountsViaSoa`,()=>lr(this.#e)):I(t,`store.state.initializeOpenVisibleCounts`,()=>cr(this.#e)):I(t,`store.state.recomputeCounts`,()=>Bt(this.#e,this.#e.snapshot.rootId))}static preparePaths(e,t={}){return Ke(e,t)}static prepareInput(e,t={}){return qe(e,t)}static preparePresortedInput(e){return Je(e)}list(e){return I(this.#e.instrumentation,`store.list`,()=>Nt(this.#e,e))}add(e){I(this.#e.instrumentation,`store.add`,()=>{let t=In(this.#e);W(this.#e,U(this.#e,t,Pt(this.#e,e)))})}remove(e,t={}){I(this.#e.instrumentation,`store.remove`,()=>{let n=In(this.#e);W(this.#e,U(this.#e,n,Ft(this.#e,e,t)))})}move(e,t,n={}){I(this.#e.instrumentation,`store.move`,()=>{let r=In(this.#e),i=It(this.#e,e,t,n);i!=null&&W(this.#e,U(this.#e,r,i))})}batch(e){xt(this.#e,()=>{if(typeof e==`function`){e(this);return}for(let t of e)switch(t.type){case`add`:this.add(t.path);break;case`remove`:this.remove(t.path,{recursive:t.recursive});break;case`move`:this.move(t.from,t.to,{collision:t.collision});break}})}getVisibleCount(){return I(this.#e.instrumentation,`store.getVisibleCount`,()=>In(this.#e))}getVisibleSlice(e,t){return I(this.#e.instrumentation,`store.getVisibleSlice`,()=>Hn(this.#e,e,t))}getVisibleRowContext(e){return I(this.#e.instrumentation,`store.getVisibleRowContext`,()=>Vn(this.#e,e))}getVisibleTreeProjection(){return Wn(this.#e)}getVisibleTreeProjectionData(e){return Un(this.#e,e)}getVisibleIndex(e){return I(this.#e.instrumentation,`store.getVisibleIndex`,()=>Gn(this.#e,e))}getPathInfo(e){return I(this.#e.instrumentation,`store.getPathInfo`,()=>{let t=Ht(this.#e,e);if(t==null)return null;let n=J(this.#e,t);return{depth:N(n),kind:P(n)?`directory`:`file`,path:K(this.#e,t)}})}isExpanded(e){return I(this.#e.instrumentation,`store.isExpanded`,()=>{let t=this.requireDirectoryNodeId(e),n=J(this.#e,t);return R(this.#e,t,n)})}expand(e){I(this.#e.instrumentation,`store.expand`,()=>{let t=In(this.#e),n=Kn(this.#e,e);n!=null&&W(this.#e,U(this.#e,t,n))})}collapse(e){I(this.#e.instrumentation,`store.collapse`,()=>{let t=In(this.#e),n=qn(this.#e,e);n!=null&&W(this.#e,U(this.#e,t,n))})}on(e,t){return dt(this.#e,e,t)}getDirectoryLoadState(e){let t=this.requireDirectoryNodeId(e);return ot(this.#e,t)}markDirectoryUnloaded(e){I(this.#e.instrumentation,`store.markDirectoryUnloaded`,()=>{let t=this.requireDirectoryNodeId(e);if(q(this.#e,t).childIds.length>0)throw Error(`Cannot mark a directory with known children as unloaded: "${e}"`);let n=In(this.#e);ct(this.#e,t),W(this.#e,U(this.#e,n,_t({affectedAncestorIds:Vt(this.#e,t),affectedNodeIds:[t],path:e,projectionChanged:this.isDirectoryProjectionVisible(t)})))})}beginChildLoad(e){return I(this.#e.instrumentation,`store.beginChildLoad`,()=>{let t=this.requireDirectoryNodeId(e),n=In(this.#e),r=st(this.#e,t);return W(this.#e,U(this.#e,n,vt({affectedAncestorIds:Vt(this.#e,t),affectedNodeIds:[t],attemptId:r.attemptId,path:e,projectionChanged:this.isDirectoryProjectionVisible(t),reused:r.reused}))),r})}applyChildPatch(e,t){return I(this.#e.instrumentation,`store.applyChildPatch`,()=>{let n=this.resolveActiveDirectoryNodeId(e.nodeId);if(n==null||ot(this.#e,n)!==`loading`||!z(this.#e,n,e.attemptId))return!1;let r=K(this.#e,n);this.validateChildPatch(r,t);let i=In(this.#e),a=[];for(let e of t.operations){fr(r,e);let t=In(this.#e);switch(e.type){case`add`:a.push(U(this.#e,t,Pt(this.#e,e.path)));break;case`remove`:a.push(U(this.#e,t,Ft(this.#e,e.path,{recursive:e.recursive})));break;case`move`:{let n=It(this.#e,e.from,e.to,{collision:e.collision});n!=null&&a.push(U(this.#e,t,n));break}}}let o=a.some(e=>e.projectionChanged)||this.isDirectoryProjectionVisible(n);return W(this.#e,U(this.#e,i,V({affectedAncestorIds:Vt(this.#e,n),affectedNodeIds:[n],attemptId:e.attemptId,childEvents:a,path:K(this.#e,n),projectionChanged:o}))),!0})}completeChildLoad(e){return I(this.#e.instrumentation,`store.completeChildLoad`,()=>{let t=this.resolveActiveDirectoryNodeId(e.nodeId);if(t==null)return!1;let n=In(this.#e),r=lt(this.#e,t,e.attemptId);return W(this.#e,U(this.#e,n,yt({affectedAncestorIds:Vt(this.#e,t),affectedNodeIds:[t],attemptId:e.attemptId,path:K(this.#e,t),projectionChanged:this.isDirectoryProjectionVisible(t),stale:!r}))),r})}failChildLoad(e,t){return I(this.#e.instrumentation,`store.failChildLoad`,()=>{let n=this.resolveActiveDirectoryNodeId(e.nodeId);if(n==null)return!1;let r=In(this.#e),i=B(this.#e,n,e.attemptId,t);return W(this.#e,U(this.#e,r,H({affectedAncestorIds:Vt(this.#e,n),affectedNodeIds:[n],attemptId:e.attemptId,errorMessage:t,path:K(this.#e,n),projectionChanged:this.isDirectoryProjectionVisible(n),stale:!i}))),i})}cleanup(e={}){return I(this.#e.instrumentation,`store.cleanup`,()=>{if(this.#e.transactionStack.length>0)throw Error(`Cleanup cannot run during an open batch or transaction.`);if(Mn(this.#e))throw Error(`Cleanup cannot run while directory loads are active.`);let t=In(this.#e),n=Nn(this.#e,e.mode??`stable`);return W(this.#e,U(this.#e,t,bt({...n,affectedAncestorIds:[],affectedNodeIds:[],projectionChanged:n.idsPreserved===!1}))),n})}getNodeCount(){return this.#e.activeNodeCount}initializeExpandedPaths(e){if(e==null||e.length===0)return 0;let t=0,n=[],r=[],i=0,a=null,o=this.#e.snapshot.segmentTable,s=o.valueById,c=this.#e.snapshot.nodes,l=new Map;for(let u of e){a!=null&&u<a&&(a=null,i=0,n.length=0,r.length=0);let e=u.length>0&&u.charCodeAt(u.length-1)===47?u.length-1:u.length;if(e===0){a=u,i=e,n.length=0,r.length=0;continue}let d=0,f=0;if(a!=null){let t=Math.min(e,i),n=!0;for(let e=0;e<t;e+=1){let t=u.charCodeAt(e);if(t!==a.charCodeAt(e)){n=!1;break}t===47&&(d+=1,f=e+1)}n&&(t===i&&e>t&&u.charCodeAt(t)===47?(d+=1,f=t+1):t===e&&i>t&&a.charCodeAt(t)===47&&(d+=1,f=e+1)),d=Math.min(d,r.length)}let p=d===0?this.#e.snapshot.rootId:r[d-1]??this.#e.snapshot.rootId,m=d,h=!0,g=f;for(;g<=e;){let t=u.indexOf(`/`,g),i=t===-1||t>e?e:t,a=u.slice(g,i),f=q(this.#e,p).childIds,_=m===d?n[m]??0:0,v=_,y,b=l.get(a)??xe(a);l.set(a,b);let x=(e,t)=>{for(v=e;v<t;v+=1){let e=f[v],t=c[e],n=s[t.nameId];if(n===a)return y=e,!0;let r=Ce(L(o,t.nameId),b);if(r>0||r===0&&n>a)return!1}return!1};if(!x(_,f.length)&&_>0&&x(0,_),y===void 0){h=!1;break}if(!P(J(this.#e,y))){h=!1;break}if(n[m]=v,r[m]=y,p=y,m+=1,i===e)break;g=i+1}if(a=u,i=e,n.length=m,r.length=m,!h){a=null,i=0,n.length=0,r.length=0;continue}for(let e=d;e<m;e+=1){let n=r[e];if(n==null)continue;let i=J(this.#e,n);R(this.#e,n,i)||(it(this.#e,n,!0,i),t+=1)}}return t}hasAllDirectoriesExpanded(){for(let e of this.#e.snapshot.directories.keys()){if(e===this.#e.snapshot.rootId)continue;let t=J(this.#e,e);if(!R(this.#e,e,t))return!1}return!0}requireDirectoryNodeId(e){let t=Ht(this.#e,e);if(t==null)throw Error(`Path does not exist: "${e}"`);if(!P(J(this.#e,t)))throw Error(`Path is not a directory: "${e}"`);return t}resolveActiveDirectoryNodeId(e){try{if(!P(J(this.#e,e)))throw Error(`Node is not a directory: ${String(e)}`);return e}catch{return null}}isDirectoryProjectionVisible(e){let t=e;for(;t!==this.#e.snapshot.rootId;){let e=J(this.#e,t).parentId;if(e!==this.#e.snapshot.rootId){let n=J(this.#e,e),r=G(this.#e,e);if(!R(this.#e,e,n)&&r!==t)return!1}t=e}return!0}validateChildPatch(t,n){new e({paths:this.list(t),presorted:!0,sort:this.#e.snapshot.options.sort}).batch(n.operations)}};function fr(e,t){switch(t.type){case`add`:case`remove`:if(!t.path.startsWith(e)||t.path===e)throw Error(`Child patch operation must stay within ${e}: "${t.path}"`);break;case`move`:if(!t.from.startsWith(e)||!t.to.startsWith(e)||t.from===e||t.to===e)throw Error(`Child patch move must stay within ${e}: "${t.from}" -> "${t.to}"`)}}var pr={compact:{itemHeight:24,factor:.8},default:{itemHeight:30,factor:1},relaxed:{itemHeight:36,factor:1.2}};function mr(e,t){if(typeof e==`number`)return{itemHeight:t??pr.default.itemHeight,factor:e};let n=pr[e??`default`];return{itemHeight:t??n.itemHeight,factor:n.factor}}var hr=pr.default.itemHeight,gr=e=>e.startsWith(`f::`)?e.slice(3):e;function _r(e){let t=e.lastIndexOf(`/`);return t<0?{parentPath:``,baseName:e}:{parentPath:e.slice(0,t),baseName:e.slice(t+1)}}function vr(e,t){return e===``?t:`${e}/${t}`}function yr({files:e,path:t,isFolder:n,nextBasename:r}){let i=gr(t),a=r.trim();if(a.length===0)return{error:`Name cannot be empty.`};if(a.includes(`/`))return{error:`Name cannot include "/".`};let{parentPath:o,baseName:s}=_r(i);if(a===s)return{nextFiles:e,sourcePath:i,destinationPath:i,isFolder:n};let c=vr(o,a),l=Array(e.length),u=new Set;if(!n){let t=`${c}/`,r=!1;for(let n=0;n<e.length;n++){let a=e[n];if(a!==i&&a.startsWith(t))return{error:`"${c}" already exists.`};let o=a===i?c:a;if(u.has(o))return{error:`"${c}" already exists.`};u.add(o),l[n]=o,a===i&&(r=!0)}return r?{nextFiles:l,sourcePath:i,destinationPath:c,isFolder:n}:{error:`Could not find the selected file to rename.`}}let d=`${i}/`,f=`${c}/`,p=0;for(let t=0;t<e.length;t++){let n=e[t],r=n===i||n.startsWith(d);if(!r&&(n===c||n.startsWith(f)))return{error:`"${c}" already exists.`};let a=r?`${c}${n.slice(i.length)}`:n;if(u.has(a))return{error:`"${c}" already exists.`};u.add(a),l[t]=a,r&&p++}return p===0?{error:`Could not find the selected folder to rename.`}:{nextFiles:l,sourcePath:i,destinationPath:c,isFolder:n}}function br(e){return e.endsWith(`/`)}function xr(e){let t=e.endsWith(`/`)?e.slice(0,-1):e,n=t.lastIndexOf(`/`),r=n<0?t:t.slice(n+1);return e.endsWith(`/`)?`${r}/`:r}function Sr(e){let t=[],n=new Set;for(let r of e)n.has(r)||(n.add(r),t.push(r));let r=new Set;for(let e of t.toSorted((e,t)=>e.length===t.length?e.localeCompare(t):e.length-t.length)){let t=(e.endsWith(`/`)?e.slice(0,-1):e).split(`/`),n=!1;for(let e=0;e<t.length-1;e+=1){let i=`${t.slice(0,e+1).join(`/`)}/`;if(r.has(i)){n=!0;break}}n||r.add(e)}return t.filter(e=>r.has(e))}function Cr(e,t){return t.includes(e)?Sr(t):[e]}function wr(e,t){return e===t?!0:e==null||t==null?!1:e.kind===t.kind&&e.directoryPath===t.directoryPath&&e.flattenedSegmentPath===t.flattenedSegmentPath&&e.hoveredPath===t.hoveredPath}function Tr(e,t){return{draggedPaths:e,target:t}}function Er(e,t){if(t.kind!==`directory`||t.directoryPath==null)return!1;for(let n of e)if(br(n)&&(t.directoryPath===n||t.directoryPath.startsWith(n)))return!0;return!1}function Dr(e,t){return t.kind===`root`||t.directoryPath==null?xr(e):t.directoryPath}function Or(e,t){let n=e.map(e=>{let n=Dr(e,t);return n===e?null:{from:e,to:n,type:`move`}}).filter(e=>e!=null);return n.length===0?null:{operations:n,result:{draggedPaths:e,operation:n.length===1?`move`:`batch`,target:t}}}function kr(e,t){if(e===t)return!0;if(e.length!==t.length)return!1;for(let n=0;n<e.length;n+=1)if(e[n]!==t[n])return!1;return!0}function Ar(e,t,n){let{paths:r,preparedInput:i}=e;if(i==null){if(r==null)throw Error(`FileTree requires paths or preparedInput`);return{paths:r,preparedInput:void 0}}let a=i.paths;if(r==null)return{paths:a,preparedInput:i};if(!kr(dr.preparePaths(r,n==null?{}:{sort:n}),a))throw Error(`FileTree ${t} received paths and preparedInput for different path lists`);return{paths:a,preparedInput:i}}function jr(e){return e.operation===`add`||e.operation===`remove`||e.operation===`move`||e.operation===`batch`}function Mr(e,t,n){if(e===t)return n;let r=t.endsWith(`/`)?t:`${t}/`;return e.startsWith(r)?`${n.endsWith(`/`)?n:`${n}/`}${e.slice(r.length)}`:e}function Nr(e,t){if(e===t)return!0;let n=t.endsWith(`/`)?t:`${t}/`;return e.startsWith(n)}function Pr(e,t,n=!1){if(e==null)return null;switch(t.operation){case`add`:case`expand`:case`collapse`:case`mark-directory-unloaded`:case`begin-child-load`:case`apply-child-patch`:case`complete-child-load`:case`fail-child-load`:case`cleanup`:return e;case`remove`:return Nr(e,t.path)?n?e:null:e;case`move`:return Mr(e,t.from,t.to);case`batch`:{let r=e;for(let e of t.events)if(r=Pr(r,e,n),r==null)return null;return r}}}function Fr(e){return{canonicalChanged:e.canonicalChanged,projectionChanged:e.projectionChanged,visibleCountDelta:e.visibleCountDelta}}function Ir(e){switch(e.operation){case`add`:return{...Fr(e),operation:`add`,path:e.path};case`remove`:return{...Fr(e),operation:`remove`,path:e.path,recursive:e.recursive};case`move`:return{...Fr(e),from:e.from,operation:`move`,to:e.to}}}function Lr(e){return{...Fr(e),events:e.events.filter(e=>e.operation===`add`||e.operation===`remove`||e.operation===`move`).map(e=>Ir(e)),operation:`batch`}}function Rr(e){switch(e.operation){case`add`:case`remove`:case`move`:return Ir(e);case`batch`:return Lr(e);default:return null}}function zr(e,t){if(e.size!==t.length)return!1;for(let n of t)if(!e.has(n))return!1;return!0}function Br(e){let t=e.endsWith(`/`)?e.slice(0,-1):e;if(t.length===0)return[];let n=t.split(`/`);return n.slice(0,-1).map((e,t)=>`${n.slice(0,t+1).join(`/`)}/`)}function Vr(e){return Br(e).at(-1)??null}function Hr(e,t){return t==null?e:e.startsWith(t)?e.slice(t.length):e}function Ur(e){return e.endsWith(`/`)}var Wr=e=>e.toLowerCase();function Gr(e){let t=e.endsWith(`/`)?e.slice(0,-1):e,n=t.lastIndexOf(`/`);return n<0?t:t.slice(n+1)}function Kr(e){return e.endsWith(`/`)?e.slice(0,-1):e}function qr(e,t){return t&&!e.endsWith(`/`)?`${e}/`:e}var Jr=e=>{let t=e.trim();return t.length===0?``:(t.includes(`\\`)?t.replaceAll(`\\`,`/`):t).toLowerCase()},Yr=Symbol(`FILE_TREE_RENAME_VIEW`),Xr=512,Zr=512;function Qr(e){return e===`top`||e===`center`?e:`nearest`}function $r(e,t,n){if(e===0)return-1;if(n!=null){let e=t(n);if(e!=null)return e;let r=Br(n);for(let e=r.length-1;e>=0;--e){let n=r[e];if(n==null)continue;let i=t(n);if(i!=null)return i}}return 0}function ei(e,t,n){if(e.paths.length===0)return{focusedIndex:-1,getParentIndex:e.getParentIndex,paths:e.paths,posInSetByIndex:e.posInSetByIndex,setSizeByIndex:e.setSizeByIndex};if(t==null)return{focusedIndex:0,getParentIndex:e.getParentIndex,paths:e.paths,posInSetByIndex:e.posInSetByIndex,setSizeByIndex:e.setSizeByIndex};let r=n??(t=>e.visibleIndexByPath.get(t)??null);return{focusedIndex:$r(e.paths.length,r,t),getParentIndex:e.getParentIndex,paths:e.paths,posInSetByIndex:e.posInSetByIndex,setSizeByIndex:e.setSizeByIndex}}var ti=class e{static{Object.defineProperty(e.prototype,Yr,{configurable:!0,value(){return{cancel:()=>{this.#q()},commit:()=>{this.#J()},getPath:()=>this.#T,getValue:()=>this.#E,isActive:()=>this.#T!=null,setValue:e=>{this.#Y(e)}}},writable:!0})}#e;#t=new Set;#n=new Map;#r=null;#i=null;#a=new Map;#o=new Map;#s=-1;#c=null;#l=!1;#u=e=>-1;#d=new Map;#f=null;#p=null;#m=null;#h=null;#g=null;#_;#v;#y;#b=[];#x=new Int32Array;#S=new Int32Array;#C=void 0;#w=!1;#T=null;#E=``;#D=!1;#O=new Set;#k=[];#A;#j=null;#M=null;#N=null;#P=null;#F=null;#I=null;#L=null;#R=0;#z=null;#B=new Set;#V=0;#H;#U=0;#W=!1;#G=0;#K;constructor(e){let{dragAndDrop:t,fileTreeSearchMode:n,initialSearchQuery:r,initialSelectedPaths:i,renaming:a,onSearchChange:o,paths:s,preparedInput:c,...l}=e,u=Ar({paths:s,preparedInput:c},`constructor`,l.sort);this.#e=l,t!=null&&t!==!1&&(this.#r=t===!0?{}:t),this.#w=a!=null&&a!==!1,a!=null&&a!==!1&&a!==!0&&(this.#C=a.canRename,this.#v=a.onError,this.#_=a.onRename),this.#y=o,this.#A=n??`hide-non-matches`,this.#H=this.#le(u.paths,u.preparedInput);let d=i?.map(e=>this.#Ne(e)).filter(e=>e!=null)??[],f=d.at(-1)??null;d.length>0&&(this.#B=new Set(d),this.#z=f,this.#V=1),this.#je(f,!1),r!=null&&this.#Te(r,!1),this.#K=this.#Re()}destroy(){this.#K?.(),this.#K=null,this.#n.clear(),this.#t.clear(),this.#d.clear(),this.#i=null,this.#he()}focusFirstItem(){this.#be().length>0&&this.#Fe(0)}focusLastItem(){this.#G<=0||(this.#Ie(),this.#Fe(this.#G-1))}focusNextItem(){this.#Ae(1)}focusParentItem(){if(this.#c==null)return;let e=Vr(this.#c);if(e==null)return;let t=this.#Z(e);t>=0&&this.#Fe(t)}focusPath(e){let t=this.#H.getPathInfo(e)?.path??null;if(t==null)return;this.#Ie();let n=this.#Z(t);n>=0&&this.#Fe(n)}scrollToPath(e,t){let n=this.#H.getPathInfo(e)?.path??null;if(n==null)return;this.#Ie();let r=this.#xe(n);r<0||this.#Me(r)!=null&&(t?.focus!==!1&&this.#Fe(r,!1),this.#L={id:this.#R+=1,offset:Qr(t?.offset),visibleIndex:r},this.#De())}focusMountedPathFromInput(e){let t=this.#H.getPathInfo(e)?.path??null;if(t==null)return;let n=this.#Z(t);n>=0&&this.#Fe(n)}focusNearestPath(e){let t=this.resolveNearestVisiblePath(e);if(t==null)return null;let n=this.#Z(t);return n>=0?(this.#Fe(n),this.#be()[n]??t):null}focusPreviousItem(){this.#Ae(-1)}getFocusedIndex(){return this.#s}getFocusedItem(){return this.#c==null?null:this.#Q(this.#c)}getFocusedPath(){return this.#c}getScrollRequest(){return this.#L}clearScrollRequest(e){this.#L?.id===e&&(this.#L=null)}resolveNearestVisiblePath(e){let t=this.#be();if(this.#G===0)return null;if(e==null)return this.#c??t[0]??null;let n=this.#H.getPathInfo(e)?.path??e,r=this.#Z(n);return r>=0?t[r]??n:this.#X(n)??this.#c??t[0]??null}getSelectedPaths(){return[...this.#B]}getSelectionVersion(){return this.#V}getVisibleCount(){return this.#G}getVisibleRows(e,t){if(t<e||this.#G===0)return[];let n=Math.max(0,e),r=Math.min(this.#G-1,t);if(r<n)return[];let i=r-n+1;if(this.#F==null&&!this.#l&&r>=this.#b.length&&i<=Zr){let e=[];for(let t=n;t<=r;t+=1){let n=this.#H.getVisibleRowContext(t);if(n==null)break;e.push(this.#ee(n))}return e}if(!this.#l&&r>=this.#b.length&&this.#Ie(),this.#F!=null){let e=Array.from({length:r-n+1},(e,t)=>this.#Se(n+t)),t=new Map,i=e[0]??-1,a=i;for(let n=1;n<=e.length;n+=1){let r=e[n];if(r!=null&&r===a+1){a=r;continue}if(i>=0&&this.#H.getVisibleSlice(i,a).forEach((e,n)=>{t.set(i+n,e)}),r==null){i=-1,a=-1;continue}i=r,a=r}return Array.from({length:r-n+1},(e,r)=>{let i=n+r,a=this.#Se(i),o=t.get(a),s=this.#b[a];if(o==null||s==null)throw Error(`Missing projection row for filtered visible index ${String(i)}`);return this.#$(o,i,a,{ancestorPaths:this.#re(a),path:s})})}return this.#H.getVisibleSlice(n,r).map((e,t)=>{let r=n+t,i=this.#b[r];if(i==null)throw Error(`Missing projection path for visible index ${String(r)}`);return this.#$(e,r,r,{ancestorPaths:this.#re(r),path:i})})}getStickyRowCandidates(e,t){if(this.#F!=null)return null;if(this.#G===0||e<=0||t<=0)return[];let n=[];for(let r=0;r<this.#G;r+=1){let i=e+r*t,a=Math.min(this.#G-1,Math.floor(i/t)),o=this.#te(a,r)??(a>0?this.#te(a-1,r):void 0);if(o==null)break;n.push({row:this.#ee(o),subtreeEndIndex:o.subtreeEndIndex})}return n}getItem(e){let t=this.#H.getPathInfo(e);return t==null?null:this.#Q(t.path,t)}resolveMountedDirectoryPathFromInput(e){let t=this.#H.getPathInfo(e);return t?.kind===`directory`?t.path:null}toggleMountedDirectoryFromInput(e){let t=this.resolveMountedDirectoryPathFromInput(e);t!=null&&this.#ze(t)}selectAllVisiblePaths(){this.#Ie();let e=[...this.#be()];this.#ae(e,this.#c??this.#z)}selectOnlyPath(e){let t=this.#Ne(e);t!=null&&this.#ae([t],t)}selectOnlyMountedPathFromInput(e){this.#ae([e],e)}selectPath(e){let t=this.#Ne(e);t==null||this.#B.has(t)||this.#ae([...this.#B,t])}deselectPath(e){let t=this.#Ne(e);t==null||!this.#B.has(t)||this.#ae([...this.#B].filter(e=>e!==t))}toggleFocusedSelection(){this.#c!=null&&this.togglePathSelectionFromInput(this.#c)}togglePathSelection(e){let t=this.#Ne(e);if(t!=null){if(this.#B.has(t)){this.deselectPath(t);return}this.selectPath(t)}}togglePathSelectionFromInput(e){let t=this.#Ne(e);if(t!=null){if(this.#B.has(t)){this.#ae([...this.#B].filter(e=>e!==t),t);return}this.#ae([...this.#B,t],t)}}selectPathRange(e,t){let n=this.#Ne(e);if(n==null)return;this.#Ie();let r=this.#z,i=r==null?-1:this.#Ce(r),a=this.#Ce(n);if(i===-1||a===-1){let e=t?[...this.#B,n]:[n];this.#ae(e,n);return}let[o,s]=i<=a?[i,a]:[a,i],c=this.#be().slice(o,s+1),l=t?[...this.#B,...c]:c;this.#ae(l,r)}extendSelectionFromFocused(e){if(this.#c==null)return;let t=this.#s;if(t===-1)return;let n=Math.min(this.#G-1,Math.max(0,t+e));if(n===t)return;!this.#l&&n>=this.#b.length&&this.#Ie();let r=this.#be(),i=r[t]??null,a=r[n]??null;if(i==null||a==null)return;let o=new Set(this.#B);o.has(i)&&o.has(a)?o.delete(i):o.add(a),this.#ae([...o],this.#z??i,!1),this.#Fe(n)}getDragAndDropConfig(){return this.#r}isDragAndDropEnabled(){return this.#r!=null}getDragSession(){return this.#i==null?null:{draggedPaths:[...this.#i.draggedPaths],primaryPath:this.#i.primaryPath,target:this.#i.target==null?null:{...this.#i.target}}}startDrag(e){if(this.#r==null)return!1;let t=this.#Ne(e);if(t==null||this.#M!=null&&this.#M.length>0)return!1;let n=this.getSelectedPaths(),r=Cr(t,n);return this.#r.canDrag?.(r)!==!1&&(n.includes(t)||this.#ae([t],t,!1),this.#Pe(t),this.#i={draggedPaths:r,primaryPath:t,target:null},this.#De(),!0)}setDragTarget(e){let t=this.#i;if(t==null)return;let n=e;if(n!=null){let e=Tr(t.draggedPaths,n);(Er(t.draggedPaths,n)||this.#r?.canDrop?.(e)===!1)&&(n=null)}wr(t.target,n)||(this.#i={...t,target:n},this.#De())}cancelDrag(){this.#i!=null&&(this.#i=null,this.#De())}completeDrag(){let e=this.#i;if(e==null)return!1;this.#i=null;let t=e.target==null?null:{...e.target};if(t==null)return this.#De(),!1;let n=Tr(e.draggedPaths,t);if(Er(e.draggedPaths,t)||this.#r?.canDrop?.(n)===!1)return this.#De(),!1;let r=Or(e.draggedPaths,t);if(r==null)return this.#De(),!1;try{if(r.operations.length===1){let e=r.operations[0];if(e==null||e.type!==`move`)throw Error(`Expected a single move operation for one-item drops`);this.#H.move(e.from,e.to,{collision:e.collision})}else this.#ce(r.operations),this.#H.batch(r.operations)}catch(e){return this.#De(),this.#r?.onDropError?.(e instanceof Error?e.message:String(e),n),!1}return this.#r?.onDropComplete?.(r.result),!0}subscribe(e){return this.#t.add(e),e(),()=>{this.#t.delete(e)}}add(e){this.#H.add(e)}remove(e,t={}){this.#H.remove(e,t)}move(e,t,n={}){this.#H.move(e,t,n)}batch(e){this.#H.batch(e)}onMutation(e,t){let n=e,r=t,i=this.#n.get(n);return i??(i=new Set,this.#n.set(n,i)),i.add(r),()=>{let e=this.#n.get(n);e?.delete(r),e?.size===0&&this.#n.delete(n)}}setSearch(e){this.#Te(e,!0)}openSearch(e=``){this.#Te(e,!0)}closeSearch(){this.#Te(null,!0)}isSearchOpen(){return this.#M!==null}getSearchValue(){return this.#M??``}getSearchMatchingPaths(){return this.#k}focusNextSearchMatch(){this.#we(1)}focusPreviousSearchMatch(){this.#we(-1)}startRenaming(e=this.#c??``,t={}){if(!this.#w)return!1;let n=this.#H.getPathInfo(e);if(n==null)return!1;let r=n.path,i=Ur(r),a=Kr(r);if(this.#C?.({isFolder:i,path:a})===!1)return!1;for(let e of Br(r))this.#H.isExpanded(e)||this.#H.expand(e);return this.#ae([r],r,!1),this.#M!=null&&(this.#Te(null,!1),this.#y?.(this.#M)),this.#Pe(r),this.#T=r,this.#E=Gr(r),this.#D=t.removeIfCanceled??!1,this.#De(),!0}#q(){if(this.#T==null)return;let e=this.#T,t=this.#D;if(this.#T=null,this.#E=``,this.#D=!1,t){this.remove(e,Ur(e)?{recursive:!0}:void 0);return}this.#Pe(e),this.#De()}#J(){let e=this.#T;if(e==null)return;if(this.#D&&this.#E.trim().length===0){this.#T=null,this.#E=``,this.#D=!1,this.remove(e,Ur(e)?{recursive:!0}:void 0);return}let t=Ur(e),n=yr({files:this.#H.list(),isFolder:t,nextBasename:this.#E,path:Kr(e)});if(this.#T=null,this.#E=``,this.#D=!1,`error`in n){this.#Pe(e),this.#v?.(n.error),this.#De();return}if(n.sourcePath===n.destinationPath){this.#Pe(e),this.#De();return}this.#_?.({destinationPath:n.destinationPath,isFolder:n.isFolder,sourcePath:n.sourcePath}),this.move(qr(n.sourcePath,t),qr(n.destinationPath,t))}#Y(e){this.#T!=null&&this.#E!==e&&(this.#E=e,this.#De())}resetPaths(e,t={}){let n=!Array.isArray(e),r=n?void 0:e,i=n?e:t,a=this.#H.list().length,o=this.#G,s=Ar({paths:r,preparedInput:i.preparedInput},`resetPaths`,this.#e.sort),c=this.#le(s.paths,s.preparedInput,i.initialExpandedPaths),l=this.#c,u=this.#T,d=this.getSelectedPaths(),f=this.#z;this.#K?.(),this.#H=c,this.#d.clear(),this.#he();let p=d.map(e=>c.getPathInfo(e)?.path??null).filter(e=>e!=null),m=!zr(this.#B,p);this.#B=new Set(p),m&&(this.#V+=1),this.#z=f==null?null:c.getPathInfo(f)?.path??null,this.#T=u==null?null:c.getPathInfo(u)?.path??null,this.#T??(this.#E=``,this.#D=!1),this.#je(l,l!=null||p.length>0||this.#z!=null),this.#K=this.#Re(),this.#De(),this.#Oe({canonicalChanged:!0,operation:`reset`,pathCountAfter:s.paths.length,pathCountBefore:a,projectionChanged:!0,usedPreparedInput:i.preparedInput!=null,visibleCountDelta:this.#G-o})}#X(e){this.#Ie();let t=Vr(e),n=Hr(e,t),r=null,i=null;for(let e of this.#be()){if(Vr(e)!==t)continue;let a=Hr(e,t);if(a<n){r=e;continue}if(a>n){i=e;break}}return r??i}#Z(e){let t=this.#Ce(e);if(t!==-1)return t;let n=Br(e);for(let e=n.length-1;e>=0;--e){let t=n[e];if(t==null)continue;let r=this.#Ce(t);if(r!==-1)return r}return this.#be().length>0?0:-1}#Q(e,t){let n=this.#d.get(e);if(n!=null)return n;let r=t??this.#H.getPathInfo(e);if(r==null)return null;let i=r.kind===`directory`?this.#oe(r.path):this.#se(r.path);return this.#d.set(r.path,i),i}#$(e,t,n,r){return{ancestorPaths:r.ancestorPaths,depth:e.depth,flattenedSegments:e.flattenedSegments?.map(e=>({isTerminal:e.isTerminal,name:e.name,path:e.path})),hasChildren:e.hasChildren,index:t,isExpanded:e.isExpanded,isFlattened:e.isFlattened,isFocused:r.path===this.#c,isSelected:this.#B.has(r.path),kind:e.kind,level:e.depth,name:e.name,path:r.path,posInSet:r.posInSet??this.#x[n]??0,setSize:r.setSize??this.#S[n]??0}}#ee(e){return this.#$(e.row,e.index,e.index,{ancestorPaths:e.ancestorPaths,path:e.row.path,posInSet:e.posInSet,setSize:e.setSize})}#te(e,t){let n=this.#H.getVisibleRowContext(e);return n==null?void 0:n.ancestorRows[t]??(t===n.ancestorRows.length&&n.row.kind===`directory`&&n.row.isExpanded?n:void 0)}#ne(e){let t=this.#a.get(e);if(t!=null)return t;let n=this.#u(e),r=n<0?[]:[...this.#ne(n),n];return this.#a.set(e,r),r}#re(e){let t=this.#o.get(e);if(t!=null)return t;let n=this.#ne(e).map(e=>this.#b[e]??``).filter(e=>e!==``);return this.#o.set(e,n),n}#ie(e){this.#H.collapse(e)}#ae(e,t=this.#z,n=!0){let r=[...new Set(e)],i=!zr(this.#B,r),a=this.#z!==t;!i&&!a||(this.#B=new Set(r),this.#z=t,i&&(this.#V+=1),n&&this.#De())}#oe(e){return{collapse:()=>{this.#ie(e)},deselect:()=>{this.deselectPath(e)},expand:()=>{this.#ke(e)},focus:()=>{this.focusPath(e)},getPath:()=>e,isDirectory:()=>!0,isExpanded:()=>this.#H.isExpanded(e),isFocused:()=>this.#c===e,isSelected:()=>this.#B.has(e),select:()=>{this.selectPath(e)},toggleSelect:()=>{this.togglePathSelection(e)},toggle:()=>{this.#ze(e)}}}#se(e){return{deselect:()=>{this.deselectPath(e)},focus:()=>{this.focusPath(e)},getPath:()=>e,isDirectory:()=>!1,isFocused:()=>this.#c===e,isSelected:()=>this.#B.has(e),select:()=>{this.selectPath(e)},toggleSelect:()=>{this.togglePathSelection(e)}}}#ce(e){let t=this.#H.list();this.#le(t).batch(e)}#le(e,t,n){return new dr({...this.#e,paths:e,preparedInput:t??void 0,...n===void 0?{}:{initialExpandedPaths:n}})}#ue(){return this.#h??=this.#H.list(),this.#h}#de(){if(this.#m!=null)return this.#m;let e=new Set;for(let t of this.#ue()){e.add(t);for(let n of Br(t))e.add(n)}return this.#m=[...e].sort(),this.#m}#fe(){return this.#g??=this.#ue().map(Wr),this.#g}#pe(){return this.#f??=this.#de().filter(e=>e.endsWith(`/`)),this.#f}#me(){return this.#p??=this.#pe().map(Wr),this.#p}#he(){this.#f=null,this.#p=null,this.#m=null,this.#h=null,this.#g=null}#ge(){return this.#pe().filter(e=>this.#H.isExpanded(e))}#_e(e){let t=new Set(this.#j??[]);if(e)for(let e of this.#B)for(let n of Br(e))t.add(n);this.#ve(t)}#ve(e){this.#W=!0;try{for(let t of this.#pe()){let n=e.has(t),r=this.#H.isExpanded(t);n&&!r?this.#H.expand(t):!n&&r&&this.#H.collapse(t)}}finally{this.#W=!1}}#ye(){if(this.#M==null||this.#M.length===0){this.#k=[],this.#F=null,this.#I=null,this.#P=null,this.#G=this.#U;return}let e=this.#b;if(this.#k=e.filter(e=>this.#O.has(e)),this.#A!==`hide-non-matches`||this.#O.size===0){this.#F=null,this.#I=null,this.#P=null,this.#G=this.#U;return}let t=[],n=[],r=new Map;for(let[i,a]of e.entries())this.#N?.has(a)===!0&&(r.set(a,n.length),t.push(i),n.push(a));this.#F=t,this.#I=n,this.#P=r,this.#G=n.length}#be(){return this.#I??this.#b}#xe(e){return this.#I==null?this.#H.getVisibleIndex(e)??-1:this.#P?.get(e)??-1}#Se(e){return this.#F?.[e]??e}#Ce(e){return this.#P?.get(e)??this.#H.getVisibleIndex(e)??-1}#we(e){let t=this.#k;if(t.length===0)return;let n=this.#c,r=n==null?-1:t.indexOf(n),i=t[r<0?e>0?0:t.length-1:Math.min(t.length-1,Math.max(0,r+e))];i!=null&&this.focusPath(i)}#Te(e,t){let n=e==null?null:Jr(e),r=this.#M;if(r!==n){if(r==null&&n!=null&&(this.#j=this.#ge()),this.#M=n,n==null)this.#_e(!0),this.#j=null,this.#O.clear(),this.#N=null,this.#je(this.#c,!0);else if(n.length===0)this.#_e(!1),this.#O.clear(),this.#N=null,this.#je(this.#c,!0);else{let e=this.#Ee();this.#je(e,!0)}t&&(this.#y?.(this.#M),this.#De())}}#Ee(){if(this.#M==null||this.#M.length===0)return this.#O.clear(),this.#c;let e=this.#M,t=this.#ue(),n=this.#fe(),r=[],i=new Set,a=null;for(let o=0;o<t.length;o+=1){if(!n[o].includes(e))continue;let s=t[o];r.push(s),i.add(s),a??=s}let o=this.#pe(),s=this.#me();for(let t=0;t<o.length;t+=1){if(!s[t].includes(e))continue;let n=o[t];i.has(n)||(r.push(n),i.add(n),a??=n)}this.#O=i;let c=this.#A===`hide-non-matches`&&r.length>0?new Set:null;this.#N=c;let l=this.#A===`expand-matches`?new Set(this.#j??[]):new Set;for(let e of r){c?.add(e),e.endsWith(`/`)&&l.add(e);for(let t of Br(e))l.add(t),c?.add(t)}return this.#ve(l),a??this.#c}#De(e){for(let t of this.#t)t(e)}#Oe(e){this.#n.get(e.operation)?.forEach(t=>{t(e)}),this.#n.get(`*`)?.forEach(t=>{t(e)})}#ke(e){for(let t of Br(e))this.#H.isExpanded(t)||this.#H.expand(t);this.#H.isExpanded(e)||this.#H.expand(e)}#Ae(e){let t=this.#G;if(t===0)return;let n=this.#s===-1?0:this.#s,r=Math.min(t-1,Math.max(0,n+e));(r!==n||this.#s===-1)&&(!this.#l&&this.#F==null&&r>=this.#b.length&&this.#Ie(),this.#Fe(r))}#je(e,t=!0){let n=this.#H.getVisibleCount();this.#U=n;let r=ei(this.#H.getVisibleTreeProjectionData(t?void 0:Math.min(n,Xr)),e,t?e=>this.#H.getVisibleIndex(e):void 0);this.#a.clear(),this.#o.clear(),this.#l=r.paths.length>=n,this.#u=r.getParentIndex,this.#b=r.paths,this.#x=r.posInSetByIndex,this.#S=r.setSizeByIndex,this.#ye(),this.#s=e==null?this.#be().length>0?0:-1:this.#Z(e),this.#c=this.#s<0?null:this.#Me(this.#s)}#Me(e){return this.#be()[e]??(this.#F==null?this.#H.getVisibleRowContext(e)?.row.path??null:null)}#Ne(e){return this.#H.getPathInfo(e)?.path??null}#Pe(e){if(e==null)return;let t=this.#Z(e);t>=0&&this.#Fe(t,!1)}#Fe(e,t=!0){let n=this.#Me(e);n!=null&&(this.#s!==e||this.#c!==n)&&(this.#s=e,this.#c=n,t&&this.#De())}#Ie(){this.#l||this.#je(this.#c,!0)}#Le(e){let t=Pr(this.#T,e);t==null&&this.#T!=null&&(this.#E=``),this.#T=t;let n=Pr(this.#c,e,!0),r=[...this.#B].map(t=>Pr(t,e)).filter(e=>e!=null).map(e=>this.#H.getPathInfo(e)?.path??null).filter(e=>e!=null),i=Pr(this.#z,e),a=i==null?null:this.#H.getPathInfo(i)?.path??null,o=[...new Set(r)];return zr(this.#B,o)||(this.#B=new Set(o),this.#V+=1),this.#z=a,n}#Re(){return this.#H.on(`*`,e=>{if(this.#W)return;e.canonicalChanged&&(this.#d.clear(),this.#he()),this.#i!=null&&jr(e)&&(this.#i=null);let t=jr(e)?this.#Le(e):this.#c,n=this.#M!=null&&this.#M.length>0?this.#Ee():this.#M===``?this.#c:t,r=this.#M!=null||e.operation!==`expand`&&e.operation!==`collapse`;this.#je(n,r),this.#De(e);let i=Rr(e);i!=null&&this.#Oe(i)})}#ze(e){if(this.#H.isExpanded(e)){this.#ie(e);return}this.#ke(e)}},ni=e=>{if(e==null||e.length===0)return`0`;let t=`${e.length}`;for(let n of e)t+=`\0${n.path}\0${n.status}`;return t};function ri(e){let t=e.endsWith(`/`),n=``,r=-1;for(let t=0;t<=e.length;t+=1){if(e[t]!==`/`&&t!==e.length){r===-1&&(r=t);continue}r!==-1&&(n!==``&&(n+=`/`),n+=e.slice(r,t),r=-1)}return n===``?null:{isDirectory:t,path:n}}function ii(e){let t=e.endsWith(`/`)?e.slice(0,-1):e;if(t.length===0)return[];let n=[],r=0;for(;;){let e=t.indexOf(`/`,r);if(e===-1)break;n.push(t.slice(0,e+1)),r=e+1}return n}function ai(e,t){return t?`${e}/`:e}function oi(e,t=null){let n=ni(e==null?void 0:[...e]);if(n===`0`)return null;if(t?.signature===n)return t;let r=new Map,i=new Set,a=new Set,o=new Map;for(let t of e??[]){let e=ci(t);e!=null&&di({canonicalPath:e.canonicalPath,changeCountByDirectoryPath:o,directoriesWithChanges:i,ignoredDirectoryPaths:a,isDirectory:e.isDirectory,status:t.status,statusByPath:r})}return r.size===0?null:{changeCountByDirectoryPath:o,directoriesWithChanges:i,ignoredDirectoryPaths:a,signature:n,statusByPath:r}}function si(e,t){let n=t?.remove??[],r=t?.set??[];if(n.length===0&&r.length===0)return e;let i=new Map(e?.statusByPath),a=new Set(e?.directoriesWithChanges),o=new Set(e?.ignoredDirectoryPaths),s=new Map(e?.changeCountByDirectoryPath),c=!1;for(let e of n){let t=li(e);t!=null&&(c=ui({canonicalPath:t.canonicalPath,changeCountByDirectoryPath:s,directoriesWithChanges:a,ignoredDirectoryPaths:o,statusByPath:i})||c)}for(let e of r){let t=ci(e);t!=null&&(c=di({canonicalPath:t.canonicalPath,changeCountByDirectoryPath:s,directoriesWithChanges:a,ignoredDirectoryPaths:o,isDirectory:t.isDirectory,status:e.status,statusByPath:i})||c)}return c?i.size===0?null:{changeCountByDirectoryPath:s,directoriesWithChanges:a,ignoredDirectoryPaths:o,signature:mi(i),statusByPath:i}:e}function ci(e){return li(e.path)}function li(e){let t=ri(e);if(t!=null)return{canonicalPath:ai(t.path,t.isDirectory),isDirectory:t.isDirectory}}function ui({canonicalPath:e,changeCountByDirectoryPath:t,directoriesWithChanges:n,ignoredDirectoryPaths:r,statusByPath:i}){let a=i.get(e);return a!=null&&(i.delete(e),a===`ignored`&&e.endsWith(`/`)&&r.delete(e),pi(t,n,e),!0)}function di({canonicalPath:e,changeCountByDirectoryPath:t,directoriesWithChanges:n,ignoredDirectoryPaths:r,isDirectory:i,status:a,statusByPath:o}){let s=o.get(e);return s!==a&&(s??fi(t,n,e),o.set(e,a),a===`ignored`&&i?r.add(e):i&&r.delete(e),!0)}function fi(e,t,n){for(let r of ii(n))e.set(r,(e.get(r)??0)+1),t.add(r)}function pi(e,t,n){for(let r of ii(n)){let n=(e.get(r)??0)-1;n>0?e.set(r,n):(e.delete(r),t.delete(r))}}function mi(e){let t=`${e.size}`;for(let[n,r]of e)t+=`\0${n}\0${r}`;return t}var Y,hi,gi,_i,vi,yi,bi,xi,Si,Ci,wi={},Ti=[],Ei=Array.isArray,Di=Ti.slice,Oi=Object.assign;function ki(e){e&&e.parentNode&&e.remove()}function Ai(e,t,n){var r,i,a,o={};for(a in t)a==`key`?r=t[a]:a==`ref`&&typeof e!=`function`?i=t[a]:o[a]=t[a];return arguments.length>2&&(o.children=arguments.length>3?Di.call(arguments,2):n),ji(e,o,r,i,null)}function ji(e,t,n,r,i){var a={type:e,props:t,key:n,ref:r,__k:null,__:null,__b:0,__e:null,__c:null,constructor:void 0,__v:i??++hi,__i:-1,__u:0};return i==null&&Y.vnode!=null&&Y.vnode(a),a}function Mi(e){return e.children}function Ni(e,t){this.props=e,this.context=t,this.__g=0}function Pi(e,t){if(t==null)return e.__?Pi(e.__,e.__i+1):null;for(var n;t<e.__k.length;t++)if((n=e.__k[t])!=null&&n.__e!=null)return n.__e;return typeof e.type==`function`?Pi(e):null}function Fi(e){var t,n;if((e=e.__)!=null&&e.__c!=null){for(e.__e=null,t=0;t<e.__k.length;t++)if((n=e.__k[t])!=null&&n.__e!=null){e.__e=n.__e;break}return Fi(e)}}function Ii(e){(8&e.__g||!(e.__g|=8)||!gi.push(e)||vi++)&&_i==Y.debounceRendering||((_i=Y.debounceRendering)||queueMicrotask)(Li)}function Li(){for(var e,t,n,r,i,a,o,s,c=1;gi.length;)gi.length>c&&gi.sort(yi),e=gi.shift(),c=gi.length,8&e.__g&&(n=void 0,i=(r=(t=e).__v).__e,a=[],o=[],(s=t.__P)&&((n=Oi({},r)).__v=r.__v+1,Y.vnode&&Y.vnode(n),Gi(s,n,r,t.__n,s.namespaceURI,32&r.__u?[i]:null,a,i??Pi(r),!!(32&r.__u),o,s.ownerDocument),n.__v=r.__v,n.__.__k[n.__i]=n,qi(a,n,o),n.__e!=i&&Fi(n)));vi=0}function Ri(e,t,n,r,i,a,o,s,c,l,u,d){var f,p,m,h,g,_,v,y=r&&r.__k||Ti,b=t.length;for(c=zi(n,t,y,c,b),f=0;f<b;f++)(m=n.__k[f])!=null&&(p=m.__i==-1?wi:y[m.__i]||wi,m.__i=f,_=Gi(e,m,p,i,a,o,s,c,l,u,d),h=m.__e,m.ref&&p.ref!=m.ref&&(p.ref&&Xi(p.ref,null,m),u.push(m.ref,m.__c||h,m)),g==null&&h!=null&&(g=h),(v=!!(4&m.__u))||p.__k===m.__k?c=Bi(m,c,e,v):typeof m.type==`function`&&_!==void 0?c=_:h&&(c=h.nextSibling),m.__u&=-7);return n.__e=g,c}function zi(e,t,n,r,i){var a,o,s,c,l,u=n.length,d=u,f=0;for(e.__k=Array(i),a=0;a<i;a++)(o=t[a])!=null&&typeof o!=`boolean`&&typeof o!=`function`?(c=a+f,(o=e.__k[a]=typeof o==`string`||typeof o==`number`||typeof o==`bigint`||o.constructor==String?ji(null,o,null,null,null):Ei(o)?ji(Mi,{children:o},null,null,null):o.constructor==null&&o.__b>0?ji(o.type,o.props,o.key,o.ref?o.ref:null,o.__v):o).__=e,o.__b=e.__b+1,s=null,(l=o.__i=Vi(o,n,c,d))!=-1&&(d--,(s=n[l])&&(s.__u|=2)),s==null||s.__v==null?(l==-1&&(i>u?f--:i<u&&f++),typeof o.type!=`function`&&(o.__u|=4)):l!=c&&(l==c-1?f--:l==c+1?f++:(l>c?f--:f++,o.__u|=4))):e.__k[a]=null;if(d)for(a=0;a<u;a++)(s=n[a])!=null&&!(2&s.__u)&&(s.__e==r&&(r=Pi(s)),Zi(s,s));return r}function Bi(e,t,n,r){var i,a;if(typeof e.type==`function`){for(i=e.__k,a=0;i&&a<i.length;a++)i[a]&&(i[a].__=e,t=Bi(i[a],t,n,r));return t}e.__e!=t&&(r&&(t&&e.type&&!t.parentNode&&(t=Pi(e)),n.insertBefore(e.__e,t||null)),t=e.__e);do t&&=t.nextSibling;while(t!=null&&t.nodeType==8);return t}function Vi(e,t,n,r){var i,a,o,s=e.key,c=e.type,l=t[n],u=l!=null&&!(2&l.__u);if(l===null&&e.key==null||u&&s==l.key&&c==l.type)return n;if(r>+!!u){for(i=n-1,a=n+1;i>=0||a<t.length;)if((l=t[o=i>=0?i--:a++])!=null&&!(2&l.__u)&&s==l.key&&c==l.type)return o}return-1}function Hi(e,t,n){t[0]==`-`?e.setProperty(t,n??``):e[t]=n??``}function Ui(e,t,n,r,i){var a;n:if(t==`style`){if(typeof n==`string`)e.style.cssText=n;else{if(typeof r==`string`&&(e.style.cssText=r=``),r)for(t in r)n&&t in n||Hi(e.style,t,``);if(n)for(t in n)r&&n[t]==r[t]||Hi(e.style,t,n[t])}}else if(t[0]==`o`&&t[1]==`n`)a=t!=(t=t.replace(bi,`$1`)),(t=t.slice(2))[0].toLowerCase()!=t[0]&&(t=t.toLowerCase()),e.__l||={},e.__l[t+a]=n,n?r?n.l=r.l:(n.l=xi,e.addEventListener(t,a?Ci:Si,a)):e.removeEventListener(t,a?Ci:Si,a);else{if(i==`http://www.w3.org/2000/svg`)t=t.replace(/xlink(H|:h)/,`h`).replace(/sName$/,`s`);else if(t!=`width`&&t!=`height`&&t!=`href`&&t!=`list`&&t!=`form`&&t!=`tabIndex`&&t!=`download`&&t!=`rowSpan`&&t!=`colSpan`&&t!=`role`&&t!=`popover`&&t in e)try{e[t]=n??``;break n}catch{}typeof n==`function`||(n==null||!1===n&&t[4]!=`-`?e.removeAttribute(t):e.setAttribute(t,t==`popover`&&n==1?``:n))}}function Wi(e){return function(t){if(this.__l){var n=this.__l[t.type+e];if(t.u==null)t.u=xi++;else if(t.u<n.l)return;return n(Y.event?Y.event(t):t)}}}function Gi(e,t,n,r,i,a,o,s,c,l,u){var d,f,p,m,h,g,_,v,y,b,x,S,C,w,ee,te,T,E,D,O,ne,k=t.type;if(t.constructor!=null)return null;128&n.__u&&(c=!!(32&n.__u),n.__c.__z&&(s=t.__e=n.__e=(a=n.__c.__z)[0],n.__c.__z=null)),(d=Y.__b)&&d(t);n:if(typeof k==`function`)try{if(v=t.props,y=`prototype`in k&&k.prototype.render,b=(d=k.contextType)&&r[d.__c],x=d?b?b.props.value:d.__:r,n.__c?2&(f=t.__c=n.__c).__g&&(f.__g|=1,_=!0):(y?t.__c=f=new k(v,x):(t.__c=f=new Ni(v,x),f.constructor=k,f.render=Qi),b&&b.sub(f),f.props=v,f.state||(f.state={}),f.context=x,f.__n=r,p=!0,f.__g|=8,f.__h=[],f._sb=[]),y&&f.__s==null&&(f.__s=f.state),y&&k.getDerivedStateFromProps!=null&&(f.__s==f.state&&(f.__s=Oi({},f.__s)),Oi(f.__s,k.getDerivedStateFromProps(v,f.__s))),m=f.props,h=f.state,f.__v=t,p)y&&k.getDerivedStateFromProps==null&&f.componentWillMount!=null&&f.componentWillMount(),y&&f.componentDidMount!=null&&f.__h.push(f.componentDidMount);else{if(y&&k.getDerivedStateFromProps==null&&v!==m&&f.componentWillReceiveProps!=null&&f.componentWillReceiveProps(v,x),!(4&f.__g)&&f.shouldComponentUpdate!=null&&!1===f.shouldComponentUpdate(v,f.__s,x)||t.__v==n.__v){for(t.__v!=n.__v&&(f.props=v,f.state=f.__s,f.__g&=-9),t.__e=n.__e,t.__k=n.__k,t.__k.some(function(e){e&&(e.__=t)}),S=0;S<f._sb.length;S++)f.__h.push(f._sb[S]);f._sb=[],f.__h.length&&o.push(f);break n}f.componentWillUpdate!=null&&f.componentWillUpdate(v,f.__s,x),y&&f.componentDidUpdate!=null&&f.__h.push(function(){f.componentDidUpdate(m,h,g)})}if(f.context=x,f.props=v,f.__P=e,f.__g&=-5,C=Y.__r,w=0,y){for(f.state=f.__s,f.__g&=-9,C&&C(t),d=f.render(f.props,f.state,f.context),ee=0;ee<f._sb.length;ee++)f.__h.push(f._sb[ee]);f._sb=[]}else do f.__g&=-9,C&&C(t),d=f.render(f.props,f.state,f.context),f.state=f.__s;while(8&f.__g&&++w<25);f.state=f.__s,f.getChildContext!=null&&(r=Oi({},r,f.getChildContext())),y&&!p&&f.getSnapshotBeforeUpdate!=null&&(g=f.getSnapshotBeforeUpdate(m,h)),te=d,d!=null&&d.type===Mi&&d.key==null&&(te=Ji(d.props.children)),s=Ri(e,Ei(te)?te:[te],t,n,r,i,a,o,s,c,l,u),t.__u&=-161,f.__h.length&&o.push(f),_&&(f.__g&=-4)}catch(e){if(t.__v=null,c||a!=null){if(e.then){for(T=0,E=!1,t.__u|=c?160:128,t.__c.__z=[],D=0;D<a.length;D++)(O=a[D])==null||E||(O.nodeType==8&&O.data==`$s`?(T>0&&t.__c.__z.push(O),T++,a[D]=null):O.nodeType==8&&O.data==`/$s`?(--T>0&&t.__c.__z.push(O),E=T===0,s=a[D],a[D]=null):T>0&&(t.__c.__z.push(O),a[D]=null));if(!E){for(;s&&s.nodeType==8&&s.nextSibling;)s=s.nextSibling;a[a.indexOf(s)]=null,t.__c.__z=[s]}t.__e=s}else{for(ne=a.length;ne--;)ki(a[ne]);Ki(t)}}else t.__e=n.__e,t.__k=n.__k,e.then||Ki(t);Y.__e(e,t,n)}else s=t.__e=Yi(n.__e,t,n,r,i,a,o,c,l,u);return(d=Y.diffed)&&d(t),128&t.__u?void 0:s}function Ki(e){e&&e.__c&&(e.__c.__g|=4),e&&e.__k&&e.__k.forEach(Ki)}function qi(e,t,n){for(var r=0;r<n.length;r++)Xi(n[r],n[++r],n[++r]);Y.__c&&Y.__c(t,e),e.some(function(t){try{e=t.__h,t.__h=[],e.some(function(e){e.call(t)})}catch(e){Y.__e(e,t.__v)}})}function Ji(e){return typeof e!=`object`||!e||e.__b&&e.__b>0?e:Ei(e)?e.map(Ji):Oi({},e)}function Yi(e,t,n,r,i,a,o,s,c,l){var u,d,f,p,m,h,g,_,v=n.props,y=t.props,b=t.type;if(b==`svg`?i=`http://www.w3.org/2000/svg`:b==`math`?i=`http://www.w3.org/1998/Math/MathML`:i||=`http://www.w3.org/1999/xhtml`,a!=null){for(u=0;u<a.length;u++)if((m=a[u])&&`setAttribute`in m==!!b&&(b?m.localName==b:m.nodeType==3)){e=m,a[u]=null;break}}if(e==null){if(b==null)return l.createTextNode(y);e=l.createElementNS(i,b,y.is&&y),s&&=(Y.__m&&Y.__m(t,a),!1),a=null}if(b==null)v===y||s&&e.data==y||(e.data=y);else{if(a&&=Di.call(e.childNodes),v=n.props||wi,!s&&a!=null)for(v={},u=0;u<e.attributes.length;u++)v[(m=e.attributes[u]).name]=m.value;for(u in v)if(m=v[u],u!=`children`){if(u==`dangerouslySetInnerHTML`)f=m;else if(!(u in y)){if(u==`value`&&`defaultValue`in y||u==`checked`&&`defaultChecked`in y)continue;Ui(e,u,null,m,i)}}for(u in _=1&n.__u,y)m=y[u],u==`children`?p=m:u==`dangerouslySetInnerHTML`?d=m:u==`value`?h=m:u==`checked`?g=m:s&&typeof m!=`function`||v[u]===m&&!_||Ui(e,u,m,v[u],i);if(d)s||f&&(d.__html==f.__html||d.__html==e.innerHTML)||(e.innerHTML=d.__html),t.__k=[];else if(f&&(e.innerHTML=``),Ri(b==`template`?e.content:e,Ei(p)?p:[p],t,n,r,b==`foreignObject`?`http://www.w3.org/1999/xhtml`:i,a,o,a?a[0]:n.__k&&Pi(n,0),s,c,l),a!=null)for(u=a.length;u--;)ki(a[u]);s||(u=`value`,b==`progress`&&h==null?e.removeAttribute(`value`):h==null||h===e[u]&&(b!==`progress`||h)||Ui(e,u,h,v[u],i),u=`checked`,g!=null&&g!=e[u]&&Ui(e,u,g,v[u],i))}return e}function Xi(e,t,n){try{if(typeof e==`function`){var r=typeof e.__u==`function`;r&&e.__u(),r&&t==null||(e.__u=e(t))}else e.current=t}catch(e){Y.__e(e,n)}}function Zi(e,t,n){var r,i;if(Y.unmount&&Y.unmount(e),(r=e.ref)&&(r.current&&r.current!=e.__e||Xi(r,null,t)),(r=e.__c)!=null){if(r.componentWillUnmount)try{r.componentWillUnmount()}catch(e){Y.__e(e,t)}r.__P=null}if(r=e.__k)for(i=0;i<r.length;i++)r[i]&&Zi(r[i],t,n||typeof e.type!=`function`);n||ki(e.__e),e.__e&&e.__e.__l&&(e.__e.__l=null),e.__e=e.__c=e.__=null}function Qi(e,t,n){return this.constructor(e,n)}function $i(e,t){var n,r,i,a;t==document&&(t=document.documentElement),Y.__&&Y.__(e,t),r=(n=!!(e&&32&e.__u))?null:t.__k,e=t.__k=Ai(Mi,null,[e]),i=[],a=[],Gi(t,e,r||wi,wi,t.namespaceURI,r?null:t.firstChild?Di.call(t.childNodes):null,i,r?r.__e:t.firstChild,n,a,t.ownerDocument),qi(i,e,a)}function ea(e,t){e.__u|=32,$i(e,t)}Y={__e:function(e,t,n,r){for(var i,a,o;t=t.__;)if((i=t.__c)&&!(1&i.__g)){i.__g|=4;try{if((a=i.constructor)&&a.getDerivedStateFromError!=null&&(i.setState(a.getDerivedStateFromError(e)),o=8&i.__g),i.componentDidCatch!=null&&(i.componentDidCatch(e,r||{}),o=8&i.__g),o)return void(i.__g|=2)}catch(t){e=t}}throw vi=0,e}},hi=0,Ni.prototype.setState=function(e,t){var n=this.__s!=null&&this.__s!=this.state?this.__s:this.__s=Oi({},this.state);typeof e==`function`&&(e=e(Oi({},n),this.props)),e&&Oi(n,e),e!=null&&this.__v&&(t&&this._sb.push(t),Ii(this))},Ni.prototype.forceUpdate=function(e){this.__v&&(this.__g|=4,e&&this.__h.push(e),Ii(this))},Ni.prototype.render=Mi,gi=[],vi=0,yi=function(e,t){return e.__v.__b-t.__v.__b},bi=/(PointerCapture)$|Capture$/i,xi=0,Si=Wi(!1),Ci=Wi(!0);var ta=0;Array.isArray;function X(e,t,n,r,i,a){t||={};var o,s,c=t;if(`ref`in c&&typeof e!=`function`)for(s in c={},t)s==`ref`?o=t[s]:c[s]=t[s];var l={type:e,props:c,key:n,ref:o,__k:null,__:null,__b:0,__e:null,__c:null,constructor:void 0,__v:--ta,__i:-1,__u:0,__source:i,__self:a};return Y.vnode&&Y.vnode(l),l}var na=16,ra=16,ia={};function aa({name:e,remappedFrom:t,token:n,width:r,height:i,viewBox:a,label:o,alignCapitals:s=!1}){"use no memo";let c=`#${e.replace(/^#/,``)}`,{width:l,height:u,viewBox:d}=ia[e]??{width:na,height:ra},f=r??l,p=i??u,m=a??d??`0 0 ${l} ${u}`;return X(`svg`,{"data-icon-name":t??e,"data-icon-token":n,"data-align-capitals":s,...o==null?{"aria-hidden":!0}:{"aria-label":o,role:`img`},viewBox:m,width:f,height:p,children:X(`use`,{href:c})})}var oa=e=>e!==void 0&&/\s/.test(e),sa=(e,t)=>{let n=t=>oa(e[t-1])||oa(e[t]);if(!n(t))return t;for(let r=1;r<e.length;r++){let i=t-r;if(i>0&&!n(i))return i;let a=t+r;if(a<e.length&&!n(a))return a}return t},ca=e=>sa(e,Math.ceil(e.length/2)),la=e=>{if(e.length<2)return[e,``];let t=ca(e);return[e.slice(0,t),e.slice(t)]},ua=e=>{if(e.length<4)return[e,``];let t=e.lastIndexOf(`.`)+1,n=e.length-t>10,r=t>=1&&!n?t:ca(e);return[e.slice(0,r),e.slice(r)]},da=e=>{if(e.length<4)return[e,``];let t=e.lastIndexOf(`/`)+1,n=e.length-t>25,r=t>=1&&!n?t:Math.ceil(e.length/2);return[e.slice(0,r),e.slice(r)]},fa=(e,{splitIndex:t}={})=>{if(typeof t!=`number`){let t=Math.ceil(e.length/2);return[e.slice(0,t),e.slice(t)]}return[e.slice(0,t),e.slice(t)]},pa=(e,{splitOffset:t}={})=>{if(typeof t!=`number`||t<=0||t>=e.length){let t=Math.ceil(e.length/2);return[e.slice(0,t),e.slice(t)]}let n=e.length-t;return[e.slice(0,n),e.slice(n)]},ma=(e,{splitOffset:t}={})=>{if(typeof t!=`number`||t<=0||t>=e.length){let t=Math.ceil(e.length/2);return[e.slice(0,t),e.slice(t)]}let n=t;return[e.slice(0,n),e.slice(n)]};function ha({children:e,marker:t,variant:n=`default`}){"use no memo";return X(`div`,{"aria-hidden":!0,"data-truncate-marker-cell":!0,children:X(`div`,{"data-truncate-marker":!0,children:typeof t==`function`?t({children:e}):n===`fade`?X(`span`,{"data-truncate-fade":!0}):t})})}function ga(e){"use no memo";let{mode:t,children:n}=e;return X(`div`,{children:[X(`div`,{"data-truncate-content":`visible`,children:t===`fruncate`?X(`span`,{children:n}):n}),X(`div`,{"data-truncate-content":`overflow`,"aria-hidden":!0,children:t===`fruncate`?X(`span`,{children:n}):n})]})}function _a({children:e,mode:t=`truncate`,marker:n=`…`,variant:r=`default`,...i}){"use no memo";let a=X(ga,{mode:t,children:e},`content`),o=X(ha,{marker:n,mode:t,variant:r},`marker`),s=X(`div`,{"data-truncate-fill":!0},`fill`);return X(`div`,{"data-truncate-container":t,"data-truncate-variant":r,...i,children:X(`div`,{"data-truncate-grid":!0,children:t===`truncate`?[a,o]:[o,a,s]})})}function va({children:e,...t}){"use no memo";return X(_a,{mode:`truncate`,...t,children:e})}function ya({children:e,...t}){"use no memo";return X(_a,{mode:`fruncate`,...t,children:e})}function ba({children:e,contents:t,priority:n=`end`,split:r=`center`,minimumLength:i=12,className:a,style:o,...s}){"use no memo";let c=null,l=null;if(Array.isArray(t)){if(t.length!==2)return console.error(`MiddleTruncate: contents must be an array of two items`),null;c=X(va,{...s,children:t[0]}),l=X(ya,{...s,children:t[1]})}else{if(typeof e!=`string`)return console.error(`MiddleTruncate: children must be a string`),null;if(e.length===0)return X(`div`,{className:a,style:o});if(e.length<i)return X(n===`end`?ya:va,{...s,className:a,style:o,children:e});let t=null,u=null,d=null;if(typeof r==`string`)r===`center`?t=la:r===`extension`?t=ua:r===`leaf-path`&&(t=da);else if(typeof r==`number`)t=fa,u=r;else if(Array.isArray(r)){let[e,n]=r;d=n,e===`last`?t=pa:e===`first`&&(t=ma)}else typeof r==`function`&&(t=r);t??=la;let[f,p]=t(e,{priority:n,variant:s.variant,splitIndex:typeof u==`number`?u:void 0,splitOffset:typeof d==`number`?d:void 0}),m=f.length>=p.length,h=n===`equal`&&!m,g=n===`equal`&&m,_={},v={};h&&(_.marker=``),g&&(v.marker=``),c=X(va,{...s,..._,children:f}),l=X(ya,{...s,...v,children:p})}return X(`div`,{"data-truncate-group-container":`middle`,className:a,style:o,children:[X(`div`,{"data-truncate-segment-priority":n===`start`||n===`equal`?`1`:`2`,children:c}),X(`div`,{"data-truncate-segment-priority":n===`end`||n===`equal`?`1`:`2`,children:l})]})}var xa={endIndex:-1,startIndex:-1};function Sa(e,t,n){return Math.min(Math.max(e,t),n)}function Ca(e,t){return e<0||t<e?xa:{endIndex:t,startIndex:e}}function wa(e){return e.startIndex<0||e.endIndex<e.startIndex}function Ta(e,t){return wa(e)?0:(e.endIndex-e.startIndex+1)*t}function Ea(e,t,n){if(t<=0)return-1;let r=t*n;return e<=0?0:e>=r?t:Math.floor(e/n)}function Da(e,t,n){return t<=0||e<=0?-1:e>=t*n?t-1:Math.ceil(e/n)-1}function Oa(e){let t=new Map;return e.forEach((e,n)=>{if(e.kind!==`directory`||!e.isExpanded)return;let r=e.ancestorPaths.length,i=t.get(r);if(i==null){t.set(r,[n]);return}i.push(n)}),t}function ka(e,t){let n=0,r=e.length-1,i=-1;for(;n<=r;){let a=Math.floor((n+r)/2),o=e[a];if(o==null)break;if(o<=t){i=a,n=a+1;continue}r=a-1}return i}function Aa(e){let t=new Map,n=[];for(let r=0;r<e.length;r+=1){let i=e[r];if(i==null)continue;let a=i.kind===`directory`&&i.isExpanded?[...i.ancestorPaths,i.path]:i.ancestorPaths,o=0;for(;o<n.length&&o<a.length&&n[o]===a[o];)o+=1;for(let e=n.length-1;e>=o;--e){let i=n[e];i!=null&&t.set(i,r-1)}n.length=o;for(let e=o;e<a.length;e+=1){let t=a[e];t!=null&&n.push(t)}}let r=e.length-1;for(let e of n)t.set(e,r);return t}function ja(e,t,n){if(e.length===0||t<=0)return[];let r=Aa(e),i=Oa(e),a=[];for(let r=0;r<e.length;r+=1){let o=i.get(r);if(o==null||o.length===0)break;let s=t+r*n,c=ka(o,Math.min(e.length-1,Math.floor(s/n))),l=null;for(;c>=0;){let t=o[c],n=t==null?null:e[t]??null;if(n!=null&&(r===0||n.ancestorPaths[r-1]===a[r-1]?.path)){l=n;break}--c}if(l==null)break;a.push(l)}return a.map((i,a)=>{let o=a*n,s=(r.get(i.path)??e.length-1)+1;if(s>=e.length)return{row:i,top:o};let c=s*n-t;return{row:i,top:Math.min(o,c-n)}}).filter(e=>e.top+n>0)}function Ma(e,t){let n=t.totalRowCount??e.length,r=n*t.itemHeight,i=Math.max(0,t.viewportHeight),a=Math.max(0,Math.floor(t.overscan)),o=Math.max(0,r-i),s=Sa(t.scrollTop,0,o),c=t.stickyRows??ja(e,s,t.itemHeight),l=c.reduce((e,n)=>Math.max(e,n.top+t.itemHeight),0),u=Math.min(r,s+l),d=Math.max(0,i-l),f=Math.max(0,r-u),p=Ea(s,n,t.itemHeight),m=Ea(u,n,t.itemHeight),h=l<=0||p<0||p>=n?-1:p,g=h===-1?-1:Math.min(n-1,m-1),_=h===-1||g<h?0:g-h+1,v=d<=0||m>=n?xa:Ca(m,Da(u+d,n,t.itemHeight)),y=g+1,b=wa(v)?xa:Ca(Math.max(y,v.startIndex-a),Math.min(n-1,v.endIndex+a)),x=Ta(b,t.itemHeight);return{occlusion:{firstOccludedIndex:h,lastOccludedIndex:g,occludedCount:_},physical:{itemHeight:t.itemHeight,maxScrollTop:o,overscan:a,scrollTop:s,totalHeight:r,totalRowCount:n,viewportHeight:i},projected:{contentHeight:f,paneHeight:d,paneTop:u},sticky:{height:l,rows:c},visible:v,window:{endIndex:b.endIndex,height:x,offsetTop:wa(b)?0:b.startIndex*t.itemHeight,startIndex:b.startIndex}}}var Na={added:`A`,deleted:`D`,ignored:null,modified:`M`,renamed:`R`,untracked:`U`},Pa={added:`Git status: added`,deleted:`Git status: deleted`,ignored:`Git status: ignored`,modified:`Git status: modified`,renamed:`Git status: renamed`,untracked:`Git status: untracked`},Fa=`Contains git status items`;function Ia(e){return e.current?!0:(e.current=!0,!1)}function La(e){let{currentScrollTop:t,focusedIndex:n,itemHeight:r,topInset:i=0,viewportHeight:a}=e;if(n<0)return null;let o=Math.max(0,i),s=n*r,c=s+r;if(s<t+o){let e=Math.max(0,s-o);return e===t?null:e}if(c>t+a){let e=c-a;return e===t?null:e}return null}function Ra(e){let{currentScrollTop:t,focusedIndex:n,itemHeight:r,offset:i,topInset:a=0,totalHeight:o,viewportHeight:s}=e;if(i===`nearest`)return La({currentScrollTop:t,focusedIndex:n,itemHeight:r,topInset:a,viewportHeight:s});if(n<0)return null;let c=Math.max(0,a),l=n*r,u=Math.max(0,s-c),d=i===`center`?c+Math.max(0,(u-r)/2):c,f=Math.max(0,o-s),p=Math.max(0,Math.min(l-d,f));return p===t?null:p}function za(e){let{currentScrollTop:t,focusedIndex:n,itemHeight:r,targetViewportOffset:i,totalHeight:a,viewportHeight:o}=e;if(n<0)return null;let s=Math.max(0,i),c=n*r,l=c+r,u=t+s,d=t+o;if(c>=u&&l<=d)return null;let f=Math.max(0,a-o),p=Math.max(0,Math.min(c-s,f));return p===t?null:p}function Ba(e){if(e==null||!e.isConnected||e===document.body||e===document.documentElement)return!1;e.focus({preventScroll:!0});let t=e.getRootNode();return t instanceof ShadowRoot?t.activeElement===e:document.activeElement===e}function Va(e){let t=e.getRootNode();if(t instanceof ShadowRoot){let e=t.activeElement;return e instanceof HTMLElement?e:null}let n=document.activeElement;return n instanceof HTMLElement&&e.contains(n)?n:null}function Ha(e,t){if(e==null)return t;let n=e.getBoundingClientRect().height;return n>0?n:e.clientHeight>0?e.clientHeight:t}function Ua(e,t){return e!=null&&e>0?e:t}function Wa(e){let t=e.borderBoxSize,n=Array.isArray(t)?t[0]:t;return n!=null&&Number.isFinite(n.blockSize)&&n.blockSize>0?n.blockSize:e.contentRect.height>0?e.contentRect.height:null}function Ga(e,t,n,r,i=0){let a=La({currentScrollTop:e.scrollTop,focusedIndex:t,itemHeight:n,topInset:i,viewportHeight:r});return a!=null&&(e.scrollTop=a,!0)}function Ka(e,t,n,r,i,a,o=0){let s=Ra({currentScrollTop:e.scrollTop,focusedIndex:t,itemHeight:n,offset:a,topInset:o,totalHeight:i,viewportHeight:r});return s!=null&&(e.scrollTop=s,!0)}function qa(e,t,n,r,i,a){let o=za({currentScrollTop:e.scrollTop,focusedIndex:t,itemHeight:n,targetViewportOffset:a,totalHeight:i,viewportHeight:r});return o!=null&&(e.scrollTop=o,!0)}function Ja(e,t,n,r){return n.end<n.start?null:e<n.start?-t:e>n.end?r:null}function Ya(e){let{renamingPath:t,previousRenamingPath:n,hasRenderedInput:r}=e;return t==null?`reset`:r?n===t?`ignore`:`focus-input`:`reveal-canonical`}function Xa({ariaLabel:e,isFlattened:t=!1,ref:n,value:r,onBlur:i,onInput:a}){return X(`input`,{ref:n,"data-item-rename-input":!0,...t?{"data-item-flattened-rename-input":!0}:{},"aria-label":e,value:r,onBlur:i,onInput:a,onClick:e=>e.stopPropagation(),onMouseDown:e=>e.stopPropagation(),onPointerDown:e=>e.stopPropagation()})}function Za(e){let{row:t,mode:n,targetPath:r,ariaLabel:i,domId:a,isParked:o,itemHeight:s,features:c,state:l,extraStyle:u}=e,d=n===`sticky`,f=t.ancestorPaths.at(-1)??``,p={};return l.isFocusRinged&&(p[`data-item-focused`]=!0),t.isSelected&&(p[`data-item-selected`]=!0),l.isContextHovered&&(p[`data-item-context-hover`]=`true`),l.isDragTarget&&(p[`data-item-drag-target`]=!0),l.isDragging&&(p[`data-item-dragging`]=!0),l.effectiveGitStatus!=null&&(p[`data-item-git-status`]=l.effectiveGitStatus),l.containsGitChange&&(p[`data-item-contains-git-change`]=`true`),{"aria-expanded":!d&&t.kind===`directory`?t.isExpanded:void 0,"aria-haspopup":c.contextMenuEnabled?`menu`:void 0,"aria-label":i,"aria-level":d?void 0:t.level+1,"aria-posinset":d?void 0:t.posInSet+1,"aria-selected":d?void 0:t.isSelected?`true`:`false`,"aria-setsize":d?void 0:t.setSize,"data-file-tree-sticky-path":d?r:void 0,"data-file-tree-sticky-row":d?`true`:void 0,"data-item-context-menu-button-visibility":c.actionLaneEnabled?c.contextMenuButtonVisibility:void 0,"data-item-context-menu-trigger-mode":c.contextMenuEnabled?c.contextMenuTriggerMode:void 0,"data-item-has-context-menu-action-lane":c.actionLaneEnabled?`true`:void 0,"data-item-has-git-lane":c.gitLaneActive?`true`:void 0,"data-item-parent-path":f.length>0?f:void 0,"data-item-parked":o?`true`:void 0,"data-item-path":r,"data-item-type":t.kind===`directory`?`folder`:`file`,"data-type":`item`,id:d?void 0:a,role:d?void 0:`treeitem`,style:{minHeight:`${s}px`,...u},tabIndex:!d&&t.isFocused?0:-1,...p}}function Qa(e){let{event:t,mode:n,isSearchOpen:r,isDirectory:i}=e,a=t.ctrlKey||t.metaKey,o=t.shiftKey||a,s=t.shiftKey?{additive:a,kind:`range`}:a?{kind:`toggle`}:{kind:`single`};return{closeSearch:r,revealCanonical:n===`sticky`,selection:s,toggleDirectory:!o&&i}}var $a,Z,eo,to,no=Object.is,ro=0,io=[],Q=Y,ao=Q.__b,oo=Q.__r,so=Q.diffed,co=Q.__c,lo=Q.unmount,uo=Q.__;function fo(e,t){Q.__h&&Q.__h(Z,e,ro||t),ro=0;var n=Z.__H||(Z.__H={__:[],__h:[]});return e>=n.__.length&&n.__.push({}),n.__[e]}function po(e){return ro=1,mo(To,e)}function mo(e,t,n){var r=fo($a++,2);if(r.t=e,!r.__c&&(r.__=[n?n(t):To(void 0,t),function(e){var t=r.__N?r.__N[0]:r.__[0],n=r.t(t,e);no(t,n)||(r.__N=[n,r.__[1]],r.__c.setState({}))}],r.__c=Z,!Z.__f)){var i=function(e,t,n){if(!r.__c.__H)return!0;var i=r.__c.__H.__.filter(function(e){return!!e.__c});if(i.every(function(e){return!e.__N}))return!a||a.call(this,e,t,n);var o=r.__c.props!==e;return i.forEach(function(e){if(e.__N){var t=e.__[0];e.__=e.__N,e.__N=void 0,no(t,e.__[0])||(o=!0)}}),a&&a.call(this,e,t,n)||o};Z.__f=!0;var a=Z.shouldComponentUpdate,o=Z.componentWillUpdate;Z.componentWillUpdate=function(e,t,n){if(4&this.__g){var r=a;a=void 0,i(e,t,n),a=r}o&&o.call(this,e,t,n)},Z.shouldComponentUpdate=i}return r.__N||r.__}function ho(e,t){var n=fo($a++,3);!Q.__s&&wo(n.__H,t)&&(n.__=e,n.u=t,Z.__H.__h.push(n))}function go(e,t){var n=fo($a++,4);!Q.__s&&wo(n.__H,t)&&(n.__=e,n.u=t,Z.__h.push(n))}function $(e){return ro=5,_o(function(){return{current:e}},[])}function _o(e,t){var n=fo($a++,7);return wo(n.__H,t)&&(n.__=e(),n.__H=t,n.__h=e),n.__}function vo(e,t){return ro=8,_o(function(){return e},t)}function yo(){for(var e;e=io.shift();)if(e.__P&&e.__H)try{e.__H.__h.forEach(So),e.__H.__h.forEach(Co),e.__H.__h=[]}catch(t){e.__H.__h=[],Q.__e(t,e.__v)}}Q.__b=function(e){Z=null,ao&&ao(e)},Q.__=function(e,t){e&&t.__k&&t.__k.__m&&(e.__m=t.__k.__m),uo&&uo(e,t)},Q.__r=function(e){oo&&oo(e),$a=0;var t=(Z=e.__c).__H;t&&(eo===Z?(t.__h=[],Z.__h=[],t.__.forEach(function(e){e.__N&&(e.__=e.__N),e.u=e.__N=void 0})):(t.__h.forEach(So),t.__h.forEach(Co),t.__h=[],$a=0)),eo=Z},Q.diffed=function(e){so&&so(e);var t=e.__c;t&&t.__H&&(t.__H.__h.length&&(io.push(t)!==1&&to===Q.requestAnimationFrame||((to=Q.requestAnimationFrame)||xo)(yo)),t.__H.__.forEach(function(e){e.u&&(e.__H=e.u),e.u=void 0})),eo=Z=null},Q.__c=function(e,t){t.some(function(e){try{e.__h.forEach(So),e.__h=e.__h.filter(function(e){return!e.__||Co(e)})}catch(n){t.some(function(e){e.__h&&=[]}),t=[],Q.__e(n,e.__v)}}),co&&co(e,t)},Q.unmount=function(e){lo&&lo(e);var t,n=e.__c;n&&n.__H&&(n.__H.__.forEach(function(e){try{So(e)}catch(e){t=e}}),n.__H=void 0,t&&Q.__e(t,n.__v))};var bo=typeof requestAnimationFrame==`function`;function xo(e){var t,n=function(){clearTimeout(r),bo&&cancelAnimationFrame(t),setTimeout(e)},r=setTimeout(n,35);bo&&(t=requestAnimationFrame(n))}function So(e){var t=Z,n=e.__c;typeof n==`function`&&(e.__c=void 0,n()),Z=t}function Co(e){var t=Z;e.__c=e.__(),Z=t}function wo(e,t){return!e||e.length!==t.length||t.some(function(t,n){return!no(t,e[n])})}function To(e,t){return typeof t==`function`?t(e):t}function Eo(e,t=null,n=null){"use no memo";let r=e.flattenedSegments;return r==null||r.length===0?t??e.name:X(`span`,{"data-item-flattened-subitems":!0,children:r.map((e,i)=>{let a=i===r.length-1;return X(Mi,{children:[X(`span`,{"data-item-flattened-subitem":e.path,"data-item-flattened-subitem-drag-target":n===e.path?`true`:void 0,children:a&&t!=null?t:X(va,{children:e.name})}),i<r.length-1?` / `:``]},e.path)})})}function Do(e){return e.isFlattened?e.flattenedSegments?.findLast(e=>e.isTerminal)?.path??e.path:e.path}function Oo(e){let t=e.flattenedSegments;return t==null||t.length===0?e.name:t.map(e=>e.name).join(` / `)}function ko(e,t,n,r){return e.map((e,i)=>{let a=i*n,o=e.subtreeEndIndex+1;if(o>=r)return{row:e.row,top:a};let s=o*n-t;return{row:e.row,top:Math.min(a,s-n)}}).filter(e=>e.top+n>0)}function Ao({controller:e,itemHeight:t,overscan:n,scrollTop:r,stickyFolders:i,viewportHeight:a}){let o=e.getVisibleCount(),s=i&&o>0?e.getStickyRowCandidates(r,t):[],c=s==null&&i&&o>0?e.getVisibleRows(0,o-1):[],l=Ma(c,{itemHeight:t,overscan:n,scrollTop:r,stickyRows:s==null?void 0:ko(s,r,t,o),totalRowCount:o,viewportHeight:a}),u=i&&r<=0&&o>0?e.getStickyRowCandidates(1,t):[],d=u!=null&&r<=0?ko(u,1,t,o):i&&r<=0&&c.length>0?ja(c,1,t):l.sticky.rows;return{overlayHeight:d.reduce((e,n)=>Math.max(e,n.top+t),0),overlayRows:d,snapshot:l,visibleRows:c}}var jo=400,Mo=40,No=18;function Po(e,t,n){let r=e,i=document.elementFromPoint?.bind(document)??null,a=r.elementFromPoint?.(t,n)??i?.(t,n)??null;return e instanceof ShadowRoot&&(a==null||!e.contains(a))?Fo(e,t,n):a instanceof HTMLElement?a:null}function Fo(e,t,n){let r=Array.from(e.querySelectorAll(`[data-type="item"], [data-item-flattened-subitem]`));for(let e=r.length-1;e>=0;e--){let i=r[e],a=i.getBoundingClientRect();if(t>=a.left&&t<=a.right&&n>=a.top&&n<=a.bottom)return i}return null}function Io(e){let t=e?.closest?.(`[data-type="item"]`);if(!(t instanceof HTMLElement))return null;let n=t.dataset.itemPath??null;if(n==null)return null;let r=e?.closest?.(`[data-item-flattened-subitem]`),i=r instanceof HTMLElement?r.getAttribute(`data-item-flattened-subitem`)??null:null;if(i!=null&&i.endsWith(`/`))return{directoryPath:i,flattenedSegmentPath:i,hoveredPath:n,kind:`directory`};if(t.dataset.itemType===`folder`)return{directoryPath:n,flattenedSegmentPath:null,hoveredPath:n,kind:`directory`};let a=t.dataset.itemParentPath??null;return a==null||a.length===0?{directoryPath:null,flattenedSegmentPath:null,hoveredPath:n,kind:`root`}:{directoryPath:a,flattenedSegmentPath:null,hoveredPath:n,kind:`directory`}}function Lo(e){let t=e.cloneNode(!0);return t.removeAttribute(`id`),t.dataset.fileTreeDragPreview=`true`,t.setAttribute(`aria-hidden`,`true`),t.tabIndex=-1,Object.assign(t.style,{boxShadow:`0 4px 12px rgba(0, 0, 0, 0.15)`,left:`0px`,margin:`0`,pointerEvents:`none`,position:`fixed`,top:`0px`,willChange:`transform`,zIndex:`10000`}),t}function Ro(){return navigator.vendor!==`Apple Computer, Inc.`}function zo(e,t){let n=e-t.top;if(n<Mo)return-Math.ceil((Mo-Math.max(0,n))/Mo*No);let r=t.bottom-e;return r<Mo?Math.ceil((Mo-Math.max(0,r))/Mo*No):0}function Bo(e,t){if(e!=null){let t=Na[e];return t==null?null:{text:t,title:Pa[e]}}return t?{icon:{name:`file-tree-icon-dot`,width:6,height:6},title:Fa}:null}function Vo(e,t,n){if(t==null||t.size===0)return null;let r=[];for(let i=e.length-1;i>=0;--i){let a=e[i],o=n.get(a);if(o!=null){for(let e of r)n.set(e,o);return o?`ignored`:null}if(t.has(a)){n.set(a,!0);for(let e of r)n.set(e,!0);return`ignored`}r.push(a)}for(let e of r)n.set(e,!1);return null}function Ho(e){return e!=null&&`toggle`in e}function Uo(e){return e.code===`Space`||e.key===` `||e.key===`Spacebar`}function Wo(e){return e.key.length===1&&/^[\p{L}\p{N}]$/u.test(e.key)&&!e.ctrlKey&&!e.metaKey&&!e.altKey}function Go(e){return e==null?``:`[data-item-section="spacing-item"][data-ancestor-path="${e.replaceAll(`\\`,`\\\\`).replaceAll(`"`,`\\"`)}"] { opacity: 1; }`}function Ko(e){return e.shiftKey&&e.key===`F10`||e.key===`ContextMenu`}function qo(e,t){return t&&Ko(e)||(e.ctrlKey||e.metaKey)&&Uo(e)?!0:e.key===`ArrowDown`||e.key===`ArrowLeft`||e.key===`ArrowRight`||e.key===`ArrowUp`}var Jo=new Set([`ArrowDown`,`ArrowLeft`,`ArrowRight`,`ArrowUp`,`End`,`Home`,`PageDown`,`PageUp`]);function Yo(e){for(let t of e.composedPath())if(t instanceof HTMLElement&&(t.dataset.fileTreeContextMenuRoot===`true`||t.dataset.type===`context-menu-anchor`||t.dataset.type===`context-menu-trigger`||t.getAttribute(`slot`)===`context-menu`))return!0;return!1}function Xo(e){return{bottom:e.bottom,height:e.height,left:e.left,right:e.right,top:e.top,width:e.width,x:e.x,y:e.y}}function Zo(e,t){return{bottom:t,height:0,left:e,right:e,top:t,width:0,x:e,y:t}}function Qo(e,t){if(e==null)return t.offsetTop;let n=t.getBoundingClientRect(),r=e.getBoundingClientRect();return n.top-r.top}function $o(e,t,n){if(n==null){e.delete(t);return}e.set(t,n)}function es(e,t,n){if(e==null)return null;let r=t.get(e)??null;if(r!=null)return r;let i=n.get(e)??null;return i?.dataset.itemParked===`true`?null:i}function ts(e){if(e==null)return[];let t=[];for(let n of e.querySelectorAll(`button[data-file-tree-sticky-row="true"]`)){if(!(n instanceof HTMLElement))continue;let e=n.dataset.fileTreeStickyPath;e!=null&&t.push(e)}return t}function ns(e,t){if(e==null||t==null)return null;for(let n of e.querySelectorAll(`button[data-item-focused="true"][data-item-parked="true"]`))if(n instanceof HTMLElement&&n.dataset.itemPath===t)return n;return null}function rs(e,t,n,r,i,a,o){let s=Math.max(0,a-i),c=t?.getBoundingClientRect()??null,l=c==null||n==null?null:n.getBoundingClientRect().top-c.top,u=ns(e,r),d=c==null||u==null?null:u.getBoundingClientRect().top-c.top;return Math.max(0,Math.min(d??Math.max(l??0,s),Math.max(0,o-i)))}function is(e,t){return{kind:e.kind,name:Oo(e),path:t}}function as(e){return e==null?void 0:`${e}__tree`}function os(e,t,n){if(e!=null)return`${e}__focused-item-${encodeURIComponent(t)}${n?`__parked`:``}`}function ss(e){return e===`file-tree-icon-chevron`||e===`file-tree-icon-dot`||e===`file-tree-icon-file`||e===`file-tree-icon-lock`}function cs(e,t){if(e==null)return null;if(`text`in e)return e.parts==null?X(`span`,{title:e.title,children:e.text}):X(`span`,{title:e.title,children:e.parts.map((e,t)=>X(`span`,{style:{color:e.color},children:e.text},t))});let n=typeof e.icon==`string`?ss(e.icon)?t(e.icon):{name:e.icon}:ss(e.icon.name)?(()=>{let n=t(e.icon.name),{name:r,...i}=e.icon;return{...n,...i}})():e.icon;return X(`span`,{title:e.title,children:X(aa,{...n})})}function ls(e){e!=null&&Ba(e.querySelector([`button:not([disabled])`,`[href]`,`input:not([disabled])`,`select:not([disabled])`,`textarea:not([disabled])`,`[tabindex]:not([tabindex="-1"])`].join(`, `))??e)}function us(e,t,{actionLaneEnabled:n=!1,customDecoration:r=null,decorationLaneEnabled:i=!1,dragTargetFlattenedSegmentPath:a=null,gitDecoration:o=null,gitLaneActive:s=!1,middleTruncate:c=!0,renameInput:l=null,showDecorativeActionAffordance:u=!1}={}){let d=Do(e);return X(Mi,{children:[e.depth>0?X(`div`,{"data-item-section":`spacing`,children:Array.from({length:e.depth}).map((t,n)=>X(`div`,{"data-item-section":`spacing-item`,"data-ancestor-path":e.ancestorPaths[n]},n))}):null,X(`div`,{"data-item-section":`icon`,children:e.kind===`directory`?X(aa,{...t(`file-tree-icon-chevron`)}):X(aa,{...t(`file-tree-icon-file`,d)})}),X(`div`,{"data-item-section":`content`,title:c?void 0:e.name,children:e.isFlattened?Eo(e,l,a):l??(c?X(ba,{minimumLength:5,split:`extension`,children:e.name}):e.name)}),i?X(`div`,{"data-item-section":`decoration`,children:r==null?null:cs(r,t)}):null,s?X(`div`,{"data-item-section":`git`,children:cs(o,t)}):null,n?X(`div`,{"data-item-section":`action`,children:u?X(`span`,{"aria-hidden":`true`,"data-item-action-affordance":`decorative`,children:X(aa,{...t(`file-tree-icon-ellipsis`)})}):null}):null]})}function ds(e,t,n,r={}){let{controller:i,renameView:a,visualFocusPath:o,contextHoverPath:s,draggedPathSet:c,dragTarget:l,dragAndDropEnabled:u,shouldSuppressContextMenu:d,handleRowDragStart:f,handleRowDragEnd:p,handleRowTouchStart:m,instanceId:h,itemHeight:g,middleTruncate:_,gitStatusByPath:v,ignoredGitDirectories:y,ignoredInheritanceCache:b,directoriesWithGitChanges:x,gitLaneActive:S,contextMenuEnabled:C,contextMenuTriggerMode:w,contextMenuButtonTriggerEnabled:ee,contextMenuButtonVisibility:te,contextMenuRightClickEnabled:T,registerRenameInput:E,registerButton:D,resolveIcon:O,renderDecorationForRow:ne,openContextMenuForRow:k,onRowClick:re,onKeyDown:ie}=e,A=Do(t),{isParked:ae=!1,mode:j=`flow`,style:oe}=r,se=j===`sticky`,ce=v?.get(A)??null??Vo(t.ancestorPaths,y,b),le=t.kind===`directory`&&(x?.has(A)??!1),ue=ne(t,A),M=Bo(ce,le),N=C&&ee,de=ue!=null||S||N,P=N&&te===`always`,fe=a.getPath()===A,F=fe?a.getValue():``,pe=se||!fe?null:X(Xa,{ref:E,ariaLabel:`Rename ${Oo(t)}`,isFlattened:t.isFlattened,value:F,onBlur:()=>{a.commit()},onInput:e=>{a.setValue(e.currentTarget.value)}}),me=us(t,O,{actionLaneEnabled:N,customDecoration:ue,decorationLaneEnabled:de,dragTargetFlattenedSegmentPath:l?.flattenedSegmentPath??null,gitDecoration:M,gitLaneActive:S,middleTruncate:_,renameInput:pe,showDecorativeActionAffordance:P}),he={...Za({ariaLabel:Oo(t),domId:t.isFocused?os(h,A,ae):void 0,extraStyle:oe,features:{actionLaneEnabled:N,contextMenuButtonVisibility:N?te:null,contextMenuEnabled:C,contextMenuTriggerMode:C?w:null,gitLaneActive:S},isParked:ae,itemHeight:g,mode:j,row:t,state:{containsGitChange:le,effectiveGitStatus:ce,isContextHovered:s===A,isDragTarget:l?.kind===`directory`&&l.directoryPath===A,isDragging:c?.has(A)===!0,isFocusRinged:t.isFocused&&o===A},targetPath:A}),key:n,onContextMenu:C||u?e=>{if(d()){e.preventDefault();return}C&&(e.preventDefault(),T&&(i.focusMountedPathFromInput(A),k(t,A,{anchorRect:Zo(e.clientX,e.clientY),source:`right-click`})))}:void 0,onFocus:se?void 0:()=>{i.focusMountedPathFromInput(A)},onKeyDown:se?void 0:ie,ref:e=>{D(A,e)}};return!se&&fe?X(`div`,{...he,children:me}):X(`button`,{...he,type:`button`,draggable:u&&!ae,onDragEnd:u&&!ae?p:void 0,onDragStart:u&&!ae?e=>{f(e,t,A)}:void 0,onMouseDown:e=>{if(se){e.preventDefault();return}i.isSearchOpen()&&e.preventDefault()},onTouchStart:u&&!ae?e=>{m(e,t,A)}:void 0,onClick:e=>{re(e,t,A,j)},children:me})}function fs(e,t,n){return t.end<t.start?[]:e.controller.getVisibleRows(t.start,t.end).filter(e=>!n.has(Do(e))).map((n,r)=>ds(e,n,t.start+r))}function ps({composition:e,controller:t,gitStatusByPath:n,ignoredGitDirectories:i,directoriesWithGitChanges:a,icons:o,instanceId:s,itemHeight:c=hr,middleTruncate:l=!0,overscan:p=10,renamingEnabled:m=!1,renderRowDecoration:h,searchBlurBehavior:g=`close`,searchEnabled:_=!1,searchFakeFocus:v=!1,slotHost:y,stickyFolders:b=!1,initialViewportHeight:x=420}){"use no memo";let S=$(null),C=$(null),w=$(!1),ee=$(null),te=$(null),T=$(null),E=$(null),D=$(null),O=$(new Map),ne=$(new Map),k=$(()=>{}),re=$(null),ie=$(0),A=$(!1),ae=$(null);ae.current!==t&&(A.current=!1,ae.current=t);let j=$(!1),oe=$(null),se=$(null),ce=$(!1),le=$(null),ue=$(null),M=$(null),N=$(null),de=$(null),P=$(null),fe=$(null),F=$(null),pe=$(!1),me=$(null),he=$(null),ge=$(null),_e=$(null),I=_o(()=>new Map,[]),[,ve]=po(0),ye=$(!1),[be,xe]=po(null),[Se,Ce]=po(null),[we,Te]=po(null),[Ee,De]=po(null),[Oe,ke]=po(0),[L,Ae]=po(null),je=$(L);je.current=L;let Me=$(null),Ne=$(null),Pe=$(null),Fe=$(null),Ie=$(null),Le=$(!1),Re=()=>{Ne.current=null,Pe.current=null,Fe.current=null},ze=(e,t)=>{Ne.current=e,Pe.current=null,Fe.current=t==null?null:{path:e,scrollTop:t}},Be=(e,t)=>{Ne.current=null,Pe.current={path:e,viewportOffset:t},Fe.current=null},Ve=$(g===`retain`&&t.isSearchOpen()),[He,Ue]=po(v);ho(()=>{v||Ue(!1)},[v]);let We=$(!1),Ge=vo(()=>{We.current=!0,Ue(e=>e&&!1)},[]),[Ke,qe]=po(()=>Ao({controller:t,itemHeight:c,overscan:p,scrollTop:0,stickyFolders:b,viewportHeight:x})),[Je,Ye]=po(!1);ho(()=>{Ye(!0)},[]);let Xe=e?.contextMenu?.enabled===!0||e?.contextMenu?.render!=null||e?.contextMenu?.onOpen!=null||e?.contextMenu?.onClose!=null,Ze=e?.contextMenu?.triggerMode??(Xe?`right-click`:`both`),Qe=Ze===`both`||Ze===`button`,$e=e?.contextMenu?.buttonVisibility??`when-needed`,et=Ze===`both`||Ze===`right-click`;go(()=>{let e=T.current;if(e==null)return;let t=e=>{if(!(e instanceof CustomEvent))return;let t=e.detail?.path??null;Ie.current=t,Ce(t),De(t==null?null:`pointer`)},n=e=>{e instanceof CustomEvent&&(Le.current=e.detail?.disabled===!0)};return e.addEventListener(`file-tree-debug-set-context-menu-trigger`,t),e.addEventListener(`file-tree-debug-set-scroll-suppression`,n),()=>{e.removeEventListener(`file-tree-debug-set-context-menu-trigger`,t),e.removeEventListener(`file-tree-debug-set-scroll-suppression`,n)}},[]);let tt=vo((e,t)=>{$o(O.current,e,t)},[]),nt=vo((e,t)=>{$o(ne.current,e,t)},[]),rt=vo(e=>{te.current=e},[]),R=vo(e=>es(e,ne.current,O.current),[]),{resolveIcon:it}=_o(()=>r(o),[o]),at=t[Yr](),ot=at.getPath(),st=ot!=null,ct=t.isSearchOpen(),lt=t.getSearchValue(),z=t.getFocusedPath(),B=t.getFocusedIndex(),ut=t.getScrollRequest(),dt=t.isDragAndDropEnabled(),ft=t.getDragSession(),pt=_o(()=>ft==null?null:new Set(ft.draggedPaths),[ft]),mt=ft?.target??null,ht=ft?.primaryPath??null,gt=as(s),{overlayHeight:_t,overlayRows:vt,snapshot:V,visibleRows:yt}=Ke,H=V.physical.viewportHeight,bt=_o(()=>({end:V.window.endIndex,start:V.window.startIndex}),[V.window.endIndex,V.window.startIndex]),U=vt,xt=V.sticky.rows,W=V.physical.totalHeight,St=V.sticky.height,Ct=_o(()=>new Set(xt.map(e=>Do(e.row))),[xt]),wt=B>=0&&B>=bt.start&&B<=bt.end,Tt=vo((e,t)=>h?.({item:is(e,t),row:e})??null,[h]),Et=vo(e=>Ba(e==null?null:O.current.get(e)??null)?!0:Ba(T.current),[]),Dt=vo(e=>{Et(t.focusNearestPath(e))},[t,Et]),Ot=$(Dt);Ot.current=Dt;let kt=$(!0),At=$(()=>{}),G=vo((t=!0)=>{let n=je.current;n!=null&&(kt.current=kt.current&&t,Ae(null),e?.contextMenu?.onClose?.(),kt.current&&Dt(n.path))},[e?.contextMenu,Dt]);At.current=G;let jt=vo(e=>{let t=e==null?null:Qo(T.current,e);Te(e=>e===t?e:t)},[]),Mt=vo((e,n,r)=>{let i=t.getItem(n);if(i==null)return;let a=R(n);if(a?.dataset.fileTreeStickyRow===`true`){let e=E.current;ze(n,e?.scrollTop??null),j.current=!0,xe(e=>e===n?e:n)}i.focus(),jt(a),kt.current=!0,Ae({anchorRect:r?.anchorRect??null,item:is(e,n),path:n,source:r?.source??`keyboard`})},[t,R,jt]),Nt=vo(e=>{if(m){if(t.isSearchOpen()){let e=E.current,t=Ha(e,H);le.current=B<0||e==null?null:Math.max(0,Math.min(B*c-e.scrollTop,Math.max(0,t-c))),ce.current=!0}t.startRenaming(e)!==!1&&(De(`focus`),ve(e=>e+1))}},[t,B,c,m,H]),Pt=vo((e,{restoreTreeFocus:n=!0,targetOffset:r=`live-overlay`}={})=>{let i=E.current;if(i==null)return!1;t.focusPath(e);let a=t.getFocusedIndex();if(a<0)return!1;let o=t.getVisibleRows(a,a)[0]??null;if(o==null)return!1;let s=Ha(i,H),l=t.getVisibleCount()*c,u=r===`sticky-parents`?o.ancestorPaths.length*c:Ao({controller:t,itemHeight:c,overscan:p,scrollTop:i.scrollTop,stickyFolders:b,viewportHeight:s}).snapshot.sticky.height;return j.current=!0,qa(i,a,c,s,l,u),k.current(),Me.current=n?e:null,!0},[t,c,p,H,b]),Ft=()=>w.current===!0||_e.current!=null||pe.current===!0,It=e=>typeof window.requestAnimationFrame==`function`?window.requestAnimationFrame(()=>{e()}):window.setTimeout(e,16),Lt=e=>{if(e!=null){if(typeof window.cancelAnimationFrame==`function`){window.cancelAnimationFrame(e);return}window.clearTimeout(e)}},Rt=()=>{N.current!=null&&(clearTimeout(N.current),N.current=null),M.current=null},K=()=>{P.current?.remove(),P.current=null},zt=()=>{Lt(ue.current),ue.current=null,de.current=null},Bt=e=>{let t=T.current?.getRootNode();if(t instanceof ShadowRoot){t.append(e);return}document.body.append(e)},Vt=()=>{F.current?.(),F.current=null,_e.current!=null&&(clearTimeout(_e.current),_e.current=null),pe.current=!1,me.current=null,ge.current=null,he.current!=null&&(he.current.setAttribute(`draggable`,`true`),he.current.style.removeProperty(`touch-action`),he.current=null),K(),Rt(),zt(),fe.current=null},Ht=(e,n)=>{let r=T.current?.getRootNode(),i=Io(Po(r instanceof ShadowRoot?r:document,e,n));return t.setDragTarget(i),t.getDragSession()?.target??null},Ut=e=>{let n=t.getDragAndDropConfig()?.openOnDropDelay??800;if(e==null||e.kind!==`directory`||e.directoryPath==null||n<=0){Rt();return}let r=t.getItem(e.directoryPath),i=Ho(r)?r:null;if(i==null||i.isExpanded()){Rt();return}let a=`${e.directoryPath}::${e.flattenedSegmentPath??``}`;M.current!==a&&(Rt(),M.current=a,N.current=setTimeout(()=>{let n=t.getDragSession()?.target;n?.kind===`directory`&&n.directoryPath===e.directoryPath&&n.flattenedSegmentPath===e.flattenedSegmentPath&&i.expand()},n))},q=()=>{ue.current=null;let e=de.current,n=E.current;if(e==null||n==null||t.getDragSession()==null)return;let r=n.getBoundingClientRect(),i=zo(e.clientY,r);if(i===0)return;let a=Math.max(0,n.scrollHeight-n.clientHeight),o=Math.max(0,Math.min(a,n.scrollTop+i));o!==n.scrollTop&&(n.scrollTop=o,k.current()),Ut(Ht(e.clientX,e.clientY)),ue.current=It(q)},J=(e,t)=>{de.current={clientX:e,clientY:t},ue.current??=It(q)},Wt=(e,n,r)=>{let i=e.currentTarget;if(i!=null){if(Vt(),K(),Rt(),zt(),t.startDrag(r)===!1){e.preventDefault();return}if(fe.current=n,e.dataTransfer!=null&&(e.dataTransfer.effectAllowed=`move`,e.dataTransfer.dropEffect=`move`,e.dataTransfer.setData(`text/plain`,r),Ro())){let t=Lo(i),n=i.getBoundingClientRect();Object.assign(t.style,{height:`${n.height}px`,opacity:`0.85`,transform:`translate3d(-9999px, 0px, 0)`,width:`${n.width}px`}),Bt(t),P.current=t,e.dataTransfer.setDragImage(t,Math.max(0,e.clientX-n.left),Math.max(0,e.clientY-n.top))}}},Gt=()=>{K(),Rt(),zt(),fe.current=null,t.cancelDrag()},Kt=(e,n,r)=>{if(_e.current!=null||pe.current)return;let i=e.touches[0],a=e.currentTarget;if(i==null||a==null)return;ge.current={clientX:i.clientX,clientY:i.clientY},he.current=a,a.setAttribute(`draggable`,`false`);let o=(e={})=>{let t=e.restoreNativeDraggable??!pe.current;_e.current!=null&&(clearTimeout(_e.current),_e.current=null),document.removeEventListener(`touchmove`,s),document.removeEventListener(`touchend`,c),document.removeEventListener(`touchcancel`,c),F.current===o&&(F.current=null),t&&(a.setAttribute(`draggable`,`true`),he.current===a&&(he.current=null),ge.current=null)},s=e=>{let t=e.touches[0],n=ge.current;if(t==null||n==null)return;let r=t.clientX-n.clientX,i=t.clientY-n.clientY;r*r+i*i<=100||o()},c=()=>{o()};document.addEventListener(`touchmove`,s,{passive:!0}),document.addEventListener(`touchend`,c),document.addEventListener(`touchcancel`,c),F.current=o,_e.current=setTimeout(()=>{if(o({restoreNativeDraggable:!1}),t.startDrag(r)===!1){a.setAttribute(`draggable`,`true`),he.current===a&&(he.current=null),ge.current=null;return}pe.current=!0,he.current=a,a.setAttribute(`draggable`,`false`),a.style.setProperty(`touch-action`,`none`),fe.current=n;let e=a.getBoundingClientRect(),s=Lo(a);Object.assign(s.style,{height:`${e.height}px`,opacity:`0.85`,transform:`translate3d(${e.left}px, ${e.top}px, 0)`,width:`${e.width}px`}),Bt(s),P.current=s,me.current={x:i.clientX-e.left,y:i.clientY-e.top};let c=e=>{let t=e.touches[0];if(t==null)return;e.preventDefault();let n=me.current;n!=null&&P.current!=null&&(P.current.style.transform=`translate3d(${t.clientX-n.x}px, ${t.clientY-n.y}px, 0)`),Ut(Ht(t.clientX,t.clientY)),J(t.clientX,t.clientY)},l=e=>{let n=e.changedTouches[0];n!=null&&Ht(n.clientX,n.clientY),t.completeDrag(),Vt()},u=()=>{t.cancelDrag(),Vt()};F.current=()=>{document.removeEventListener(`touchmove`,c),document.removeEventListener(`touchend`,l),document.removeEventListener(`touchcancel`,u)},document.addEventListener(`touchmove`,c,{passive:!1}),document.addEventListener(`touchend`,l),document.addEventListener(`touchcancel`,u)},jo)},qt=e=>{if(L!=null){if(e.key===`Escape`){G(),e.preventDefault(),e.stopPropagation();return}Jo.has(e.key)&&(e.preventDefault(),e.stopPropagation());return}if(at.isActive()){if(e.isComposing||e.keyCode===229)return;if(e.key===`Escape`)at.cancel();else if(e.key===`Enter`)at.commit();else return;De(`focus`),ve(e=>e+1),e.preventDefault(),e.stopPropagation();return}if(m&&e.key===`F2`){Nt(z??void 0),e.preventDefault(),e.stopPropagation();return}if(ct){if(e.key===`Escape`)ce.current=!1,le.current=null,t.closeSearch();else if(e.key===`Enter`){let e=t.getFocusedPath();e!=null&&t.selectOnlyPath(e);let n=E.current,r=Ha(n,H);le.current=B<0||n==null?null:Math.max(0,Math.min(B*c-n.scrollTop,Math.max(0,r-c))),ce.current=!0,t.closeSearch()}else if(e.key===`ArrowDown`)t.focusNextSearchMatch();else if(e.key===`ArrowUp`)t.focusPreviousSearchMatch();else return;De(`focus`),ve(e=>e+1),e.preventDefault(),e.stopPropagation();return}if(_&&Wo(e)){t.openSearch(e.key),ve(e=>e+1),e.preventDefault(),e.stopPropagation();return}let n=Xe&&Ko(e),r=qo(e,Xe),i=r&&T.current!=null?Va(T.current):null,a=r?new Set(ts(T.current)):new Set,o=i?.dataset.fileTreeStickyPath??null,s=i?.dataset.fileTreeStickyRow===`true`&&o!=null;if(s&&o!==z&&a.has(o)){let e=E.current;ze(o,e?.scrollTop??null),t.focusPath(o)}let l=t.getFocusedPath(),u=t.getFocusedIndex(),d=t.getFocusedItem();if(d==null)return;let f=Ho(d)?d:null,p=l!=null&&(Ct.has(l)||s&&o===l&&a.has(l)),h=e.key===`ArrowDown`||e.key===`ArrowUp`||e.key===`ArrowRight`&&f!=null&&f.isExpanded(),g=e.key===`ArrowLeft`&&p&&f!=null&&f.isExpanded(),v=E.current,y=!0;if(e.shiftKey&&e.key===`ArrowDown`)t.extendSelectionFromFocused(1);else if(e.shiftKey&&e.key===`ArrowUp`)t.extendSelectionFromFocused(-1);else if(n&&l!=null&&u>=0){let e=t.getVisibleRows(u,u)[0]??null,n=es(l,ne.current,O.current);e==null||n==null?y=!1:Mt(e,l)}else if((e.ctrlKey||e.metaKey)&&Uo(e))t.toggleFocusedSelection();else if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()===`a`)t.selectAllVisiblePaths();else switch(e.key){case`ArrowDown`:t.focusNextItem();break;case`ArrowUp`:t.focusPreviousItem();break;case`ArrowRight`:f==null||f.isExpanded()?t.focusNextItem():f.expand();break;case`ArrowLeft`:f!=null&&f.isExpanded()?f.collapse():t.focusParentItem();break;case`Home`:t.focusFirstItem();break;case`End`:t.focusLastItem();break;default:y=!1}if(!y)return;De(`focus`);let b=t.getFocusedPath(),x=b!=null&&(Ct.has(b)||a.has(b)),S=h&&b!==l,C=n&&s&&o===l&&b===l;if((p||C)&&b!=null&&(S&&x||C))ze(b,v?.scrollTop??null),j.current=!0,xe(e=>e===b?e:b);else{let t=e.key===`ArrowUp`&&p&&b!==l;b!=null&&(t||g&&b===l)?(Be(b,rs(T.current,v,i,l,c,St,H)),j.current=!0,xe(e=>e===b?e:b)):Re()}ve(e=>e+1),e.preventDefault(),e.stopPropagation()};go(()=>{if(!(!_||!ct)){if(Ve.current){Ve.current=!1;return}Ba(D.current)}},[ct,_]),go(()=>{let e=te.current;switch(Ya({hasRenderedInput:e!=null,previousRenamingPath:se.current,renamingPath:ot})){case`reset`:se.current=null;return;case`reveal-canonical`:ot!=null&&Pt(ot,{restoreTreeFocus:!1,targetOffset:`live-overlay`});return;case`ignore`:return;case`focus-input`:e!=null&&(Me.current=null,se.current=ot,Ba(e),e.select());return}},[bt.end,bt.start,ot,Pt,Ct]),go(()=>{let e=T.current;if(e==null)return;let t=null,n=()=>{t!=null&&(clearTimeout(t),t=null)},r=()=>{let t=Va(e)?.dataset.itemPath??null;xe(e=>e===t?e:t)},i=()=>{n(),j.current=!0,r()},a=i=>{let a=i.relatedTarget;if(a==null){n(),t=setTimeout(()=>{if(t=null,Va(e)!=null){r();return}j.current=!1,xe(null)},0);return}if(!(a instanceof Node)||!e.contains(a)){n(),j.current=!1,xe(null);return}let o=a instanceof HTMLElement?a.dataset.itemPath??null:null;xe(e=>e===o?e:o)};return e.addEventListener(`focusin`,i),e.addEventListener(`focusout`,a),()=>{n(),e.removeEventListener(`focusin`,i),e.removeEventListener(`focusout`,a)}},[]),go(()=>{let e=T.current;e!=null&&(V.physical.scrollTop<=0?e.dataset.scrollAtTop=`true`:delete e.dataset.scrollAtTop)},[V.physical.scrollTop]),go(()=>{let e=null,n=E.current,r=ee.current,i=T.current;if(n==null)return;re.current=Ha(n,x);let a=()=>{let e=t.getVisibleCount(),r=Ua(re.current,x),i=Math.max(0,e*c-r,n.scrollHeight-n.clientHeight);n.scrollTop>i&&(n.scrollTop=i),qe(Ao({controller:t,itemHeight:c,overscan:p,scrollTop:Math.min(n.scrollTop,i),stickyFolders:b,viewportHeight:r}))};if(!A.current){A.current=!0;let e=t.getFocusedIndex();if(e>=0){let r=Ua(re.current,x),i=t.getVisibleRows(e,e)[0]??null;Ga(n,e,c,r,b&&i!=null?Math.max(0,Math.min(i.ancestorPaths.length*c,Math.max(0,r-c))):0)}}k.current=a;let o=t.subscribe(()=>{Ia(ye)&&ve(e=>e+1),a()}),s=()=>{Le.current!==!0&&(r!=null&&(r.dataset.isScrolling??=``),i!=null&&(i.dataset.isScrolling??=``),w.current=!0,e!=null&&clearTimeout(e),e=setTimeout(()=>{r!=null&&delete r.dataset.isScrolling,i!=null&&delete i.dataset.isScrolling,w.current=!1,ke(e=>e+1),e=null},50))},l=null,u=()=>{i!=null&&delete i.dataset.overlayReveal,l!=null&&(clearTimeout(l),l=null)},d=()=>{i!=null&&Le.current!==!0&&(n.scrollTop>0||(i.dataset.overlayReveal=`true`,l!=null&&clearTimeout(l),l=setTimeout(()=>{u()},200)))},f=()=>{if(a(),n.scrollTop>0&&u(),je.current!=null&&w.current&&At.current(),Le.current===!0){w.current=!1;return}Ce(e=>e==null?e:null),s()},m=()=>{s(),d()},h=new Set([`ArrowUp`,`ArrowDown`,`ArrowLeft`,`ArrowRight`,`PageUp`,`PageDown`,`Home`,`End`,` `,`Spacebar`]),g=e=>{h.has(e.key)&&m()};n.addEventListener(`scroll`,f,{passive:!0}),n.addEventListener(`wheel`,m,{passive:!0}),n.addEventListener(`touchmove`,m,{passive:!0}),n.addEventListener(`keydown`,g);let _=typeof ResizeObserver<`u`?new ResizeObserver(e=>{re.current=(e[0]==null?null:Wa(e[0]))??Ha(n,x),a()}):null;return _?.observe(n),()=>{k.current=()=>{},o(),n.removeEventListener(`scroll`,f),n.removeEventListener(`wheel`,m),n.removeEventListener(`touchmove`,m),n.removeEventListener(`keydown`,g),e!=null&&clearTimeout(e),l!=null&&clearTimeout(l),r!=null&&delete r.dataset.isScrolling,i!=null&&(delete i.dataset.isScrolling,delete i.dataset.overlayReveal),w.current=!1,re.current=null,_?.disconnect()}},[t,x,c,p,b]),go(()=>{Xe||L==null||G(!1)},[G,Xe,L]);let Jt=_o(()=>L==null?null:`${L.path}::${L.source}`,[L]);go(()=>{if(Jt==null){y?.clearSlotContent(d);return}let t=je.current;if(t==null)return;let n=C.current??S.current;if(n==null)return;let r={anchorElement:n,anchorRect:t.anchorRect??Xo(n.getBoundingClientRect()),close:e=>{At.current(e?.restoreFocus??!0)},restoreFocus:()=>{kt.current&&Ot.current(je.current?.path??null)}},i=e?.contextMenu?.render?.(t.item,r)??null;return y?.setSlotContent(d,i),e?.contextMenu?.onOpen?.(t.item,r),ls(i),queueMicrotask(()=>{i==null||!i.isConnected||document.activeElement===i&&ls(i)}),()=>{y?.clearSlotContent(d)}},[Jt,e?.contextMenu,y]),go(()=>{L!=null&&t.getItem(L.path)==null&&G()},[G,L,t]),go(()=>{if(L==null)return;let e=T.current?.getRootNode(),t=e instanceof ShadowRoot?e.host:T.current,n=e=>{let n=e.target;n instanceof Node&&(Yo(e)||S.current?.contains(n)!==!0&&t?.contains(n)!==!0&&G())},r=e=>{e.key===`Escape`&&(e.preventDefault(),e.stopPropagation(),G())};return document.addEventListener(`mousedown`,n,!0),document.addEventListener(`keydown`,r,!0),()=>{document.removeEventListener(`mousedown`,n,!0),document.removeEventListener(`keydown`,r,!0)}},[G,L]),go(()=>{let e=E.current,n=T.current;if(e==null||n==null){oe.current=z;return}let r=z==null?null:O.current.get(z)??null,i=Va(n),a=i?.dataset.itemPath??null,o=st&&te.current===i,s=_&&D.current===i,l=ce.current&&!ct,u=le.current??0,d=Me.current,f=Ne.current,p=Pe.current,m=Fe.current,h=i!=null,g=j.current||h,v=oe.current!==z,y=f!=null&&f===z&&z!=null,x=!1,S=!1;if(ut!=null&&ut.id!==ie.current){ie.current=ut.id;let n=ut.visibleIndex,r=t.getVisibleRows(n,n)[0]??null;if(r!=null){let t=b?Math.max(0,Math.min(r.ancestorPaths.length*c,Math.max(0,H-c))):St;x=!0,S=Ka(e,n,c,H,W,ut.offset,t)}t.clearScrollRequest(ut.id)}let C=!x&&l&&qa(e,B,c,H,W,u),w=!x&&d!=null&&d===z&&qa(e,B,c,H,W,St),ee=!x&&p!=null&&p.path===z&&qa(e,B,c,H,W,p.viewportOffset),ne=!x&&m!=null&&m.path===z&&e.scrollTop!==m.scrollTop;if(ne&&(e.scrollTop=m.scrollTop),(ne||S||w||ee||C||g&&v&&d!==z&&!y&&Ga(e,B,c,H,St))&&k.current(),x){oe.current=z;return}if(!g){oe.current=z;return}if(o){oe.current=z;return}if(s&&!l){oe.current=z;return}if(r==null){l&&B>=0&&(qa(e,B,c,H,W,u),k.current()),oe.current=z;return}(v||l||d===z||f===z||p?.path===z||m?.path===z||a==null||a!==z)&&(Ba(r),d===z&&(Me.current=null),f===z&&(Ne.current=null),p?.path===z&&(Pe.current=null),m?.path===z&&(Fe.current=null),ce.current=!1,le.current=null),oe.current=z},[t,B,z,wt,c,st,ct,bt,H,_,ut,b,St,W,yt]);let Yt=B>=0&&B>=V.visible.startIndex&&B<=V.visible.endIndex,Xt=z!=null&&U.some(e=>Do(e.row)===z),Zt=Yt||Xt,Qt=Qe&&j.current===!0&&Zt?z:null,$t=Ee===`pointer`?Se:null,en=L?.path??Ie.current??$t??Qt??Se,tn=L?.source===`right-click`;go(()=>{w.current&&L==null||jt(R(en))},[L,R,bt,H,Oe,U,en,jt,yt]);let nn=vo(e=>{if(w.current||Yo(e))return;let t=e.target;if(!(t instanceof HTMLElement)||t.closest?.(`[data-type="context-menu-trigger"]`)!=null)return;let n=t.closest?.(`[data-file-tree-sticky-row="true"]`),r=t.closest?.(`[data-type="item"]`),i=n instanceof HTMLElement?n.dataset.fileTreeStickyPath??null:r instanceof HTMLElement?r.dataset.itemPath??null:null;i!=null&&De(e=>e===`pointer`?e:`pointer`),Ce(e=>e===i?e:i)},[]),rn=vo(()=>{Ce(null)},[]);go(()=>{if(!dt)return;let e=()=>{Vt(),t.cancelDrag()};return window.addEventListener(`dragend`,e),()=>{window.removeEventListener(`dragend`,e),Vt(),t.cancelDrag()}},[t,dt]);let an=e=>{if(!dt||t.getDragSession()==null||pe.current)return;let n=Io(e.target instanceof HTMLElement?e.target:null);t.setDragTarget(n),Ut(t.getDragSession()?.target??null),J(e.clientX,e.clientY),e.dataTransfer!=null&&(e.dataTransfer.dropEffect=`move`),e.preventDefault()},on=e=>{if(!dt||t.getDragSession()==null||pe.current)return;let n=e.relatedTarget;n instanceof Node&&T.current?.contains(n)===!0||(Rt(),zt(),t.setDragTarget(null))},sn=e=>{!dt||t.getDragSession()==null||pe.current||(e.preventDefault(),Ht(e.clientX,e.clientY),t.completeDrag(),K(),Rt(),zt(),fe.current=null)},cn=V.window.height,ln=V.window.offsetTop,un=Math.min(0,H-cn),dn=Math.min(0,H-cn-St),fn=be===z||ce.current,pn=z!=null&&fn&&!wt&&B>=0?yt[B]??t.getVisibleRows(B,B)[0]??null:null,mn=pn==null?null:Ja(B,c,bt,cn),hn=fe.current,gn=ht!=null&&hn!=null&&hn.path===ht&&hn.index>=bt.start&&hn.index<=bt.end,_n=ht!=null&&hn!=null&&hn.path===ht&&!gn&&hn.path!==pn?.path?hn:null,vn=_n==null?null:Ja(_n.index,c,bt,cn),yn=Go((B>=0?yt[B]??t.getVisibleRows(B,B)[0]??null:null)?.ancestorPaths.at(-1)??null),bn=ct&&z!=null?os(s,z,!wt):void 0,xn=L?.path??(ct?z:be),Sn=L?.path??Se,Cn=R(en),wn=Xe&&Qe&&!tn&&!st&&Cn!=null&&we!=null&&en!=null,Tn=Xe&&(wn||L!=null),En=L?.anchorRect,Dn=En==null&&Cn!=null&&we!=null&&(L!=null||wn)?we:null,On=En==null?Dn==null?void 0:{top:`${Dn}px`}:{left:`${En.left}px`,position:`fixed`,right:`auto`,top:`${En.top}px`},kn=tn?{opacity:`0`}:void 0,An=vo((e,n,r,i)=>{let a=Qa({event:{ctrlKey:e.ctrlKey,metaKey:e.metaKey,shiftKey:e.shiftKey},isDirectory:n.kind===`directory`,isSearchOpen:ct,mode:i}),o=a.toggleDirectory&&n.kind===`directory`,s=o?t.resolveMountedDirectoryPathFromInput(r):null;if(o&&s==null)return;let c=s??r;switch(a.selection.kind){case`range`:t.selectPathRange(c,a.selection.additive);break;case`toggle`:t.togglePathSelectionFromInput(c);break;case`single`:t.selectOnlyMountedPathFromInput(c)}let l=e.currentTarget instanceof HTMLElement?e.currentTarget:null,u=n.index>=V.visible.startIndex&&n.index<=V.visible.endIndex,d=i===`flow`&&u&&l!=null&&l.dataset.itemParked!==`true`;t.focusMountedPathFromInput(c),d&&(j.current=!0,xe(e=>e===c?e:c),De(`focus`)),o&&t.toggleMountedDirectoryFromInput(c),a.closeSearch&&t.closeSearch(),a.revealCanonical&&Pt(c,{targetOffset:`sticky-parents`})},[t,ct,V.visible.endIndex,V.visible.startIndex,Pt]),jn=()=>{if(w.current||!Qe||en==null||Cn==null)return;let e=t.getItem(en);e!=null&&(jt(Cn),kt.current=!0,Ae({anchorRect:null,item:{kind:e.isDirectory()?`directory`:`file`,name:Cn.getAttribute(`aria-label`)??en,path:e.getPath()},path:e.getPath(),source:`button`}))},Mn={contextHoverPath:Sn,contextMenuButtonTriggerEnabled:Qe,contextMenuButtonVisibility:$e,contextMenuEnabled:Xe,contextMenuRightClickEnabled:et,contextMenuTriggerMode:Ze,controller:t,directoriesWithGitChanges:a,dragAndDropEnabled:dt,draggedPathSet:pt,dragTarget:mt,gitLaneActive:!0,gitStatusByPath:n,handleRowDragEnd:Gt,handleRowDragStart:Wt,handleRowTouchStart:Kt,ignoredGitDirectories:i,ignoredInheritanceCache:I,instanceId:s,itemHeight:c,middleTruncate:l,onKeyDown:qt,onRowClick:An,openContextMenuForRow:Mt,registerButton:tt,registerRenameInput:rt,renameView:at,renderDecorationForRow:Tt,resolveIcon:it,shouldSuppressContextMenu:Ft,visualFocusPath:xn},Nn={...Mn,registerButton:nt};return X(`div`,{ref:T,id:gt,"data-file-tree-context-menu-button-visibility":Xe&&Qe?$e:void 0,"data-file-tree-context-menu-trigger-mode":Xe?Ze:void 0,"data-file-tree-has-context-menu-action-lane":Xe&&Qe?`true`:void 0,"data-file-tree-has-git-lane":`true`,"data-file-tree-virtualized-root":`true`,onDragLeave:dt?on:void 0,onDragOver:dt?an:void 0,onDrop:dt?sn:void 0,onKeyDown:qt,onPointerLeave:Xe?rn:void 0,onPointerOver:Xe?nn:void 0,role:`tree`,tabIndex:-1,style:{outline:`none`,position:`relative`},children:[X(`style`,{"data-file-tree-guide-style":`true`,dangerouslySetInnerHTML:{__html:yn}}),X(`slot`,{name:u,"data-type":`header-slot`}),_?X(`div`,{"data-file-tree-search-container":!0,"data-open":ct?`true`:`false`,children:X(`input`,{ref:D,"aria-activedescendant":bn,"aria-controls":gt,placeholder:`Search…`,"data-file-tree-search-input":!0,"data-file-tree-search-input-fake-focus":He?`true`:void 0,value:lt,onBlur:()=>{g===`retain`&&!We.current||t.closeSearch()},onFocus:Ge,onPointerDown:Ge,onInput:e=>{Ge();let n=e.currentTarget;t.setSearch(n.value)}})}):null,X(`div`,{ref:E,"data-file-tree-virtualized-scroll":`true`,children:[b&&Je&&U.length>0?X(`div`,{"aria-hidden":`true`,"data-file-tree-sticky-overlay":`true`,children:X(`div`,{"data-file-tree-sticky-overlay-content":`true`,style:{height:`${_t}px`},children:U.map((e,t)=>ds(Nn,e.row,`sticky:${Do(e.row)}`,{mode:`sticky`,style:{left:`0`,position:`absolute`,right:`0`,top:`${e.top}px`,zIndex:`${U.length-t}`}}))})}):null,X(`div`,{ref:ee,"data-file-tree-virtualized-list":`true`,style:{height:`${W}px`},children:[X(`div`,{"data-file-tree-virtualized-sticky-offset":`true`,"aria-hidden":`true`,style:{height:`${ln}px`}}),X(`div`,{"data-file-tree-virtualized-sticky":`true`,style:{height:`${cn}px`,top:`${un}px`,bottom:`${dn}px`},children:[fs(Mn,bt,Ct),pn!=null&&mn!=null?ds(Mn,pn,`parked:${pn.path}`,{isParked:!0,style:{left:`0`,opacity:`0`,pointerEvents:ht===pn.path?`none`:void 0,position:`absolute`,right:`0`,top:`${mn}px`}}):null,_n!=null&&vn!=null?ds(Mn,_n,`parked-drag:${_n.path}`,{isParked:!0,style:{left:`0`,opacity:`0`,pointerEvents:`none`,position:`absolute`,right:`0`,top:`${vn}px`}}):null]})]})]}),Xe?X(`div`,{ref:S,"data-type":`context-menu-anchor`,"data-visible":Tn?`true`:`false`,style:On,children:[X(`button`,{ref:C,type:`button`,"data-type":f,"aria-label":`Options`,"aria-haspopup":`menu`,"aria-expanded":L==null?`false`:`true`,"data-visible":wn?`true`:`false`,onMouseDown:e=>{e.preventDefault()},onClick:e=>{if(e.preventDefault(),e.stopPropagation(),L!=null){G();return}jn()},tabIndex:-1,style:kn,children:X(aa,{...it(`file-tree-icon-ellipsis`)})}),L==null?null:X(`slot`,{name:d})]}):null,L==null?null:X(`div`,{"data-type":`context-menu-wash`,"aria-hidden":`true`,onMouseDownCapture:e=>{e.preventDefault(),G()},onTouchStartCapture:e=>{e.preventDefault(),e.stopPropagation(),G()},onTouchMoveCapture:e=>{e.preventDefault(),e.stopPropagation()},onWheelCapture:e=>{e.preventDefault(),e.stopPropagation()}})]})}var ms={hydrateRoot:(e,t)=>{ea(Ai(ps,t),e)},renderRoot:(e,t)=>{$i(Ai(ps,t),e)},unmountRoot:e=>{$i(null,e)}};function hs(e,t){ms.renderRoot(e,t)}function gs(e,t){ms.hydrateRoot(e,t)}function _s(e){ms.unmountRoot(e)}var vs=class{#e=new Map;#t=null;clearAll(){for(let e of this.#e.values())e.remove();this.#e.clear()}clearSlotContent(e){let t=this.#n(e);t!=null&&(t.remove(),this.#e.delete(e))}setHost(e){if(this.#t=e,e!=null){this.#i(e);for(let[e,t]of this.#e)this.#r(e,t)}}setSlotContent(e,t){let n=this.#n(e);if(n===t){t!=null&&(this.#e.set(e,t),this.#r(e,t));return}if(n?.remove(),t==null){this.#e.delete(e);return}this.#e.set(e,t),this.#r(e,t)}setSlotHtml(e,t){let n=t?.trim()??``;if(n.length===0){this.setSlotContent(e,null);return}let r=this.#n(e);if(r!=null&&r.innerHTML===n){this.#e.set(e,r),this.#r(e,r);return}let i=document.createElement(`div`);i.innerHTML=n,this.setSlotContent(e,i)}#n(e){let t=this.#e.get(e)??null;if(t!=null)return t;let n=this.#t;if(n==null)return null;for(let t of Array.from(n.children))if(t instanceof HTMLElement&&t.dataset.fileTreeManagedSlot===e)return t;return null}#r(e,t){t.slot=e,t.dataset.fileTreeManagedSlot=e,this.#t!=null&&t.parentNode!==this.#t&&this.#t.appendChild(t)}#i(e){for(let t of Array.from(e.children)){if(!(t instanceof HTMLElement))continue;let e=t.dataset.fileTreeManagedSlot;e==null||this.#e.has(e)||this.#e.set(e,t)}}};Array.isArray;var ys=0;function bs(e){return e!=null&&e.length>0?e:(ys+=1,`pst_ft_${ys}`)}function xs({initialVisibleRowCount:e,itemHeight:t}){return e==null?420:Math.max(0,e)*(t??hr)}function Ss(e){if(typeof document>`u`)return;let t=document.createElement(`div`);t.innerHTML=e;let n=t.querySelector(`svg`);return n instanceof SVGElement?n:void 0}function Cs(e){return e.querySelector(`#file-tree-icon-chevron`)instanceof SVGElement&&e.querySelector(`#file-tree-icon-file`)instanceof SVGElement&&e.querySelector(`#file-tree-icon-dot`)instanceof SVGElement&&e.querySelector(`#file-tree-icon-lock`)instanceof SVGElement}function ws(e){return Array.from(e.children).filter(e=>e instanceof SVGElement)}var Ts=class{static LoadedCustomComponent=!0;#e;#t;#n;#r;#i;#a;#o;#s;#c;#l;#u=new vs;#d;#f;#p;#m;#h;#g;#_;#v;#y;#b=null;#x=null;#S;#C=!1;#w=!1;constructor(e){let{composition:t,density:n,fileTreeSearchMode:r,gitStatus:i,id:a,initialSearchQuery:o,icons:s,itemHeight:c,middleTruncate:l,onExpansionChange:u,onSearchChange:d,onSelectionChange:f,overscan:p,renderRowDecoration:m,renaming:h,search:g,searchBlurBehavior:_,searchFakeFocus:v,stickyFolders:y,unsafeCSS:b,initialVisibleRowCount:x,...S}=e;this.#e=t,this.#n=bs(a),this.#m=oi(i),this.#h=s,this.#g=b,this.#r=u,this.#i=f,this.#a=m,this.#o=h!=null&&h!==!1,this.#s=_,this.#c=g===!0,this.#l=v===!0,this.#d=mr(n,c),this.#f={itemHeight:this.#d.itemHeight,middleTruncate:l,overscan:p,stickyFolders:y,initialVisibleRowCount:x},this.#t=new ti({...S,fileTreeSearchMode:r,initialSearchQuery:o,onSearchChange:d,renaming:h}),this.#y=this.#t.getSelectionVersion(),this.#b=this.#i==null?null:this.subscribe(()=>{this.#k()}),this.#x=this.#r==null?null:this.#t.subscribe(e=>{(e?.operation===`expand`||e?.operation===`collapse`)&&this.#r?.({path:e.path,expanded:e.operation===`expand`})})}unmount(){this.#S!=null&&(_s(this.#S),delete this.#S.dataset.fileTreeVirtualizedWrapper,this.#S=void 0),this.#u.clearAll(),this.#u.setHost(null),this.#p!=null&&(delete this.#p.dataset.fileTreeVirtualized,this.#R(this.#p),this.#p=void 0)}cleanUp(){this.unmount(),this.#b?.(),this.#b=null,this.#x?.(),this.#x=null,this.#t.destroy()}getFileTreeContainer(){return this.#p}getItem(e){return this.#t.getItem(e)}getFocusedItem(){return this.#t.getFocusedItem()}getFocusedPath(){return this.#t.getFocusedPath()}getFocusedIndex(){return this.#t.getFocusedIndex()}getVisibleCount(){return this.#t.getVisibleCount()}getVisibleRows(e,t){return this.#t.getVisibleRows(e,t)}getSelectedPaths(){return this.#t.getSelectedPaths()}getComposition(){return this.#e}getItemHeight(){return this.#d.itemHeight}getDensityFactor(){return this.#d.factor}subscribe(e){let t=!1;return this.#t.subscribe(()=>{if(!t){t=!0;return}e()})}focusPath(e){this.#t.focusPath(e)}focusFirstItem(){this.#t.focusFirstItem()}focusLastItem(){this.#t.focusLastItem()}focusNextItem(){this.#t.focusNextItem()}focusPreviousItem(){this.#t.focusPreviousItem()}focusParentItem(){this.#t.focusParentItem()}scrollToPath(e,t){this.#t.scrollToPath(e,t)}focusNearestPath(e){return this.#t.focusNearestPath(e)}add(e){this.#t.add(e)}batch(e){this.#t.batch(e)}applyGitStatusPatch(e){let t=si(this.#m,e);if(t===this.#m)return;this.#m=t;let n=this.#D();n!=null&&hs(n.wrapper,this.#E())}move(e,t,n){this.#t.move(e,t,n)}onMutation(e,t){return this.#t.onMutation(e,t)}setSearch(e){this.#t.setSearch(e)}openSearch(e){this.#t.openSearch(e)}closeSearch(){this.#t.closeSearch()}isSearchOpen(){return this.#t.isSearchOpen()}getSearchValue(){return this.#t.getSearchValue()}getSearchMatchingPaths(){return this.#t.getSearchMatchingPaths()}focusNextSearchMatch(){this.#t.focusNextSearchMatch()}focusPreviousSearchMatch(){this.#t.focusPreviousSearchMatch()}startRenaming(e,t){return this.#t.startRenaming(e,t)}remove(e,t){this.#t.remove(e,t)}resetPaths(e,t){Array.isArray(e)?this.#t.resetPaths(e,t):this.#t.resetPaths(e)}setComposition(e){this.#e=e;let t=this.#D();t!=null&&(this.#A(),hs(t.wrapper,this.#E()))}setGitStatus(e){let t=oi(e,this.#m);if(t===this.#m)return;this.#m=t;let n=this.#D();n!=null&&hs(n.wrapper,this.#E())}setIcons(e){this.#h=e;let t=this.#D();t!=null&&(this.#O(t.host,t.wrapper),hs(t.wrapper,this.#E()))}hydrate({fileTreeContainer:e}){let t=this.#I(e),n=this.#F(t);this.#A(),gs(n,this.#E())}render({containerWrapper:e,fileTreeContainer:t}){let n=this.#I(t??this.#p,e),r=this.#F(n);this.#A(),hs(r,this.#E())}#T(){return{initialViewportHeight:xs({initialVisibleRowCount:this.#f.initialVisibleRowCount,itemHeight:this.#f.itemHeight}),itemHeight:this.#f.itemHeight,middleTruncate:this.#f.middleTruncate,overscan:this.#f.overscan,stickyFolders:this.#f.stickyFolders}}#E(){return{composition:this.#e,controller:this.#t,gitStatusByPath:this.#m?.statusByPath,ignoredGitDirectories:this.#m?.ignoredDirectoryPaths,directoriesWithGitChanges:this.#m?.directoriesWithChanges,icons:this.#h,instanceId:this.#n,renamingEnabled:this.#o,renderRowDecoration:this.#a,searchBlurBehavior:this.#s,searchEnabled:this.#c,searchFakeFocus:this.#l,slotHost:this.#u,...this.#T()}}#D(){let e=this.#p,t=this.#S;return e==null||t==null?null:{host:e,wrapper:t}}#O(e,t){let n=e.shadowRoot;n!=null&&(this.#j(n),this.#M(n)),this.#N(t)}#k(){let e=this.#i;if(e==null)return;let t=this.#t.getSelectionVersion();t!==this.#y&&(this.#y=t,e(this.#t.getSelectedPaths()))}#A(){let e=this.#e?.header?.render;if(e!=null){this.#u.setSlotContent(u,e());return}this.#u.setSlotHtml(u,this.#e?.header?.html??null)}#j(e){let r=ws(e).find(e=>Cs(e)),i=Ss(n(t(this.#h).set));i!=null&&(r==null||r.outerHTML!==i.outerHTML)&&(r==null?e.prepend(i):r.replaceWith(i))}#M(e){let n=ws(e),r=n.find(e=>Cs(e)),i=n.filter(e=>e!==r),a=t(this.#h).spriteSheet?.trim()??``;if(a.length===0){for(let e of i)e.remove();return}let o=Ss(a);if(o==null){for(let e of i)e.remove();return}if(i.length!==1||i[0].outerHTML!==o.outerHTML){for(let e of i)e.remove();e.appendChild(o)}}#N(n){let r=t(this.#h);r.colored&&e(r.set)?n.dataset.fileTreeColoredIcons=`true`:delete n.dataset.fileTreeColoredIcons}#P(e){let t=e.querySelector(`style[${o}]`);if(this.#_==null&&t instanceof HTMLStyleElement&&(this.#_=t),this.#g==null||this.#g===``){this.#_?.remove(),this.#_=void 0,this.#v=void 0;return}(this.#_?.parentNode!==e||this.#v!==this.#g)&&(this.#_??=document.createElement(`style`),this.#_.setAttribute(o,``),this.#_.parentNode!==e&&e.appendChild(this.#_),this.#_.textContent=g(this.#g),this.#v=this.#g)}#F(e){if(this.#S!=null)return this.#S;let t=e.shadowRoot;if(t==null)throw Error(`FileTree requires a shadow root`);let n=Array.from(t.children).filter(e=>e instanceof HTMLDivElement&&typeof e.dataset.fileTreeId==`string`&&e.dataset.fileTreeId.length>0),r=n.find(e=>e.dataset.fileTreeId===this.#n)??n[0];return r!=null&&(this.#n=r.dataset.fileTreeId??this.#n),this.#S=r??document.createElement(`div`),this.#S.dataset.fileTreeId=this.#n,this.#S.dataset.fileTreeVirtualizedWrapper=`true`,this.#O(e,this.#S),this.#S.parentNode!==t&&t.appendChild(this.#S),this.#S}#I(e,t){let n=e??this.#p??document.createElement(`file-tree-container`);t!=null&&n.parentNode!==t&&t.appendChild(n);let r=n.shadowRoot??n.attachShadow({mode:`open`});return S(n,r),this.#P(r),n.dataset.fileTreeVirtualized=`true`,n.style.display=`flex`,this.#L(n),this.#u.setHost(n),this.#p=n,n}#L(e){e.style.getPropertyValue(`--trees-item-height`)===``&&(e.style.setProperty(`--trees-item-height`,`${String(this.#d.itemHeight)}px`),this.#C=!0),e.style.getPropertyValue(`--trees-density-override`)===``&&(e.style.setProperty(`--trees-density-override`,String(this.#d.factor)),this.#w=!0)}#R(e){this.#C&&=(e.style.removeProperty(`--trees-item-height`),!1),this.#w&&=(e.style.removeProperty(`--trees-density-override`),!1)}};export{Ts as t};