# VELIN assets

## Hero portraits

The project owner supplied and approved `public/portrait_top.png` and
`public/portrait_bottom.png`. Both are retained byte-for-byte. Each is a
1672 × 941 RGB PNG carrying an embedded C2PA content-credential manifest that
identifies OpenAI's image model as the generator and marks the image as
synthetic (`trainedAlgorithmicMedia`). No photograph of a real person and no
former source portrait from any earlier design is included; the shader crops
the pair consistently to fill the viewport.

## Authored assets

The favicon (`public/favicon.svg`), the inline contour SVGs, the CSS study
patterns, the WebGL fluid trail, the Three.js ping-pong simulation and the GLSL
shaders are authored in this repository. Reduced motion disables the automatic
trail and marquee. No other bitmap, video or audio media is bundled.

## Fonts

Instrument Serif and DM Mono are loaded from Google Fonts under the SIL Open
Font License. No font binaries are bundled. Three.js is an MIT-licensed npm
dependency.

## Placeholders

`scripts/placeholders.mjs` can create a matching local placeholder pair when
portraits are missing. It preserves existing images unless explicitly given
`--force`. The approved portraits do not need replacement.
