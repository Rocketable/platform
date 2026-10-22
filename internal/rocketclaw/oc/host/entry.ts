// The built entry, /oc-ui/_assets/index.js. The view's stylesheets load with its chunk, as for any lazy chunk, so the
// entry imports it lazily: RocketClaw's import() of the entry resolves once mount is ready and its styles are loaded.
export const { mount } = await import("./mount")
