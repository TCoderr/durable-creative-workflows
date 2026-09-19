/**
 * shaders.js — GLSL sources for the hero fluid-trail reveal.
 *
 * Two passes share one vertex shader:
 *
 *   1. fluidFragmentShader   simulation pass. Renders into a 500x500 ping-pong
 *                            target that holds the trail mask in its red channel.
 *   2. displayFragmentShader composite pass. Reads that mask and cross-fades the
 *                            top portrait into the bottom portrait wherever the
 *                            mask has been painted.
 *
 * The numeric constants below (lineWidth 0.09, per-frame intensity 0.3, reveal
 * threshold 0.02, edge width 0.004, halo bounds) are inlined so the compiler can
 * constant-fold them. They mirror CONFIG in script.js — change both together.
 */

export const vertexShader = /* glsl */ `
  varying vec2 vUv;

  void main() {
    vUv = uv;
    gl_Position = projectionMatrix * modelViewMatrix * vec4(position, 1.0);
  }
`;

/**
 * Simulation pass.
 *
 * Every frame the previous mask is faded by uDecay and, if the cursor moved, a
 * soft capsule is stamped along the segment prevMouse -> mouse. Stamping the
 * whole segment (rather than a dot at the cursor) is what keeps fast movement
 * continuous instead of dotted.
 */
export const fluidFragmentShader = /* glsl */ `
  uniform sampler2D uPrevTrails;
  uniform vec2 uMouse;
  uniform vec2 uPrevMouse;
  uniform vec2 uResolution;
  uniform float uDecay;
  uniform bool uIsMoving;

  varying vec2 vUv;

  void main() {
    vec4 prev = texture2D(uPrevTrails, vUv);
    float newValue = prev.r * uDecay;

    if (uIsMoving) {
      vec2 dir = uMouse - uPrevMouse;
      float len = length(dir);

      if (len > 0.001) {
        // Closest point on the segment prevMouse -> mouse.
        vec2 d = dir / len;
        vec2 toPx = vUv - uPrevMouse;
        float proj = clamp(dot(toPx, d), 0.0, len);
        vec2 closest = uPrevMouse + proj * d;
        float dist = length(vUv - closest);

        float lineWidth = 0.09;
        float intensity = smoothstep(lineWidth, 0.0, dist) * 0.3;
        newValue += intensity;
      }
    }

    gl_FragColor = vec4(newValue, 0.0, 0.0, 1.0);
  }
`;

/**
 * Composite pass.
 *
 * getCoverUV replicates CSS `object-fit: cover` in shader space so both
 * portraits fill the viewport at their own aspect ratio without stretching.
 * A texture whose size uniform is still (0, 0) — i.e. the placeholder that is
 * bound before the real image decodes — skips the fit and samples uv directly.
 */
export const displayFragmentShader = /* glsl */ `
  uniform sampler2D uFluid;
  uniform sampler2D uTopTexture;
  uniform sampler2D uBottomTexture;
  uniform vec2 uResolution;
  uniform vec2 uTopTextureSize;
  uniform vec2 uBottomTextureSize;
  uniform float uDpr;

  varying vec2 vUv;

  vec2 getCoverUV(vec2 uv, vec2 ts) {
    if (ts.x < 1.0 || ts.y < 1.0) return uv;

    vec2 s = uResolution / ts;
    float scale = max(s.x, s.y);
    vec2 scaled = ts * scale;
    vec2 offset = (uResolution - scaled) * 0.5;

    return (uv * uResolution - offset) / scaled;
  }

  void main() {
    float fluid = texture2D(uFluid, vUv).r;

    vec2 topUV = getCoverUV(vUv, uTopTextureSize);
    vec2 bottomUV = getCoverUV(vUv, uBottomTextureSize);

    vec4 topColor = texture2D(uTopTexture, topUV);
    vec4 bottomColor = texture2D(uBottomTexture, bottomUV);

    float threshold = 0.02;
    float edgeWidth = 0.004 / uDpr;
    float t = smoothstep(threshold, threshold + edgeWidth, fluid);

    // Soft gray trail-visibility overlay (top image only).
    // Halo peaks just before the reveal threshold and fades out as
    // t -> 1 so the bottom image still appears clean inside the trail.
    float halo = smoothstep(0.0, threshold * 2.0, fluid) * (1.0 - t);
    vec3 trailGray = vec3(0.12);
    vec3 tintedTop = mix(topColor.rgb, trailGray, halo * 0.35);

    vec4 finalColor = mix(vec4(tintedTop, topColor.a), bottomColor, t);

    gl_FragColor = finalColor;
  }
`;
