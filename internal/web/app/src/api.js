// The API client. The site signs in with its cookie; the Mini App with a
// bearer token traded for Telegram's launch data (POST /auth/miniapp), which
// it trades again when the token runs out.

export class ApiError extends Error {
  constructor(status, body, res) {
    super((body && body.error) || "http_" + status);
    this.status = status;
    this.code = (body && body.error) || (status ? "http_" + status : "network");
    this.body = body || {};
    this.retryAfter = +(res?.headers.get("Retry-After") || 0);
  }
}

export const session = { mini: false, initData: "", token: "", until: 0, dead: "" };

export function setToken(a) {
  if (!a) return;
  session.token = a.token;
  session.until = Date.now() + Math.max(60, (a.expires_in || 3600) - 60) * 1000;
}

/* Telegram signs the launch data only when the Mini App opens, and framy takes it for an hour
   (init_data_expired after that; init_data_invalid when it does not check out). Once it is
   refused, nothing here can sign in again until the Mini App is opened anew: the session is
   dead, every call fails at once as signed out without asking framy, and the page hears it
   once, as "framy:expired" with the reason. */
export function endSession(why) {
  if (session.dead) return;
  session.dead = why || "init_data_invalid";
  session.token = "";
  dispatchEvent(new CustomEvent("framy:expired", { detail: session.dead }));
}
const signedOut = () => new ApiError(401, { error: "sign_in", why: session.dead });

let trading = null;
async function trade() {
  trading ??= fetch("/auth/miniapp", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ init_data: session.initData }) })
    .catch(() => { throw new ApiError(0, { error: "network" }); })
    .then(async (r) => {
      const b = await r.json().catch(() => null);
      if (r.status === 401 || r.status === 400) { endSession(b?.error); throw signedOut(); }
      if (!r.ok) throw new ApiError(r.status, b, r);
      setToken(b);
    })
    .finally(() => { trading = null; });
  return trading;
}

async function authHeader(fresh) {
  if (!session.mini) return {};
  if (session.dead) throw signedOut();
  if (fresh || !session.token || Date.now() > session.until) await trade();
  return { Authorization: "Bearer " + session.token };
}

export async function api(path, o = {}) {
  for (let attempt = 0; ; attempt++) {
    const headers = { ...(o.headers || {}), ...(await authHeader(attempt > 0)) };
    let body = o.body;
    if (body !== undefined && !(body instanceof Blob)) { headers["Content-Type"] = "application/json"; body = JSON.stringify(body); }
    let r;
    try {
      r = await fetch(path, { method: o.method || "GET", headers, body, signal: o.signal, cache: "no-store" });
    } catch (e) {
      if (e.name === "AbortError") throw e;
      throw new ApiError(0, { error: "network" });
    }
    // A Mini App token that ran out: trade the launch data once more and repeat.
    if (r.status === 401 && session.mini && attempt === 0) continue;
    const data = (r.headers.get("Content-Type") || "").includes("json") ? await r.json().catch(() => null) : null;
    if (!r.ok) throw new ApiError(r.status, data, r);
    return data;
  }
}

export const isFinal = (s) => s === "done" || s === "failed" || s === "cancelled" || s === "expired";
// What a job shows: the clip it went on from with its own video after it,
// where the server joined them (results then hold only the new part), else its results.
export const outsOf = (j) => (j?.joined ? [j.joined] : j?.results || []);
const wait = (ms, sig) => new Promise((done) => { const t = setTimeout(done, ms); sig?.addEventListener("abort", () => { clearTimeout(t); done(); }); });

/* A job's events (GET /api/jobs/{id}/events). Server-sent events read through fetch, since the Mini App
   needs its Authorization header and EventSource cannot send one. Each event is the whole job; a
   reconnect says Last-Event-ID and gets only what changed. When the server has three streams of the
   person's open already (429 too_many_streams), or the stream keeps failing (framy or the tunnel in
   front of it answering 5xx), the job is read every 5 seconds instead. A stream quiet for longer than
   three of the server's pings (every 15 s) is a connection that died without a word, and is opened
   again. onNet hears how the page hears: "live" over the stream, "poll" every 5 seconds, "lost" while
   nothing gets through. onError hears why watching ended before the job did: 404, the job is not the
   person's (any more), or 401, nobody is signed in. */
