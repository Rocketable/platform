# Theme sources and licenses

RocketClaw's color themes are defined in
[`internal/rocketclaw/web/app/globals.css`](internal/rocketclaw/web/app/globals.css).
The chooser's names are defined in
[`theme.tsx`](internal/rocketclaw/web/src/components/theme.tsx).

## Upstream credit

The Neutral base theme and semantic CSS token structure come from **shadcn/ui**,
by shadcn and contributors. The project's
[`components.json`](internal/rocketclaw/web/components.json) selects `base-nova`
with the `neutral` base color. RocketClaw adds message and sidebar-row tokens
and adjusts some chart values; this is an adapted theme, not an unchanged copy.

Verified upstream references:

- [Theming documentation and default Neutral CSS](https://ui.shadcn.com/docs/theming#default-theme-css).
- [Pinned source of that documentation](https://github.com/shadcn-ui/ui/blob/98a1fe67b439324ddc857f47fbdce056600a4329/apps/v4/content/docs/%28root%29/theming.mdx).
- [Pinned shadcn/ui MIT license](https://github.com/shadcn-ui/ui/blob/98a1fe67b439324ddc857f47fbdce056600a4329/LICENSE.md).

These references were checked on September 22, 2026. The pinned revision is the
revision checked for this attribution, not a claim about the revision originally
used to generate the app.

## Palette provenance

The implementation record shows the following additional palettes being written
by the coding assistant for this project. No external theme collection was
consulted or copied for these overrides in that record. They build on the
shadcn/ui base above; their names do not identify ports of third-party themes.
There is no separate external source URL to attribute for these custom values.

| Theme | Source of the palette values |
| --- | --- |
| Neutral | Adapted shadcn/ui Neutral base, linked above. |
| Harbor | Project-local OKLCH formulas; hue 252, chroma 0.04. |
| Grove | Project-local OKLCH formulas; hue 152, chroma 0.045. |
| Ember | Project-local OKLCH formulas; hue 55, chroma 0.05. |
| Violet | Project-local OKLCH formulas; hue 300, chroma 0.05. |
| Rose | Project-local OKLCH formulas; hue 12, chroma 0.045. |
| Sand | Project-local OKLCH formulas; hue 85, chroma 0.022. |
| Lagoon | Project-local OKLCH formulas; hue 195, chroma 0.04. |
| Slate | Project-local OKLCH formulas; hue 250, chroma 0.012. |
| Copper | Project-local OKLCH formulas; hue 42, chroma 0.05. |
| Ink | Project-local black/white high-contrast overrides. |
| Signal | Project-local black/white and yellow high-contrast overrides. |

The hue and chroma entries are inputs to the shared light/dark formulas, not
complete color specifications. The linked stylesheet is the source of truth.

## Permission to use

### Go - Playground and Go - Sources

These two themes adapt **Go Themes (playground & src) 0.0.3**, by
**Mike Gleason jr Couturier**, under the MIT license:

- [Marketplace listing](https://marketplace.visualstudio.com/items?itemName=mikegleasonjr.theme-go).
- [Go - Playground source](https://github.com/mikegleasonjr/vscode-theme-go/blob/bfc330e33e3cd5fb80dc974ca7221e90ef735bf3/themes/playground.tmTheme).
- [Go - Sources source](https://github.com/mikegleasonjr/vscode-theme-go/blob/bfc330e33e3cd5fb80dc974ca7221e90ef735bf3/themes/sources.tmTheme).
- [License](https://github.com/mikegleasonjr/vscode-theme-go/blob/bfc330e33e3cd5fb80dc974ca7221e90ef735bf3/LICENSE).

Light mode retains the upstream backgrounds (`#FFFFDD` and `#EFEFEF`),
link blue (`#375EAB`), selection blue (`#B2D7F0`), and inactive selection
(`#D4D4C6`). RocketClaw maps those editor colors to its semantic UI tokens;
it does not import editor search decorations or syntax highlighting. Black text
and white text on blue buttons are local UI choices. Upstream supplies no dark
themes: both dark variations use RocketClaw's shared OKLCH formulas.

Verified September 22, 2026. MIT permits commercial use, modification, and
redistribution with the following notice retained:

```text
MIT License

Copyright (c) 2016 Mike Gleason jr Couturier

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### Base theme and local modifications

shadcn/ui uses the MIT license, which permits commercial use, modification,
distribution, sublicensing, and sale. It requires retaining its copyright and
permission notice in copies or substantial portions of the software. The full
notice is retained below and must accompany distributions containing this code.

Project-local modifications are covered by this repository's
[`LICENSE`](LICENSE), which also grants MIT-style permissions and requires
retaining the Rocketable notice. No additional third-party palette license was
identified for the custom overrides above.

### shadcn/ui license notice

```text
MIT License

Copyright (c) 2023 shadcn

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
