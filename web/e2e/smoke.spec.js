const { test, expect } = require('@playwright/test');

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    class FakeTerminal {
      constructor() {
        this.cols = 80;
        this.rows = 24;
        this.textarea = { focus() {} };
      }
      loadAddon() {}
      open() {}
      write() {}
      onData(cb) {
        this._onData = cb;
      }
      focus() {}
    }

    class FakeFitAddon {
      fit() {}
    }

    class FakeWebSocket {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSING = 2;
      static CLOSED = 3;

      constructor(url) {
        this.url = url;
        this.readyState = FakeWebSocket.CONNECTING;
        this.binaryType = 'arraybuffer';
        this._listeners = { open: [], message: [], close: [], error: [] };
        this._writer = false;
        window.__lastWebSocket = this;
        setTimeout(() => {
          this.readyState = FakeWebSocket.OPEN;
          this._emit('open', {});
        }, 0);
      }

      addEventListener(type, handler) {
        if (!this._listeners[type]) this._listeners[type] = [];
        this._listeners[type].push(handler);
      }

      send(data) {
        if (typeof data === 'string') {
          let msg = null;
          try {
            msg = JSON.parse(data);
          } catch {
            msg = null;
          }
          if (msg && msg.type === 'attach') {
            this._emitMessage(JSON.stringify({ type: 'attached', status: 'ok' }));
            this._emitMessage(JSON.stringify({ type: 'write_status', write: false }));
            this._emitMessage(JSON.stringify({ type: 'sessions_sync', sessions: ['ai', 'ops'] }));
          } else if (msg && msg.type === 'request_write') {
            this._writer = true;
            this._emitMessage(JSON.stringify({ type: 'write_status', write: true }));
          } else if (msg && msg.type === 'release_write') {
            this._writer = false;
            this._emitMessage(JSON.stringify({ type: 'write_status', write: false }));
          }
          return;
        }
        if (!this._writer) return;
        if (data instanceof ArrayBuffer) {
          this._emit('message', { data });
        } else if (ArrayBuffer.isView(data)) {
          this._emit('message', { data: data.buffer });
        }
      }

      close() {
        if (this.readyState === FakeWebSocket.CLOSED) return;
        this.readyState = FakeWebSocket.CLOSED;
        this._emit('close', {});
      }

      _emitMessage(data) {
        this._emit('message', { data });
      }

      _emit(type, payload) {
        const list = this._listeners[type] || [];
        for (const handler of list) {
          handler(payload);
        }
      }
    }

    window.Terminal = FakeTerminal;
    window.FitAddon = { FitAddon: FakeFitAddon };
    window.WebSocket = FakeWebSocket;
    window.fetch = async () =>
      new Response(JSON.stringify({ sessions: ['ai', 'ops'] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
  });
});

test('local mode connects and renders toolbar', async ({ page }) => {
  await page.goto('/?mode=local&session=ai');
  await expect(page.locator('#status')).toHaveText(/Connected \(local\)/);
  await expect(page.locator('#toolbar')).toBeVisible();
});

test('hub mode supports taking control and updates role banner', async ({ page }) => {
  await page.goto('/?mode=hub&agent=agent1&session=ai');
  await expect(page.locator('#status')).toHaveText(/Connected \(hub\)/);
  await page.getByRole('button', { name: 'Take control' }).click();
  await expect(page.locator('#role-banner')).toHaveText('Writer');
  await expect(page.locator('.tab-name')).toHaveCount(2);
});
