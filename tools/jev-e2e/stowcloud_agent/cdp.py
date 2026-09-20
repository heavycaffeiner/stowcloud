"""Chrome DevTools Protocol (CDP) client using websockets with fallback."""

import json
import time
import urllib.request
from typing import Any

from .browser_state import BrowserState, InteractiveElement, ScrollContainer

try:
    from websockets.sync.client import connect as ws_connect
except ImportError:
    ws_connect = None


class ChromeCDP:
    """Chrome DevTools Protocol driver for live browser interaction."""

    def __init__(self, debug_port: int) -> None:
        self.debug_port = debug_port
        self.ws: Any = None
        self._msg_id = 0

    def connect(self) -> None:
        url = f"http://127.0.0.1:{self.debug_port}/json/list"
        with urllib.request.urlopen(url, timeout=5) as resp:
            targets = json.loads(resp.read().decode("utf-8"))

        page_target = next((t for t in targets if t.get("type") == "page"), None)
        if not page_target:
            raise RuntimeError(f"No page target found at {url}: {targets}")

        ws_url = page_target.get("webSocketDebuggerUrl")
        if not ws_url:
            raise RuntimeError("Target has no webSocketDebuggerUrl")

        if ws_connect:
            self.ws = ws_connect(ws_url, max_size=None)
        else:
            raise RuntimeError("websockets library is required for CDP connection")

        self.call("Page.enable")
        self.call("DOM.enable")
        self.call("Runtime.enable")

    def call(self, method: str, params: dict[str, Any] | None = None, timeout: float = 15.0) -> dict[str, Any]:
        if not self.ws:
            self.connect()
        assert self.ws is not None

        self._msg_id += 1
        req_id = self._msg_id
        msg = json.dumps({"id": req_id, "method": method, "params": params or {}})
        self.ws.send(msg)

        start = time.time()
        while time.time() - start < timeout:
            raw = self.ws.recv(timeout=timeout)
            if isinstance(raw, bytes):
                raw = raw.decode("utf-8", errors="replace")
            data = json.loads(raw)
            if data.get("id") == req_id:
                if "error" in data:
                    raise RuntimeError(f"CDP error calling {method}: {data['error']}")
                return data.get("result", {})
        raise TimeoutError(f"Timed out waiting for CDP response to {method} [{req_id}]")

    def set_cookie(self, name: str, value: str, url: str) -> None:
        self.call("Network.enable")
        self.call("Network.setCookie", {
            "name": name,
            "value": value,
            "url": url,
            "path": "/",
            "secure": True,
            "sameSite": "Lax",
        })

    def navigate(self, url: str) -> None:
        self.call("Page.navigate", {"url": url})
        time.sleep(1.0)

    def evaluate(self, expr: str) -> Any:
        res = self.call("Runtime.evaluate", {"expression": expr, "returnByValue": True})
        val = res.get("result", {})
        return val.get("value")

    def click(self, x: int, y: int) -> None:
        self.evaluate(f"""
        (() => {{
            let el = document.elementFromPoint({x}, {y});
            if (el) {{
                const clickable = el.closest('button, a, [role="button"], [role="menuitem"], input, mdui-button') || el;
                clickable.focus();
                clickable.click();
                const shadowBtn = clickable.shadowRoot ? clickable.shadowRoot.querySelector('button, [part="button"]') : null;
                if (shadowBtn) shadowBtn.click();
            }}
        }})()
        """)
        time.sleep(0.5)

    def type_text(self, text: str, x: int = 0, y: int = 0) -> None:
        safe_text = json.dumps(text)
        self.evaluate(f"""
        (() => {{
            let target = document.activeElement;
            if ({x} > 0 && {y} > 0) {{
                target = document.elementFromPoint({x}, {y}) || target;
            }}
            if (target) {{
                if (target.tagName === 'MDUI-TEXT-FIELD') {{
                    target.value = {safe_text};
                    target.dispatchEvent(new Event('input', {{ bubbles: true }}));
                    target.dispatchEvent(new Event('change', {{ bubbles: true }}));
                }}
                const input = target.shadowRoot ? target.shadowRoot.querySelector('input') : target.querySelector ? target.querySelector('input') : null;
                if (input) {{
                    input.value = {safe_text};
                    input.dispatchEvent(new Event('input', {{ bubbles: true }}));
                    input.dispatchEvent(new Event('change', {{ bubbles: true }}));
                }}
            }}
        }})()
        """)
        time.sleep(0.3)

    def press_key(self, key: str) -> None:
        self.evaluate(f"""
        (() => {{
            const active = document.activeElement || document.body;
            active.dispatchEvent(new KeyboardEvent('keydown', {{ key: '{key}', code: '{key}', bubbles: true }}));
            active.dispatchEvent(new KeyboardEvent('keyup', {{ key: '{key}', code: '{key}', bubbles: true }}));
        }})()
        """)
        time.sleep(0.3)

    def set_file_input_files(self, file_path: str) -> None:
        try:
            doc = self.call("DOM.getDocument")
            node = self.call(
                "DOM.querySelector",
                {"nodeId": doc["root"]["nodeId"], "selector": "input[type='file']"},
            )
            if node and node.get("nodeId"):
                self.call(
                    "DOM.setFileInputFiles",
                    {"files": [file_path], "nodeId": node["nodeId"]},
                )
        except Exception:
            pass
        time.sleep(1.0)

    def extract_live_browser_state(self) -> BrowserState:
        """Extract real semantic elements from DOM via injected JavaScript."""
        js_extractor = """
        (() => {
            const elements = [];
            let root = document;
            const dialogs = document.querySelectorAll('mdui-dialog, .sc-browse-dialog, .sc-delete-dialog');
            for (const d of dialogs) {
                if (d.open === true || (d.hasAttribute('open') && d.getAttribute('open') !== 'false' && d.offsetWidth > 0)) {
                    root = d;
                    break;
                }
            }

            const candidates = root.querySelectorAll(
                'button, a, input, select, textarea, [role="button"], [role="menuitem"], [role="tab"], [role="row"], .sc-filename, .sc-file-grid__card, mdui-button, mdui-text-field'
            );

            let id = 1;
            for (const el of candidates) {
                const rect = el.getBoundingClientRect();
                if (rect.width > 0 && rect.height > 0 && rect.top >= 0 && rect.top <= window.innerHeight) {
                    let name = (el.getAttribute('aria-label') || el.innerText || el.getAttribute('placeholder') || '').trim();
                    if (!name && el.tagName === 'MDUI-TEXT-FIELD') {
                        name = el.getAttribute('label') || '';
                    }
                    if (el.classList && el.classList.contains('sc-nav-drawer__new-btn')) {
                        name = '새로 만들기 (New)';
                    }
                    name = name.replace(/\\s+/g, ' ').slice(0, 40);
                    if (name) {
                        elements.push({
                            id: id++,
                            tag: el.tagName.toLowerCase(),
                            role: el.getAttribute('role') || el.tagName.toLowerCase(),
                            name: name,
                            enabled: !el.hasAttribute('disabled'),
                            x: Math.round(rect.left + rect.width / 2),
                            y: Math.round(rect.top + rect.height / 2),
                        });
                    }
                }
            }

            // Include file inputs even if hidden so agent can target them
            const fileInputs = document.querySelectorAll('input[type="file"]');
            for (const el of fileInputs) {
                elements.push({
                    id: id++,
                    tag: 'input',
                    role: 'file',
                    name: el.getAttribute('aria-label') || '파일 업로드 (File Upload)',
                    enabled: true,
                    x: 0,
                    y: 0,
                });
            }

            return {
                url: window.location.href,
                title: document.title,
                route: window.location.pathname,
                elements: elements
            };
        })()
        """
        raw = self.evaluate(js_extractor) or {}
        elems = [
            InteractiveElement(
                id=e["id"],
                tag=e["tag"],
                role=e["role"],
                name=e["name"],
                enabled=e.get("enabled", True),
                selector=f"{e.get('x', 0)},{e.get('y', 0)}",
            )
            for e in raw.get("elements", [])
        ]
        return BrowserState(
            url=raw.get("url", ""),
            title=raw.get("title", ""),
            active_route=raw.get("route", ""),
            elements=elems,
        )

    def close(self) -> None:
        if self.ws:
            try:
                self.ws.close()
            except Exception:
                pass
