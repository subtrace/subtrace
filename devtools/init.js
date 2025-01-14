// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

class Mutex {
  #counter = 1;
  #shared = new Uint8Array(new SharedArrayBuffer(1));

  tryLock() {
    if (Atomics.load(this.#shared, 0) !== 0) {
      return false;
    }
    return Atomics.compareExchange(this.#shared, 0, 0, this.#counter++) == 0;
  }

  unlock() {
    const load = Atomics.load(this.#shared, 0);
    if (load === 0) {
      throw new Error(`error: unlock of unlocked mutex: load=${load}`);
    }

    const cas = Atomics.compareExchange(this.#shared, 0, load, 0);
    if (cas != load) {
      throw new Error(`error: mutex value changed in the middle of an unlock: load=${load}, cas=${cas}`);
    }
  }
}

class Client {
  #conn;
  #promise;

  constructor(onMessage, onClose) {
    let conn = null;
    try {
      conn = new WebSocket(document.location);
      conn.binaryType = "arraybuffer";
    } catch {}
    if (conn === null) {
      onClose();
      return;
    }

    this.#conn = conn;
    this.#conn.onclose = (ev) => onClose(ev);
    this.#conn.onmessage = (ev) => onMessage(ev);
    this.#promise = new Promise((resolve, reject) => {
      this.#conn.onopen = (ev) => resolve(ev);
      this.#conn.onerror = (ev) => reject(ev);
    });
  }

  async wait() {
    await this.#promise;
  }

  close() {
    this.#conn.close();
  }

  send(msg) {
    this.#conn.send(JSON.stringify(msg));
  }
}

class Manager {
  #client = null;

  constructor() {
    this.spawn();
  }

  onMessage(ev) {
    const decoder = new TextDecoder();
    const json = decoder.decode(ev.data);

    const msg = JSON.parse(json);
    console.log(`subtrace: received message id=${msg._id}`);

    const entry = new window.subtrace.HAREntry(msg);
    console.log("entry", entry);

    const request = window.subtrace.NetworkRequest.createWithoutBackendRequest(msg._id, entry.request.url, "", "");
    console.log("request pre-fill", request);

    window.subtrace.Importer.fillRequestFromHAREntry(request, entry, null);
    console.log("request post-fill", request);

    window.subtrace.NetworkLog.instance().addRequest(request);
  }

  onClose(ev) {
    console.log(`subtrace: connection manager: websocket closed`, ev);
    setTimeout(() => this.spawn(), this.#backoff);
  }

  async spawnLocked() {
    if (this.#client !== null) {
      this.#client.close();
      this.#client = null;
    }
    this.#client = new Client((ev) => this.onMessage(ev), (ev) => this.onClose(ev));
    await this.#client.wait();
  }

  #mutex = new Mutex();
  #backoff = 0;

  increaseBackoff() {
    if (this.#backoff == 0) {
      this.#backoff = 100;
    } else if (this.#backoff < 1000) {
      this.#backoff += 100;
    } else {
      this.#backoff += 1000;
    }
    if (this.#backoff > 5000) {
      this.#backoff = 5000;
    }
  }

  async spawn() {
    if (!this.#mutex.tryLock()) {
      console.log("subtrace: connection manager: skipping websocket spawn because mutex lock failed");
      this.increaseBackoff();
      setTimeout(() => this.spawn, this.#backoff);
      return;
    }

    try {
      await this.spawnLocked();
      this.#backoff = 0;
    } catch (err) {
      console.error(`subtrace: connection manager: spawn failed`, err);
      this.increaseBackoff();
      setTimeout(() => this.spawn(), this.#backoff);
    } finally {
      this.#mutex.unlock();
      console.log("subtrace: connection manager: unlocking mutex at the end of spawn");
    }
  }
}

function main() {
  const pane = window.subtrace.InspectorView.instance().tabbedPane;
  pane.closeTabs(pane.otherTabs("network"));

  const leftToolbar = pane.leftToolbar();
  leftToolbar.style.width = 0;
  leftToolbar.style.minWidth = 0;

  const manager = new Manager();
  console.log("subtrace: initializing");
}

function init() {
  let done = false;
  const mutex = new Mutex();
  const observer = new MutationObserver(() => {
    if (!done && mutex.tryLock()) {
      main();
      observer.disconnect();
      done = true;
      mutex.unlock();
    }
  });

  observer.observe(document.body, { childList: true });
}

init();
