import { NextRequest, NextResponse } from "next/server";
import { PHASE_PRODUCTION_BUILD } from "next/dist/shared/lib/constants.js";

import * as pubsub from "./pubsub.js";

type NameValuePair = { name: string; value: string };
type RouteHandler = (req: NextRequest, context?: any) => NextResponse | Promise<NextResponse>;
type ConnState =
  | {
      kind: "Uninitialized";
    }
  | { kind: "Creating"; value: Promise<WebSocket> }
  | {
      kind: "Created";
      value: WebSocket;
    };

const originalFetch = globalThis.fetch;
const connKey = Symbol.for("SUBTRACE_WEBSOCKET_CONNECTION");

let SUBTRACE_ENDPOINT: string = "";
let SUBTRACE_TOKEN: string = "";
let SUBTRACE_DEBUG: boolean = false;
let SUBTRACE_PAYLOAD_LIMIT: number = 0;

const encoder = new TextEncoder();

function limit(stream: ReadableStream<Uint8Array>, maxBytes: number): ReadableStream {
  const reader = stream.getReader();
  let bytesLeft = maxBytes;

  return new ReadableStream<Uint8Array>({
    async pull(controller) {
      while (bytesLeft > 0) {
        const { value, done } = await reader.read();
        if (done) break;
        if (!value) continue;

        const chunk = value.length > bytesLeft ? value.subarray(0, bytesLeft) : value;
        controller.enqueue(chunk);
        bytesLeft -= chunk.length;

        if (bytesLeft <= 0) break;
      }
      controller.close();
    },
    async cancel() {
      await reader.cancel();
    },
  });
}

function patchFetch(): void {
  if (!globalThis.fetch) {
    return;
  }

  globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const start = performance.now();

    let req = new Request(input, init);
    let clonedReq: Request;
    if (req.body) {
      const [origBody, clonedBody] = req.body.tee();
      clonedReq = new Request(req, { body: limit(clonedBody, SUBTRACE_PAYLOAD_LIMIT), duplex: "half" } as RequestInit);
      req = new Request(req, { body: origBody, duplex: "half" } as RequestInit);
    } else {
      clonedReq = req;
    }

    const timestamp = new Date();
    const origStart = performance.now();
    let resp = await originalFetch(input, init);
    const origEnd = performance.now();

    let clonedResp: Response;
    if (resp.body) {
      const [origBody, clonedBody] = resp.body.tee();
      clonedResp = new Response(limit(clonedBody, SUBTRACE_PAYLOAD_LIMIT), resp);
      resp = new Response(origBody, resp);
    } else {
      clonedResp = resp;
    }

    await upload(clonedReq, clonedResp, timestamp.toString(), origEnd - origStart);
    const end = performance.now();

    if (SUBTRACE_DEBUG) {
      console.log("Overall", end - start);
      console.log("Overhead", end - start - (origEnd - origStart));
    }

    return resp;
  };
}

async function createHarEntry(req: Request, resp: Response, timestamp: string, duration: number) {
  let reqBody = "";
  let respBody = "";
  try {
    [reqBody, respBody] = await Promise.all([req.text(), resp.text()]);
  } catch {}

  const reqHeaders: NameValuePair[] = [];
  for (const [name, value] of req.headers) {
    reqHeaders.push({ name, value });
  }

  const queryString: NameValuePair[] = [];
  for (const [name, value] of new URL(req.url).searchParams) {
    queryString.push({ name, value });
  }

  const resHeaders: NameValuePair[] = [];
  for (const [name, value] of resp.headers) {
    resHeaders.push({ name, value });
  }

  return {
    startedDateTime: timestamp,
    time: duration,
    request: {
      method: req.method,
      url: req.url,
      httpVersion: "HTTP/1.1",
      headers: reqHeaders,
      queryString,
      postData: reqBody
        ? {
            mimeType: req.headers.get("content-type") || "application/octet-stream",
            text: reqBody,
          }
        : undefined,
      headersSize: -1,
      bodySize: reqBody.length,
    },
    response: {
      status: resp.status,
      statusText: resp.statusText,
      httpVersion: "HTTP/1.1",
      headers: resHeaders,
      content: {
        size: respBody.length,
        mimeType: resp.headers.get("content-type") || "application/octet-stream",
        text: respBody,
      },
      redirectURL: resp.headers.get("location") || "",
      headersSize: -1,
      bodySize: respBody.length,
    },
    cache: {},
    timings: {
      send: 0,
      wait: duration,
      receive: 0,
    },
  };
}

async function wait(ms: number): Promise<void> {
  return new Promise<void>((resolve) =>
    setTimeout(() => {
      resolve();
    }, ms)
  );
}

async function getWebSocket(): Promise<WebSocket> {
  const state: ConnState | undefined = (globalThis as any)[connKey];
  switch (state?.kind) {
    case "Uninitialized":
    case undefined:
    case null:
      const promise = createWebSocket();
      (globalThis as any)[connKey] = { kind: "Creating", value: promise };
      return promise;

    case "Creating":
      const result = await state.value;
      (globalThis as any)[connKey] = { kind: "Created", value: result };
      return result;

    case "Created":
      return state.value;

    default:
      throw new Error(`Unexpected conn state: ${state}`);
  }
}

