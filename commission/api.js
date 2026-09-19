/**
 * commission/api.js — authenticated client for the control-plane API.
 *
 * The bearer token lives only in this object for the life of the tab. Every
 * response is treated as untrusted JSON; the folio renders strings with
 * textContent and never accepts a hinted stage as truth.
 */

/** @param {unknown} value */
export function object(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('The service returned an unexpected response.');
  }
  return /** @type {Record<string, unknown>} */ (value);
}

/** @param {unknown} value */
export function text(value) {
  return typeof value === 'string' ? value : '';
}

export class ApiError extends Error {
  /**
   * @param {number} status
   * @param {string} code
   * @param {string} message
   * @param {Record<string, unknown>} [body]
   */
  constructor(status, code, message, body = {}) {
    super(message);
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

export class CommissionApi {
  /** @param {string} token */
  constructor(token) {
    this.token = token;
  }

  clear() {
    this.token = '';
  }

  /**
   * @param {string} path
   * @param {RequestInit} [options]
   */
  async request(path, options = {}) {
    const response = await fetch(`/api/v1${path}`, {
      ...options,
      headers: {
        Authorization: `Bearer ${this.token}`,
        ...(options.body ? { 'Content-Type': 'application/json' } : {}),
        ...(options.headers || {}),
      },
      signal: options.signal ?? AbortSignal.timeout(20000),
      cache: 'no-store',
      credentials: 'omit',
    });
    const body = object(await response.json());
    if (!response.ok) {
      throw new ApiError(
        response.status,
        text(body.code),
        text(body.message) || 'The request could not be completed.',
        body,
      );
    }
    return body;
  }

  /**
   * Streams state hints. Each hint only triggers a fresh authoritative read.
   * @param {string} id
   * @param {AbortSignal} signal
   * @param {() => void} onEvent
   */
  async events(id, signal, onEvent) {
    const response = await fetch(
      `/api/v1/workflows/${encodeURIComponent(id)}/events`,
      {
        headers: {
          Authorization: `Bearer ${this.token}`,
          Accept: 'text/event-stream',
        },
        cache: 'no-store',
        credentials: 'omit',
        signal,
      },
    );
    if (!response.ok || !response.body) {
      throw new Error('Live progress is temporarily unavailable.');
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let pending = '';
    try {
      while (!signal.aborted) {
        const { value, done } = await reader.read();
        if (done) break;
        pending += decoder
          .decode(value, { stream: true })
          .replace(/\r\n/g, '\n');
        if (pending.length > 65536) {
          throw new Error('Progress message exceeded its limit.');
        }
        let boundary;
        while ((boundary = pending.indexOf('\n\n')) !== -1) {
          const event = pending.slice(0, boundary);
          pending = pending.slice(boundary + 2);
          if (event.split('\n').some((line) => line.startsWith('data:'))) {
            onEvent();
          }
        }
      }
    } finally {
      await reader.cancel().catch(() => undefined);
    }
  }
}