const QUIET = 45000;
export function watchJob(id, onJob, onError, onNet = () => {}) {
  const stop = new AbortController();
  let last = "", retry = 2000, net = "";
  const say = (v) => { if (v !== net && !stop.signal.aborted) { net = v; onNet(v); } };
  const deliver = (j) => { if (stop.signal.aborted) return; onJob(j); if (isFinal(j.status)) stop.abort(); };
  const fail = (e) => { if (stop.signal.aborted) return; stop.abort(); onError?.(e); };
  const ends = (e) => e.status === 404 || e.status === 401;

  async function poll() {
    while (!stop.signal.aborted) {
      try { const j = await api(`/api/jobs/${id}`, { signal: stop.signal }); say("poll"); deliver(j); }
      catch (e) {
        if (e.name === "AbortError") return;
        if (ends(e)) { fail(e); return; }
        if (e.code === "network" || e.status >= 500) say("lost");
      }
      await wait(5000, stop.signal);
    }
  }

  async function stream() {
    let fails = 0;
    while (!stop.signal.aborted) {
      // An attempt that hears nothing for QUIET, its answer or its next line, is dropped and made again.
      const one = new AbortController(), drop = () => one.abort();
      let quiet = 0;
      const heard = () => { clearTimeout(quiet); quiet = setTimeout(drop, QUIET); };
      stop.signal.addEventListener("abort", drop, { once: true });
      try {
        let r;
        try {
          const headers = { Accept: "text/event-stream", ...(await authHeader(fails > 0 && session.mini)) };
          if (last) headers["Last-Event-ID"] = last;
          heard();
          r = await fetch(`/api/jobs/${id}/events`, { headers, signal: one.signal, cache: "no-store" });
        } catch (e) {
          if (stop.signal.aborted) return;
          if (ends(e)) { fail(e); return; }
          fails++; say("lost"); await wait(Math.min(retry * fails, 15000), stop.signal); continue;
        }
        if (r.status === 204) {
          // Final and already seen: read it once for the result.
          r.body?.cancel();
          try { deliver(await api(`/api/jobs/${id}`, { signal: stop.signal })); return; }
          catch (e) { if (e.name === "AbortError") return; if (ends(e)) { fail(e); return; } return poll(); }
        }
        if (r.status === 429) { r.body?.cancel(); say("poll"); return poll(); }
        if (r.status === 401 && session.mini && fails < 2) { r.body?.cancel(); fails++; continue; }
        if (ends(r)) { const b = await r.json().catch(() => null); fail(new ApiError(r.status, b, r)); return; }
        if (!r.ok) {
          // framy is restarting (503), or it or the tunnel failed (500, 502, 504, Cloudflare's 52x):
          // again after a pause that grows, saying so from the second time, then the job is read instead.
          r.body?.cancel(); fails++;
          if (fails > 1) say("lost");
          if (fails > 3) return poll();
          clearTimeout(quiet);
          await wait((+(r.headers.get("Retry-After") || 0) * 1000) || Math.min(retry * fails, 15000), stop.signal); continue;
        }
        fails = 0; say("live");
        try { await read(r.body, heard); } catch { if (stop.signal.aborted) return; say("lost"); }
        if (stop.signal.aborted) return;
        clearTimeout(quiet);
        await wait(retry, stop.signal);
      } finally { clearTimeout(quiet); stop.signal.removeEventListener("abort", drop); }
    }
  }

  async function read(body, heard) {
    const rd = body.pipeThrough(new TextDecoderStream()).getReader();
    let buf = "";
    for (;;) {
      const { value, done } = await rd.read();
      if (done) return;
      heard();
      buf += value;
      let m;
      while ((m = buf.match(/\r?\n\r?\n/))) {
        const block = buf.slice(0, m.index);
        buf = buf.slice(m.index + m[0].length);
        let ev = "message", data = "", eid = null;
        for (const line of block.split(/\r?\n/)) {
          if (!line || line[0] === ":") continue;
          const c = line.indexOf(":"), k = c < 0 ? line : line.slice(0, c), v = c < 0 ? "" : line.slice(c + 1).replace(/^ /, "");
          if (k === "event") ev = v; else if (k === "data") data += (data ? "\n" : "") + v; else if (k === "id") eid = v;
          else if (k === "retry" && +v > 0) retry = +v;
        }
        if (eid != null) last = eid;
        if (ev === "job" && data) { try { deliver(JSON.parse(data)); } catch {} }
        if (stop.signal.aborted) { rd.cancel().catch(() => {}); return; }
      }
    }
  }

  stream();
  return () => stop.abort();
}