async function createWebSocket(): Promise<WebSocket> {
  let backoff = 1000;
  while (true) {
    let joinPubResp: Response;

    try {
      joinPubResp = await originalFetch(`${SUBTRACE_ENDPOINT}/api/JoinPublisher`, {
        method: "POST",
        body: JSON.stringify({}),
        headers: {
          Authorization: `Bearer ${SUBTRACE_TOKEN}`,
        },
      });

      if (joinPubResp.status !== 200) {
        await wait(backoff);
        backoff = Math.min(backoff + 1000, 10000);
        continue;
      }
    } catch (error: unknown) {
      console.log(error);
      await wait(backoff);
      backoff = Math.min(backoff + 1000, 10000);
      continue;
    }

    backoff = 1000;

    const { websocketUrl } = await joinPubResp.json();

    const ws = new WebSocket(websocketUrl);

    ws.binaryType = "arraybuffer";
    try {
      await new Promise<void>((resolve, reject) => {
        if (ws?.readyState === WebSocket.OPEN) {
          resolve();
        }
        ws?.addEventListener("open", () => resolve(), { once: true });
        ws?.addEventListener("error", (ev) => reject(new Error(JSON.stringify(ev))), { once: true });
        ws?.addEventListener("close", (ev) => reject(new Error(ev.reason)), { once: true });
      });
    } catch (error: unknown) {
      console.log(error);
      await wait(backoff);
      backoff = Math.min(backoff + 1000, 10000);
      continue;
    }

    return ws;
  }
}

async function upload(req: Request, resp: Response, timestamp: string, duration: number): Promise<void> {
  let ws = await getWebSocket();
  const harEntry = await createHarEntry(req, resp, timestamp, duration);
  const harEntryJson = encoder.encode(JSON.stringify(harEntry));

  const data = pubsub.Message.encode({
    concreteV1: {
      event: {
        concreteV1: {
          harEntryJson,
          tags: {},
          log: undefined,
        },
      },
    },
  }).finish();

  try {
    if (ws.readyState !== WebSocket.OPEN) {
      (globalThis as any)[connKey] = { kind: "Uninitialized" };
      ws = await getWebSocket();
    }
    ws.send(data);
  } catch (error: unknown) {
    console.log(error);
    ws.close();
    (globalThis as any)[connKey] = { kind: "Uninitialized" };
  }
}

export function trace(handler: RouteHandler): RouteHandler {
  return async function (req, context) {
    const start = performance.now();

    let clonedReq: NextRequest;
    if (req.body) {
      const [origBody, clonedBody] = req.body.tee();
      // Using `any` here since nextjs doesn't expose the ResponseInit typethat it uses, and the `duplex` property isn't
      // included there anyway.
      clonedReq = new NextRequest(req, { body: limit(clonedBody, SUBTRACE_PAYLOAD_LIMIT), duplex: "half" } as any);
      req = new NextRequest(req, { body: origBody, duplex: "half" } as any);
    } else {
      clonedReq = req;
    }

    const timestamp = new Date();
    const origStart = performance.now();
    let resp = await handler(req, context);
    const origEnd = performance.now();

    let clonedResp: NextResponse;
    if (resp.body) {
      const [origBody, clonedBody] = resp.body.tee();
      clonedResp = new NextResponse(limit(clonedBody, SUBTRACE_PAYLOAD_LIMIT), resp);
      resp = new NextResponse(origBody, resp);
    } else {
      clonedResp = resp;
    }

    await upload(clonedReq, clonedResp, timestamp.toString(), origEnd - origStart);
    const end = performance.now();

    if (SUBTRACE_DEBUG) {
      console.log("Overall", end - start);
      console.log("Overhead", end - start - (origEnd - origStart));
    }
    return resp;
  };
}

let mutex = 0;
function init(): void {
  // Should only execute at runtime, not build time.
  if (process.env.NEXT_PHASE === PHASE_PRODUCTION_BUILD) {
    return;
  }
  if (mutex++ !== 0) {
    return;
  }

  SUBTRACE_TOKEN = process.env.SUBTRACE_TOKEN ?? "";
  if (!SUBTRACE_TOKEN) {
    throw new Error("Missing SUBTRACE_TOKEN environment variable");
  }

  SUBTRACE_ENDPOINT = process.env.SUBTRACE_ENDPOINT ?? "https://subtrace.dev";
  if (!SUBTRACE_ENDPOINT.startsWith("https://") && !SUBTRACE_ENDPOINT.startsWith("http://")) {
    SUBTRACE_ENDPOINT = "https://" + SUBTRACE_ENDPOINT;
  }

  SUBTRACE_PAYLOAD_LIMIT = process.env.SUBTRACE_PAYLOAD_LIMIT ? parseInt(process.env.SUBTRACE_PAYLOAD_LIMIT) : 4096;

  switch (process.env.SUBTRACE_DEBUG?.toLocaleLowerCase()) {
    case null:
    case undefined:
    case "0":
    case "f":
    case "false":
    case "n":
    case "no":
      SUBTRACE_DEBUG = false;
      break;

    case "1":
    case "t":
    case "true":
    case "y":
    case "yes":
      SUBTRACE_DEBUG = true;
      break;

    default:
      throw new Error(`Unexpected value for SUBTRACE_DEBUG: ${process.env.SUBTRACE_DEBUG}`);
  }

  getWebSocket();
  patchFetch();
}

init();
