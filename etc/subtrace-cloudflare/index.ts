import * as pubsub from "./pubsub.js";

import { waitUntil } from "cloudflare:workers";

let SUBTRACE_TOKEN: string | null = null;

const originalFetch = globalThis.fetch;

async function publishEvent(event: any): Promise<void> {
	if (SUBTRACE_TOKEN === null) {
		// TODO: probably not the right thing to do?
		console.log(JSON.stringify(event, null, 2));
		return;
	}

  const resp = await originalFetch(`https://subtrace.dev/api/PublishEvents`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${SUBTRACE_TOKEN}`
    },
    body: JSON.stringify(pubsub.PublishEvents_Request.toJSON({
			events: [
				{
					concreteV1: {
						harEntryJson: (new TextEncoder()).encode(JSON.stringify(event["http"])),
						tags: event["tags"],
						log: undefined,
					},
				},
			],
		})),
  });
  if (resp.status != 200) {
    let err = `subtrace: failed to publish events: status ${resp.status}: ${resp.statusText}`
    const msg = await resp.text();
    if (msg != "") {
      err = `${err}: ${msg}`
    }
    console.error(err);
    return;
  }
}

function newEvent(): any {
	const eventId = crypto.randomUUID();
	const startDate = new Date();

	return {
		"tags": {
			"event_id": eventId,
			"time": startDate.toISOString(),
		},
		"http": {
			"_id": eventId,
			"startedDateTime": startDate.toISOString(),
			"time": 0,
			"request": {
				"method": "",
				"url": "",
				"httpVersion": "HTTP/1.0",
				"cookies": [],
				"headers": [],
				"queryString": [],
				"postData": {
					"mimeType": "",
					"params": null,
					"text": ""
				},
				"headersSize": -1,
				"bodySize": 0,
			},
			"response": {
				"status": 0,
				"statusText": "",
				"httpVersion": "HTTP/1.0",
				"cookies": [],
				"headers": [
				],
				"content": {
					"size": 0,
					"mimeType": "",
					"text": "",
					"encoding": "base64"
				},
				"redirectURL": "",
				"headersSize": -1,
				"bodySize": 0,
			},
			"cache": null,
			"timings": {
				"send": 0,
				"wait": 0,
				"receive": 0
			},
			"_webSocketMessages": null
		}
	};
}

function patchFetch() {
	if (!globalThis.fetch) {
    return;
  }

  globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit<RequestInitCfProperties>): Promise<Response> => {
		const event: any = newEvent();

		for (const [key, val] of Object.entries(init?.cf ?? {})) {
			switch (key) {
			default:
				(event["tags"] as any)[`request.cf.${key}`] = (val as any).toString();
			}
		}

	  let request = new Request(input, init);
    let requestClone: Request;
    if (request.body) {
      const [origBody, clonedBody] = request.body.tee();
      requestClone = new Request(request, { body: clonedBody, duplex: "half" } as RequestInit);
      request = new Request(request, { body: origBody, duplex: "half" } as RequestInit);
    } else {
      requestClone = request;
    }
		const requestBytes = new Promise<Uint8Array>((resolve, reject) => {
			requestClone.bytes().then(bytes => resolve(bytes)).catch(reason => reject(reason));
		});

		event["http"]["request"]["method"] = request.method;
		event["http"]["request"]["url"] = request.url;
		for (const [name, value] of request.headers.entries()) {
			event["http"]["request"]["headers"].push({ name, value });
		}

    const timestamp = new Date();
    const startMilli = performance.now();
		let response: Response | null = null;
		let err: unknown;
		try {
			response = await originalFetch(input, init);
		} catch (e) {
			err = e;
		}
    const endMilli = performance.now();
		event["http"]["time"] = endMilli - startMilli;

		if (response === null) {
			event["http"]["response"]["status"] = 500;
			event["http"]["response"]["statusText"] = "Internal Server Error";
			if (err instanceof Error) {
				event["http"]["response"]["_error"] = err.message;
				if (err.stack) {
					event["tags"]["exception.stack"] = err.stack;
				};
			} else {
				try {
					event["http"]["response"]["_error"] = (err as any).toString();
				} catch (e) {
					event["http"]["response"]["_error"] = JSON.stringify(err);
				}
			}
			waitUntil(publishEvent(event));
		} else {
			event["http"]["response"]["status"] = response.status;
			event["http"]["response"]["statusText"] = response.statusText;
			event["http"]["response"]["redirectURL"] = response.url;
			for (const [name, value] of response.headers.entries()) {
				event["http"]["response"]["headers"].push({ name, value });
			}
			for (const [key, val] of Object.entries(response.cf ?? {})) {
				switch (key) {
				default:
					(event["tags"] as any)[`response.cf.${key}`] = (val as any).toString();
				}
			}
			if (response.headers.has("content-type")) {
				event["http"]["response"]["content"]["mimeType"] = response.headers.get("content-type") ?? "";
			}

			let responseClone: Response;
			if (response.body) {
				const [origBody, clonedBody] = response.body.tee();
				responseClone = new Response(clonedBody, response);
				response = new Response(origBody, response);
			} else {
				responseClone = response;
			}
			const responseBytes = new Promise<Uint8Array>((resolve, reject) => {
				responseClone.bytes().then(bytes => resolve(bytes)).catch(reason => reject(reason));
			});

			waitUntil(Promise.all([requestBytes, responseBytes]).then(([requestBytes, responseBytes]) => {
				event["http"]["request"]["postData"]["text"] = btoa((new TextDecoder('utf8')).decode(requestBytes));
				event["http"]["response"]["content"]["size"] = responseBytes.length;
				event["http"]["response"]["content"]["text"] = btoa((new TextDecoder('utf8')).decode(responseBytes));
				return publishEvent(event);
			}));
		}

		if (response !== null) {
			return response;
		} else {
			throw err;
		}
	};
}

export function subtrace<T>(handler: ExportedHandlerFetchHandler<T, any>): ExportedHandlerFetchHandler<T, any> {
	return async (request, env, ctx): Promise<Response> => {
		if ("SUBTRACE_TOKEN" in (env as any) && SUBTRACE_TOKEN === null) {
			SUBTRACE_TOKEN = (env as any).SUBTRACE_TOKEN; // TODO: toctou race but who cares
		}

		const event: any = newEvent();

		for (const [key, val] of Object.entries(request.cf ?? {})) {
			switch (key) {
			case "tlsClientAuth":
				break;
			case "tlsClientRandom":
				break;
			case "tlsExportedAuthenticator":
				break;
			default:
				(event["tags"] as any)[`request.cf.${key}`] = (val as any).toString();
			}
		}
		event["http"]["request"]["method"] = request.method;
		event["http"]["request"]["url"] = request.url;
		for (const [name, value] of request.headers.entries()) {
			event["http"]["request"]["headers"].push({ name, value });
		}

		const requestClone = request.clone();
		const requestBytes = new Promise<Uint8Array>((resolve, reject) => {
			requestClone.bytes().then(bytes => resolve(bytes)).catch(reason => reject(reason));
		});

		const startMilli = performance.now();
		let response: Response | null = null, err: unknown = null;
		try {
			response = await handler(request, env, ctx);
		} catch (e) {
			err = e;
		}
		const endMilli = performance.now();
		event["http"]["time"] = endMilli - startMilli;

		if (response === null) {
			event["http"]["response"]["status"] = 500;
			event["http"]["response"]["statusText"] = "Internal Server Error";
			if (err instanceof Error) {
				event["http"]["response"]["_error"] = err.message;
				if (err.stack) {
					event["tags"]["exception.stack"] = err.stack;
				};
			} else {
				try {
					event["http"]["response"]["_error"] = (err as any).toString();
				} catch (e) {
					event["http"]["response"]["_error"] = JSON.stringify(err);
				}
			}
			ctx.waitUntil(publishEvent(event));
		} else {
			event["http"]["response"]["status"] = response.status;
			event["http"]["response"]["statusText"] = response.statusText;
			event["http"]["response"]["redirectURL"] = response.url;
			for (const [name, value] of response.headers.entries()) {
				event["http"]["response"]["headers"].push({ name, value });
			}
			for (const [key, val] of Object.entries(response.cf ?? {})) {
				switch (key) {
				default:
					(event["tags"] as any)[`response.cf.${key}`] = (val as any).toString();
				}
			}
			if (response.headers.has("content-type")) {
				event["http"]["response"]["content"]["mimeType"] = response.headers.get("content-type") ?? "";
			}

			const responseClone = response.clone();
			const responseBytes = new Promise<Uint8Array>((resolve, reject) => {
				responseClone.bytes().then(bytes => resolve(bytes)).catch(reason => reject(reason));
			});

			ctx.waitUntil(Promise.all([requestBytes, responseBytes]).then(([requestBytes, responseBytes]) => {
				event["http"]["request"]["postData"]["text"] = btoa((new TextDecoder('utf8')).decode(requestBytes));
				event["http"]["response"]["content"]["size"] = responseBytes.length;
				event["http"]["response"]["content"]["text"] = btoa((new TextDecoder('utf8')).decode(responseBytes));
				return publishEvent(event);
			}));
		}

		if (response !== null) {
			return response;
		} else {
			throw err;
		}
	};
}

let mutex = 0;
function init() {
	if (mutex++ > 0) {
		return;
	}

	patchFetch();
}

init();
