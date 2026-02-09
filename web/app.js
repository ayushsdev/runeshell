(function (root) {
  const defaults = {
    mode: 'local',
    agent: 'agent1',
    session: 'ai',
    token: ''
  };

  function parseQuery(qs) {
    const params = new URLSearchParams(qs || '');
    return {
      mode: params.get('mode') || defaults.mode,
      agent: params.get('agent') || defaults.agent,
      session: params.get('session') || defaults.session,
      token: params.get('token') || defaults.token
    };
  }

  function buildWsUrl(opts) {
    const protocol = opts.protocol === 'https:' ? 'wss' : 'ws';
    const tokenQS = opts.token ? `?token=${encodeURIComponent(opts.token)}` : '';
    const path = opts.mode === 'hub' ? `/ws/client${tokenQS}` : `/ws${tokenQS}`;
    return `${protocol}://${opts.host}${path}`;
  }

  function buildShareUrl(locationLike) {
    const origin = locationLike.origin || `${locationLike.protocol}//${locationLike.host}`;
    const path = locationLike.pathname || '/';
    const url = new URL(origin + path);
    const cfg = parseQuery(locationLike.search || '');
    url.searchParams.set('mode', cfg.mode);
    url.searchParams.set('agent', cfg.agent);
    url.searchParams.set('session', cfg.session);
    if (cfg.token) {
      url.searchParams.set('token', cfg.token);
    }
    return url.toString();
  }

  function encodeFrame(sessionId, payload) {
    if (!sessionId) throw new Error('session id required');
    const sid = new TextEncoder().encode(sessionId);
    if (sid.length > 0xffff) throw new Error('session id too long');
    const data = payload instanceof Uint8Array ? payload : new Uint8Array(payload);
    const out = new Uint8Array(2 + sid.length + data.length);
    out[0] = (sid.length >> 8) & 0xff;
    out[1] = sid.length & 0xff;
    out.set(sid, 2);
    out.set(data, 2 + sid.length);
    return out.buffer;
  }

  function decodeFrame(buffer) {
    const data = buffer instanceof Uint8Array ? buffer : new Uint8Array(buffer);
    if (data.length < 2) throw new Error('frame too short');
    const sidLen = (data[0] << 8) | data[1];
    if (data.length < 2 + sidLen) throw new Error('frame missing session id');
    const sid = new TextDecoder().decode(data.slice(2, 2 + sidLen));
    const payload = data.slice(2 + sidLen);
    if (!sid) throw new Error('session id required');
    return { sessionId: sid, payload };
  }

  function getBackoffMs(attempt, base, max) {
    const b = base || 500;
    const m = max || 10000;
    const pow = Math.pow(2, Math.max(0, attempt));
    return Math.min(b * pow, m);
  }

  function applySessionsSync(current, incoming) {
    const curr = new Set(current || []);
    const next = new Set(incoming || []);
    const added = [];
    const removed = [];
    for (const s of next) {
      if (!curr.has(s)) added.push(s);
    }
    for (const s of curr) {
      if (!next.has(s)) removed.push(s);
    }
    return { added, removed, sessions: Array.from(next) };
  }

  function applyCtrl(ch) {
    if (!ch || ch.length !== 1) return null;
    const code = ch.charCodeAt(0);
    if (code >= 97 && code <= 122) {
      return String.fromCharCode(code - 96);
    }
    if (code >= 65 && code <= 90) {
      return String.fromCharCode(code - 64);
    }
    if (code >= 64 && code <= 95) {
      return String.fromCharCode(code - 64);
    }
    return null;
  }

  function applyShift(ch) {
    if (!ch || ch.length !== 1) return null;
    const map = {
      '1': '!',
      '2': '@',
      '3': '#',
      '4': '$',
      '5': '%',
      '6': '^',
      '7': '&',
      '8': '*',
      '9': '(',
      '0': ')',
      '-': '_',
      '=': '+',
      '[': '{',
      ']': '}',
      ';': ':',
      "'": '"',
      ',': '<',
      '.': '>',
      '/': '?',
      '\\': '|',
      '`': '~'
    };
    if (map[ch]) return map[ch];
    const code = ch.charCodeAt(0);
    if (code >= 97 && code <= 122) {
      return ch.toUpperCase();
    }
    return ch;
  }

  function transformInput(data, ctrlActive, shiftActive) {
    if (ctrlActive) {
      if (data.length === 1) {
        const mapped = applyCtrl(data);
        if (mapped) {
          return { data: mapped, usedCtrl: true, usedShift: false };
        }
      }
      return { data, usedCtrl: true, usedShift: false };
    }
    if (shiftActive && data.length === 1) {
      return { data: applyShift(data), usedCtrl: false, usedShift: true };
    }
    if (!ctrlActive) return { data, usedCtrl: false, usedShift: false };
    if (data.length === 1) {
      const mapped = applyCtrl(data);
      if (mapped) {
        return { data: mapped, usedCtrl: true, usedShift: false };
      }
    }
    return { data, usedCtrl: true, usedShift: false };
  }

  function isIOS() {
    if (typeof navigator === 'undefined') return false;
    const ua = navigator.userAgent || '';
    return /iPad|iPhone|iPod/.test(ua) && !window.MSStream;
  }

  function start() {
    const statusEl = document.getElementById('status');
    let tabsEl = document.getElementById('tabs');
    let toolbarEl = document.getElementById('toolbar');
    let roleBanner = document.getElementById('role-banner');
    const qrOverlay = document.getElementById('qr-overlay');
    const qrImage = document.getElementById('qr-image');
    const qrUrl = document.getElementById('qr-url');
    const qrClose = document.getElementById('qr-close');
    if (!window.Terminal || !window.FitAddon) {
      statusEl.textContent = 'Failed to load terminal assets.';
      return;
    }

    const termHost = document.getElementById('terminal');
    if (!tabsEl) {
      tabsEl = document.createElement('div');
      tabsEl.id = 'tabs';
      if (termHost && termHost.parentNode) {
        termHost.parentNode.insertBefore(tabsEl, termHost);
      } else {
        document.body.insertBefore(tabsEl, document.body.firstChild);
      }
    }
    tabsEl.style.display = 'flex';
    if (!toolbarEl) {
      toolbarEl = document.createElement('div');
      toolbarEl.id = 'toolbar';
      document.body.appendChild(toolbarEl);
    }
    if (!roleBanner) {
      roleBanner = document.createElement('div');
      roleBanner.id = 'role-banner';
      document.body.appendChild(roleBanner);
    }
    const terminals = new Map();
    let activeSession = null;

    const cfg = parseQuery(window.location.search);
    const wsUrl = buildWsUrl({
      mode: cfg.mode,
      host: window.location.host,
      protocol: window.location.protocol,
      token: cfg.token
    });
    const multiplex = cfg.mode === 'hub';

    let ws = null;
    let reconnectTimer = null;
    let reconnectAttempt = 0;
    let ctrlActive = false;
    let shiftActive = false;
    let agentOffline = false;
    let reconnectPaused = false;
    let isWriter = false;
    let lastWriteRequest = 0;
    let takeControlBtn = null;
    let releaseControlBtn = null;

    function setStatus(text) {
      statusEl.textContent = text;
    }

    function updateRoleBanner() {
      if (!roleBanner) return;
      roleBanner.textContent = isWriter ? 'Writer' : 'View-only';
      roleBanner.classList.toggle('writer', isWriter);
      roleBanner.classList.toggle('viewer', !isWriter);
      roleBanner.style.display = ws && ws.readyState === WebSocket.OPEN ? 'block' : 'none';
    }

    function setWriterState(write) {
      isWriter = !!write;
      updateRoleBanner();
      if (takeControlBtn) takeControlBtn.style.display = isWriter ? 'none' : '';
      if (releaseControlBtn) releaseControlBtn.style.display = isWriter ? '' : 'none';
    }

    function setCtrlActive(active) {
      ctrlActive = active;
      if (toolbarEl) {
        const btn = toolbarEl.querySelector('[data-key="ctrl"]');
        if (btn) {
          btn.classList.toggle('active', active);
          btn.setAttribute('aria-pressed', active ? 'true' : 'false');
        }
      }
    }

    function setShiftActive(active) {
      shiftActive = active;
      if (toolbarEl) {
        const btn = toolbarEl.querySelector('[data-key="shift"]');
        if (btn) {
          btn.classList.toggle('active', active);
          btn.setAttribute('aria-pressed', active ? 'true' : 'false');
        }
      }
    }

    function sendInput(data) {
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      if (!isWriter) return;
      ws.send(data);
    }

    function sendResize() {
      const entry = terminals.get(activeSession);
      if (!entry) return;
      entry.fit.fit();
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      const cols = entry.term.cols;
      const rows = entry.term.rows;
      const payload = { type: 'resize', cols, rows };
      if (multiplex) payload.session_id = activeSession;
      ws.send(JSON.stringify(payload));
    }

    let resizeTimer = null;
    function scheduleResize() {
      if (resizeTimer) clearTimeout(resizeTimer);
      resizeTimer = setTimeout(sendResize, 60);
    }

    function connect() {
      setStatus('Connecting');
      ws = new WebSocket(wsUrl);
      ws.binaryType = 'arraybuffer';

      ws.addEventListener('open', () => {
        agentOffline = false;
        reconnectPaused = false;
        reconnectAttempt = 0;
        const sessions = Array.from(terminals.keys());
        if (sessions.length === 0) addSession(cfg.session);
        const first = Array.from(terminals.keys())[0];
        const attach = { type: 'attach', session_id: first, protocol_version: multiplex ? 2 : 1 };
        if (cfg.mode === 'hub') attach.agent_id = cfg.agent;
        ws.send(JSON.stringify(attach));
        if (multiplex) {
          for (const id of Array.from(terminals.keys())) {
            if (id === first) continue;
            ws.send(JSON.stringify({ type: 'attach', session_id: id }));
          }
        }
        if (!activeSession) setActive(first);
        if (multiplex) {
          ws.send(JSON.stringify({ type: 'active', session_id: activeSession }));
        }
        sendResize();
        setStatus(`Connected (${multiplex ? 'hub' : 'local'})`);
        setWriterState(false);
        sendFocusState();
      });

      ws.addEventListener('message', (event) => {
        if (typeof event.data === 'string') {
          try {
            const msg = JSON.parse(event.data);
            if (msg.type === 'error') {
              if (msg.code === 'agent_offline') {
                agentOffline = true;
                setStatus('Waiting for agent…');
                return;
              }
              if (msg.code === 'busy') {
                reconnectPaused = true;
                setStatus('Another client is already connected.');
                return;
              }
              if (msg.code === 'not_authorized') {
                reconnectPaused = true;
                setStatus('Not authorized.');
                return;
              }
              if (msg.message) {
                setStatus(msg.message);
              }
              return;
            }
            if (msg.type === 'write_status') {
              setWriterState(!!msg.write);
              return;
            }
            if (msg.type === 'sessions_sync') {
              const current = Array.from(terminals.keys());
              const sync = applySessionsSync(current, Array.isArray(msg.sessions) ? msg.sessions : []);
              sync.removed.forEach((id) => closeSession(id, false));
              sync.added.forEach(addSession);
              if (activeSession && !terminals.has(activeSession)) {
                const next = terminals.keys().next();
                if (!next.done) {
                  setActive(next.value);
                } else {
                  activeSession = null;
                  setStatus('No sessions');
                }
              }
              return;
            }
            if (msg.type === 'write_denied') {
              if (msg.message) {
                setStatus(msg.message);
              } else if (msg.code === 'another_writer') {
                setStatus('Another client is already connected.');
              } else if (msg.code === 'not_authorized') {
                setStatus('Not authorized.');
              } else {
                setStatus('Write request denied.');
              }
              return;
            }
            if (msg.type === 'attached') {
              setStatus(`Connected (${multiplex ? 'hub' : 'local'})`);
            }
          } catch {
            // ignore
          }
          return;
        }
        if (!multiplex) {
          const entry = terminals.get(activeSession);
          if (entry) entry.term.write(new Uint8Array(event.data));
          return;
        }
        try {
          const { sessionId, payload } = decodeFrame(event.data);
          const entry = terminals.get(sessionId);
          if (entry) {
            entry.term.write(payload);
          }
        } catch {
          // ignore invalid frames
        }
      });

      ws.addEventListener('close', () => {
        if (!agentOffline) {
          setStatus('Disconnected');
        }
        updateRoleBanner();
        scheduleReconnect();
      });

      ws.addEventListener('error', () => {
        setStatus('Connection error');
      });
    }

    function scheduleReconnect() {
      if (reconnectTimer || reconnectPaused) return;
      const base = agentOffline ? 1000 : 500;
      const delay = getBackoffMs(reconnectAttempt, base, 10000);
      reconnectAttempt += 1;
      if (!agentOffline) {
        setStatus(`Reconnecting in ${Math.round(delay / 1000)}s`);
      }
      reconnectTimer = setTimeout(() => {
        reconnectTimer = null;
        connect();
      }, delay);
    }

    function sendFocusState() {
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      const active = document.visibilityState !== 'hidden';
      ws.send(JSON.stringify({ type: 'focus', state: active ? 'on' : 'off' }));
    }

    function requestWrite(reason) {
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      if (isWriter) return;
      const now = Date.now();
      if (now-lastWriteRequest < 1000) return;
      lastWriteRequest = now;
      ws.send(JSON.stringify({ type: 'request_write', reason }));
    }

    function releaseWrite() {
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      if (!isWriter) return;
      ws.send(JSON.stringify({ type: 'release_write' }));
    }

    function handleInput(sessionId, data) {
      if (sessionId !== activeSession) return;
      if (!isWriter) return;
      const res = transformInput(data, ctrlActive, shiftActive);
      if (res.usedCtrl) setCtrlActive(false);
      if (res.usedShift) setShiftActive(false);
      if (res.data && res.data.length > 0) {
        const bytes = new TextEncoder().encode(res.data);
        const frame = multiplex ? encodeFrame(sessionId, bytes) : bytes;
        sendInput(frame);
      }
    }

    function addSession(sessionId) {
      if (!sessionId || terminals.has(sessionId)) return;
      const pane = document.createElement('div');
      pane.className = 'term-pane';
      pane.style.display = 'none';
      termHost.appendChild(pane);

      const term = new Terminal({
        cursorBlink: true,
        fontFamily: "Menlo, Monaco, Consolas, 'Courier New', monospace",
        fontSize: 14,
        theme: { background: '#0b0e11' }
      });
      const fit = new FitAddon.FitAddon();
      term.loadAddon(fit);
      term.open(pane);
      fit.fit();

      term.onData((data) => handleInput(sessionId, data));

      const tab = document.createElement('div');
      tab.className = 'tab-item';
      const tabName = document.createElement('button');
      tabName.className = 'tab-name';
      tabName.textContent = sessionId;
      tabName.addEventListener('click', () => setActive(sessionId));
      tab.appendChild(tabName);

      const tabClose = document.createElement('button');
      tabClose.className = 'tab-close';
      tabClose.type = 'button';
      tabClose.setAttribute('aria-label', `Close ${sessionId}`);
      tabClose.textContent = '×';
      tabClose.addEventListener('click', (event) => {
        event.stopPropagation();
        closeSession(sessionId);
      });
      tab.appendChild(tabClose);

      if (tabsEl) tabsEl.appendChild(tab);

      terminals.set(sessionId, { term, fit, pane, tab, tabName, tabClose });

      if (!activeSession) {
        setActive(sessionId);
      }
    }

    function setActive(sessionId) {
      if (!terminals.has(sessionId)) return;
      activeSession = sessionId;
      for (const [id, entry] of terminals.entries()) {
        entry.pane.style.display = id === sessionId ? 'block' : 'none';
        if (entry.tab) entry.tab.classList.toggle('active', id === sessionId);
      }
      const entry = terminals.get(sessionId);
      entry.term.focus();
      if (entry.term.textarea) entry.term.textarea.focus();
      if (ws && ws.readyState === WebSocket.OPEN) {
        if (multiplex) {
          ws.send(JSON.stringify({ type: 'active', session_id: sessionId }));
        }
        sendResize();
      }
    }

    function closeSession(sessionId, notify) {
      const entry = terminals.get(sessionId);
      if (!entry) return;
      if (notify !== false && ws && ws.readyState === WebSocket.OPEN && multiplex) {
        ws.send(JSON.stringify({ type: 'detach', session_id: sessionId }));
      }
      if (entry.pane && entry.pane.parentNode) entry.pane.parentNode.removeChild(entry.pane);
      if (entry.tab && entry.tab.parentNode) entry.tab.parentNode.removeChild(entry.tab);
      terminals.delete(sessionId);
      if (activeSession === sessionId) {
        const next = terminals.keys().next();
        if (!next.done) {
          setActive(next.value);
        } else {
          activeSession = null;
          setStatus('No sessions');
        }
      }
    }

    function addButton(label, title, handler, key) {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.textContent = label;
      btn.title = title;
      if (key) btn.dataset.key = key;
      btn.addEventListener('click', handler);
      toolbarEl.appendChild(btn);
      return btn;
    }

    function promptSession() {
      const name = prompt('Session name');
      if (!name) return;
      if (terminals.has(name)) {
        setActive(name);
        return;
      }
      addSession(name);
      if (ws && ws.readyState === WebSocket.OPEN && multiplex) {
        ws.send(JSON.stringify({ type: 'attach', session_id: name }));
      }
      setActive(name);
    }

    function showQR() {
      if (!qrOverlay || !qrImage || !qrUrl) return;
      if (typeof qrcode !== 'function') {
        statusEl.textContent = 'QR generator not available.';
        return;
      }
      const url = buildShareUrl(window.location);
      qrUrl.textContent = url;
      qrImage.innerHTML = '';
      const qr = qrcode(0, 'M');
      qr.addData(url);
      qr.make();
      qrImage.innerHTML = qr.createImgTag(6, 8);
      qrOverlay.classList.add('active');
    }

    function hideQR() {
      if (qrOverlay) qrOverlay.classList.remove('active');
    }

    function sendSeq(seq) {
      if (!activeSession) return;
      if (!isWriter) return;
      const bytes = new TextEncoder().encode(seq);
      const frame = multiplex ? encodeFrame(activeSession, bytes) : bytes;
      sendInput(frame);
    }

    function arrowSeq(dir) {
      if (shiftActive) {
        setShiftActive(false);
        return `\x1b[1;2${dir}`;
      }
      return `\x1b[${dir}`;
    }

    if (toolbarEl) {
      if (multiplex) {
        takeControlBtn = addButton('Take control', 'Request write access', () => requestWrite('manual'), 'take');
        releaseControlBtn = addButton('Release', 'Release write access', releaseWrite, 'release');
        releaseControlBtn.style.display = 'none';
      }
      addButton('Ctrl', 'Toggle Ctrl modifier', () => setCtrlActive(!ctrlActive), 'ctrl');
      addButton('Shift', 'Toggle Shift modifier', () => setShiftActive(!shiftActive), 'shift');
      addButton('Esc', 'Escape', () => sendSeq('\x1b'));
      addButton('Tab', 'Tab (Shift reverses)', () => {
        if (shiftActive) {
          setShiftActive(false);
          sendSeq('\x1b[Z');
        } else {
          sendSeq('\t');
        }
      });
      addButton('↑', 'Arrow Up', () => sendSeq(arrowSeq('A')));
      addButton('↓', 'Arrow Down', () => sendSeq(arrowSeq('B')));
      addButton('←', 'Arrow Left', () => sendSeq(arrowSeq('D')));
      addButton('→', 'Arrow Right', () => sendSeq(arrowSeq('C')));
      addButton('QR', 'Show QR for this URL', showQR, 'qr');

      if (isIOS()) {
        addButton('Focus', 'Focus input', () => {
          const entry = terminals.get(activeSession);
          if (!entry) return;
          entry.term.focus();
          if (entry.term.textarea) entry.term.textarea.focus();
        });
      }
    }

    window.addEventListener('resize', scheduleResize);
    window.addEventListener('orientationchange', scheduleResize);
    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', scheduleResize);
    }
    window.addEventListener('focus', () => {
      sendFocusState();
      if (multiplex) requestWrite('focus');
    });
    window.addEventListener('blur', sendFocusState);
    document.addEventListener('visibilitychange', sendFocusState);
    if (qrClose) qrClose.addEventListener('click', hideQR);
    if (qrOverlay) {
      qrOverlay.addEventListener('click', (event) => {
        if (event.target === qrOverlay) hideQR();
      });
    }

    async function initSessions() {
      let list = [];
      if (cfg.mode === 'hub') {
        try {
          const resp = await fetch(`/api/sessions?agent_id=${encodeURIComponent(cfg.agent)}`);
          if (resp.ok) {
            const data = await resp.json();
            if (Array.isArray(data.sessions)) {
              list = data.sessions;
            }
          }
        } catch {
          // ignore
        }
      }
      if (list.length === 0) {
        list = [cfg.session];
      }
      list.forEach(addSession);

      if (tabsEl) {
        const addBtn = document.createElement('button');
        addBtn.textContent = '+';
        addBtn.title = 'New session';
        addBtn.className = 'tab-add';
        addBtn.addEventListener('click', promptSession);
        if (multiplex) tabsEl.appendChild(addBtn);
        if (!multiplex) tabsEl.style.display = 'none';
      }
    }

    initSessions().then(() => {
      connect();
      scheduleResize();
    });
  }

  const api = {
    parseQuery,
    buildWsUrl,
    buildShareUrl,
    applySessionsSync,
    encodeFrame,
    decodeFrame,
    getBackoffMs,
    applyCtrl,
    applyShift,
    transformInput,
    start
  };

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  } else {
    root.RuneshellApp = api;
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', start);
    } else {
      start();
    }
  }
})(typeof window !== 'undefined' ? window : globalThis);
