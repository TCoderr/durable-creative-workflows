/**
 * script.js — hero fluid-trail reveal.
 *
 * A full-viewport canvas shows portrait_top.png. The cursor paints a soft trail
 * into an off-screen ping-pong mask; where the mask crosses a threshold,
 * portrait_bottom.png is revealed underneath. When the pointer has been still
 * (or has left the hero) for a while, a synthetic cursor takes over and keeps
 * drawing on its own — see the idle auto-trail block in animate().
 */

import * as THREE from 'three';
import {
  vertexShader,
  fluidFragmentShader,
  displayFragmentShader,
} from './shaders.js';

const CONFIG = {
  // Simulation render-target size (square, ping-pong)
  simSize: 500,

  // Trail mask falloff
  decay: 0.97,
  lineWidth: 0.09,
  perFrameIntensity: 0.3,

  // Reveal threshold (display shader)
  revealThreshold: 0.02,
  edgeWidthBase: 0.004, // divided by uDpr in shader

  // Soft gray halo overlay (display shader)
  haloUpperMul: 2.0, // halo upper bound = revealThreshold * this
  haloMixStrength: 0.35,
  haloGray: [0.12, 0.12, 0.12],

  // Idle auto-trail
  idleThresholdMs: 2500,
  idleEaseInMs: 1500,
  autoLerp: 0.05,

  // Mouse stop detection
  stopAfterMs: 50,

  // Max texture size
  maxTextureSize: 4096,
};

// Amplitudes of the two layered sines that steer the synthetic cursor. The
// frequencies are incommensurate, so the path never repeats; the amplitudes sum
// to well under 0.5, so it never walks off the canvas.
const AUTO_PATH = {
  xAmpSlow: 0.3,
  xAmpFast: 0.12,
  yAmpSlow: 0.28,
  yAmpFast: 0.1,
};

const canvas = document.querySelector('.hero canvas');

if (canvas) {
  initFluidReveal(canvas);
} else {
  console.warn(
    '[VELIN] No hero canvas found — the reveal was not initialised.',
  );
}

function initFluidReveal(canvas) {
  /* ------------------------------------------------------------------ *
   * Renderer, scenes, camera
   * ------------------------------------------------------------------ */

  let renderer;
  try {
    renderer = new THREE.WebGLRenderer({
      canvas,
      antialias: true,
      precision: 'highp',
    });
  } catch {
    canvas.dataset.rendering = 'unavailable';
    console.warn(
      '[VELIN] WebGL is unavailable; editorial content remains accessible.',
    );
    return;
  }
  const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)');
  let contextLost = false;
  canvas.addEventListener('webglcontextlost', () => {
    contextLost = true;
  });
  canvas.addEventListener('webglcontextrestored', () => {
    contextLost = false;
  });
  renderer.setSize(window.innerWidth, window.innerHeight);
  renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));

  // This is a 2D composite, not a lit scene: the display shader already emits
  // display-referred sRGB values read straight from the portraits, so the
  // renderer must pass them through untouched rather than re-encoding them.
  renderer.outputColorSpace = THREE.LinearSRGBColorSpace;

  const scene = new THREE.Scene();
  const simScene = new THREE.Scene();
  const camera = new THREE.OrthographicCamera(-1, 1, 1, -1, 0, 1);

  /* ------------------------------------------------------------------ *
   * Ping-pong render targets — these hold the trail mask
   * ------------------------------------------------------------------ */

  // FloatType targets need EXT_color_buffer_float to be renderable. Half-float
  // is renderable and linearly filterable everywhere WebGL2 is, and holds the
  // 0..1 mask with room to spare, so it is the fallback.
  const gl = renderer.getContext();
  const canRenderFloat = renderer.capabilities.isWebGL2
    ? gl.getExtension('EXT_color_buffer_float') !== null
    : gl.getExtension('OES_texture_float') !== null;

  const targetOptions = {
    minFilter: THREE.LinearFilter,
    magFilter: THREE.LinearFilter,
    format: THREE.RGBAFormat,
    type: canRenderFloat ? THREE.FloatType : THREE.HalfFloatType,
  };

  const pingPong = [
    new THREE.WebGLRenderTarget(CONFIG.simSize, CONFIG.simSize, targetOptions),
    new THREE.WebGLRenderTarget(CONFIG.simSize, CONFIG.simSize, targetOptions),
  ];
  let currentTarget = 0;

  // Start both targets empty so the first frame samples a clean mask.
  pingPong.forEach((target) => {
    renderer.setRenderTarget(target);
    renderer.clear();
  });
  renderer.setRenderTarget(null);

  /* ------------------------------------------------------------------ *
   * Pointer state — normalized [0,1] canvas coordinates, y up
   * ------------------------------------------------------------------ */

  const mouse = new THREE.Vector2(0.5, 0.5);
  const prevMouse = new THREE.Vector2(0.5, 0.5);
  let isMoving = false;
  let lastMoveTime = 0;

  // Synthetic cursor used while the visitor is idle.
  const autoMouse = new THREE.Vector2(0.5, 0.5);
  const prevAutoMouse = new THREE.Vector2(0.5, 0.5);

  /* ------------------------------------------------------------------ *
   * Textures
   * ------------------------------------------------------------------ */

  // Placeholders so the composite always has something to sample. Their size
  // uniforms stay at (0, 0), which makes getCoverUV fall through to raw uv.
  // Paper on top, ink beneath: if a portrait fails to load the reveal still
  // shows a visible change of state instead of paper over paper.
  const topTexture = createSolidTexture('#ece9e1');
  const bottomTexture = createSolidTexture('#0a0a0a');

  /* ------------------------------------------------------------------ *
   * Materials and meshes
   * ------------------------------------------------------------------ */

  const geometry = new THREE.PlaneGeometry(2, 2);

  const trailsMaterial = new THREE.ShaderMaterial({
    uniforms: {
      uPrevTrails: { value: pingPong[0].texture },
      uMouse: { value: new THREE.Vector2(0.5, 0.5) },
      uPrevMouse: { value: new THREE.Vector2(0.5, 0.5) },
      uResolution: {
        value: new THREE.Vector2(CONFIG.simSize, CONFIG.simSize),
      },
      uDecay: { value: CONFIG.decay },
      uIsMoving: { value: false },
    },
    vertexShader,
    fragmentShader: fluidFragmentShader,
  });

  const displayMaterial = new THREE.ShaderMaterial({
    uniforms: {
      uFluid: { value: pingPong[0].texture },
      uTopTexture: { value: topTexture },
      uBottomTexture: { value: bottomTexture },
      uResolution: {
        value: new THREE.Vector2(window.innerWidth, window.innerHeight),
      },
      uTopTextureSize: { value: new THREE.Vector2(0, 0) },
      uBottomTextureSize: { value: new THREE.Vector2(0, 0) },
      uDpr: { value: renderer.getPixelRatio() },
    },
    vertexShader,
    fragmentShader: displayFragmentShader,
  });

  const simMesh = new THREE.Mesh(geometry, trailsMaterial);
  simScene.add(simMesh);

  const displayMesh = new THREE.Mesh(geometry, displayMaterial);
  scene.add(displayMesh);

  /* ------------------------------------------------------------------ *
   * Portrait loading
   * ------------------------------------------------------------------ */

  loadPortrait('/portrait_top.png', 'uTopTexture', 'uTopTextureSize');
  loadPortrait('/portrait_bottom.png', 'uBottomTexture', 'uBottomTextureSize');

  /* ------------------------------------------------------------------ *
   * Input
   * ------------------------------------------------------------------ */

  window.addEventListener('mousemove', (event) => {
    updatePointer(event.clientX, event.clientY);
  });

  window.addEventListener(
    'touchmove',
    (event) => {
      const touch = event.touches[0];
      if (!touch) return;

      updatePointer(touch.clientX, touch.clientY);
    },
    { passive: true },
  );

  /* ------------------------------------------------------------------ *
   * Resize
   * ------------------------------------------------------------------ */

  window.addEventListener('resize', () => {
    renderer.setSize(window.innerWidth, window.innerHeight);
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    displayMaterial.uniforms.uResolution.value.set(
      window.innerWidth,
      window.innerHeight,
    );
    displayMaterial.uniforms.uDpr.value = renderer.getPixelRatio();
  });

  /* ------------------------------------------------------------------ *
   * Render loop
   * ------------------------------------------------------------------ */

  // The hero is one screen of a long page. Once it is fully scrolled past there
  // is nothing to composite, so the loop idles instead of burning the GPU while
  // someone reads the footer.
  let heroVisible = true;
  if ('IntersectionObserver' in window) {
    const heroObserver = new IntersectionObserver(
      ([entry]) => {
        heroVisible = entry.isIntersecting;
      },
      { threshold: 0 },
    );
    heroObserver.observe(canvas.closest('.hero') || canvas);
  }

  // Every trail constant is a PER-FRAME value tuned at 60Hz: the 0.97 decay, the
  // 0.3 deposit, and the shader's `len > 0.001` guard. Left uncapped on a 120 or
  // 144Hz display the synthetic cursor advances only ~0.0005 per frame, falls
  // under that guard, and the mask decays faster than it is drawn — the idle
  // trail never appears. Pacing the loop at 60Hz makes the trail identical on
  // every display, and costs nothing on a 60Hz one.
  const FRAME_MS = 1000 / 60;
  let nextFrameAt = 0;

  function animate() {
    requestAnimationFrame(animate);

    if (!heroVisible || contextLost || document.hidden) return;

    const now = performance.now();

    if (now < nextFrameAt) return;
    // Advance by a whole step so the cadence stays on 60Hz; after a stall
    // (background tab, long task) resync to now instead of catching up.
    nextFrameAt = Math.max(now, nextFrameAt + FRAME_MS);

    if (isMoving && now - lastMoveTime > CONFIG.stopAfterMs) {
      isMoving = false;
    }

    const idleTime = now - lastMoveTime;
    const autoActive =
      !reducedMotion.matches && idleTime > CONFIG.idleThresholdMs;

    // Swap ping-pong
    const prevTarget = pingPong[currentTarget];
    currentTarget = (currentTarget + 1) % 2;
    const writeTarget = pingPong[currentTarget];

    trailsMaterial.uniforms.uPrevTrails.value = prevTarget.texture;

    if (autoActive) {
      const easeIn = Math.min(
        1,
        (idleTime - CONFIG.idleThresholdMs) / CONFIG.idleEaseInMs,
      );

      // Layered low-frequency sines on INCOMMENSURATE frequencies — the
      // resulting path is organic, never repeats, never spikes off-canvas.
      const t = now * 0.001;
      const targetX =
        0.5 +
        AUTO_PATH.xAmpSlow * Math.sin(t * 0.41) +
        AUTO_PATH.xAmpFast * Math.sin(t * 0.93 + 1.3);
      const targetY =
        0.5 +
        AUTO_PATH.yAmpSlow * Math.cos(t * 0.37 + 0.5) +
        AUTO_PATH.yAmpFast * Math.cos(t * 1.11 + 2.7);

      prevAutoMouse.copy(autoMouse);
      autoMouse.x += (targetX - autoMouse.x) * CONFIG.autoLerp * easeIn;
      autoMouse.y += (targetY - autoMouse.y) * CONFIG.autoLerp * easeIn;

      trailsMaterial.uniforms.uMouse.value.copy(autoMouse);
      trailsMaterial.uniforms.uPrevMouse.value.copy(prevAutoMouse);
      trailsMaterial.uniforms.uIsMoving.value = true;

      // Mirror onto mouse/prevMouse so the next real move continues from
      // where auto left off — no stale long segment on the handoff frame.
      mouse.copy(autoMouse);
      prevMouse.copy(prevAutoMouse);
    } else {
      trailsMaterial.uniforms.uMouse.value.copy(mouse);
      trailsMaterial.uniforms.uPrevMouse.value.copy(prevMouse);
      trailsMaterial.uniforms.uIsMoving.value =
        isMoving && !reducedMotion.matches;

      // Mirror so the next idle cycle starts from the user's last position.
      autoMouse.copy(mouse);
      prevAutoMouse.copy(mouse);
    }

    renderer.setRenderTarget(writeTarget);
    renderer.render(simScene, camera);

    displayMaterial.uniforms.uFluid.value = writeTarget.texture;

    renderer.setRenderTarget(null);
    renderer.render(scene, camera);
  }

  animate();

  /* ------------------------------------------------------------------ *
   * Helpers
   * ------------------------------------------------------------------ */

  function updatePointer(clientX, clientY) {
    const rect = canvas.getBoundingClientRect();
    const inside =
      clientX >= rect.left &&
      clientX <= rect.right &&
      clientY >= rect.top &&
      clientY <= rect.bottom;

    if (!inside) {
      // Deliberately do NOT touch lastMoveTime here: leaving the hero lets the
      // idle clock keep running, so the auto-trail carries on while the visitor
      // is reading further down the page.
      isMoving = false;
      return;
    }

    prevMouse.copy(mouse);
    mouse.x = (clientX - rect.left) / rect.width;
    mouse.y = 1 - (clientY - rect.top) / rect.height;
    isMoving = true;
    lastMoveTime = performance.now();
  }

  function createSolidTexture(color) {
    const element = document.createElement('canvas');
    element.width = 4;
    element.height = 4;

    const context = element.getContext('2d');
    context.fillStyle = color;
    context.fillRect(0, 0, element.width, element.height);

    const texture = new THREE.CanvasTexture(element);
    texture.minFilter = THREE.LinearFilter;
    texture.magFilter = THREE.LinearFilter;
    texture.wrapS = THREE.ClampToEdgeWrapping;
    texture.wrapT = THREE.ClampToEdgeWrapping;
    texture.generateMipmaps = false;

    return texture;
  }

  function loadPortrait(url, textureUniform, sizeUniform) {
    const image = new Image();
    image.crossOrigin = 'Anonymous';

    image.onload = () => {
      const naturalWidth = image.naturalWidth;
      const naturalHeight = image.naturalHeight;

      const scale = Math.min(
        1,
        CONFIG.maxTextureSize / Math.max(naturalWidth, naturalHeight),
      );
      const width = Math.round(naturalWidth * scale);
      const height = Math.round(naturalHeight * scale);

      const element = document.createElement('canvas');
      element.width = width;
      element.height = height;
      element.getContext('2d').drawImage(image, 0, 0, width, height);

      const texture = new THREE.CanvasTexture(element);
      texture.minFilter = THREE.LinearFilter;
      texture.magFilter = THREE.LinearFilter;
      texture.wrapS = THREE.ClampToEdgeWrapping;
      texture.wrapT = THREE.ClampToEdgeWrapping;
      texture.generateMipmaps = false;
      texture.needsUpdate = true;

      const previous = displayMaterial.uniforms[textureUniform].value;
      displayMaterial.uniforms[textureUniform].value = texture;
      displayMaterial.uniforms[sizeUniform].value.set(width, height);
      if (previous) previous.dispose();

      const note =
        scale < 1 ? ` (downscaled from ${naturalWidth}x${naturalHeight})` : '';
      console.log(`[VELIN] Loaded ${url} — ${width}x${height}${note}`);
    };

    image.onerror = () => {
      console.error(
        `[VELIN] Failed to load ${url}. The placeholder swatch stays bound; ` +
          `the trail still animates.`,
      );
    };

    image.src = url;
  }
}
