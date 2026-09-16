#!/usr/bin/env python3
"""Verify public multi-user endpoints using the operator's existing account.

Creates and revokes temporary grants. Prints statuses/counts, never credentials
or Telegram contents. --check-native starts an unauthenticated native login;
it expires after ten minutes or is cleaned up on API shutdown.
"""
import base64
import hashlib
import html
import http.cookiejar
import json
from pathlib import Path
import re
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
ENV = dict(line.split("=", 1) for line in (ROOT / ".env").read_text().splitlines()
           if "=" in line and not line.startswith("#"))
BASE = ENV.get("PUBLIC_URL", "http://127.0.0.1:8086").rstrip("/")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


class Browser:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            NoRedirect(), urllib.request.HTTPCookieProcessor(self.jar))

    def request(self, path, body=None, *, token="", csrf="", form=False, expect=200):
        headers = {"User-Agent": "telegram-gateway-verification/1.0",
                   "Accept": "application/json, text/event-stream"}
        if token:
            headers["Authorization"] = "Bearer " + token
        if csrf:
            headers["X-CSRF-Token"] = csrf
        data = None
        if body is not None:
            data = (urllib.parse.urlencode(body) if form else json.dumps(body)).encode()
            headers["Content-Type"] = ("application/x-www-form-urlencoded" if form
                                       else "application/json")
        req = urllib.request.Request(BASE + path, data=data, headers=headers)
        try:
            res = self.opener.open(req, timeout=30)
        except urllib.error.HTTPError as err:
            res = err
        with res:
            raw = res.read()
            assert res.status == expect, (path.split("?")[0], res.status, expect)
            if "application/json" in res.headers.get("Content-Type", ""):
                value = json.loads(raw)
            elif "text/" in res.headers.get("Content-Type", ""):
                value = raw.decode()
            else:
                value = raw
            return value, res.headers


def field(page, attribute, name):
    match = re.search(attribute + '="' + name + '"[^>]*value="([^"]+)"', page)
    assert match is not None, "missing form field"
    return html.unescape(match.group(1))


def main():
    anonymous, browser = Browser(), Browser()
    anonymous.request("/health/ready")
    page, _ = anonymous.request("/login")
    assert ENV["GATEWAY_ADMIN_TOKEN"] not in page
    anonymous.request("/account", expect=303)
    _, headers = anonymous.request("/mcp", expect=401)
    assert "resource_metadata=" in headers["WWW-Authenticate"]
    print("Public login, protected account page and MCP discovery: passed", flush=True)

    account = browser.request("/admin/accounts", token=ENV["GATEWAY_ADMIN_TOKEN"])[0]["accounts"][0]
    assert account["status"] == "active" and account["authorization"]["state"] == "authorizationStateReady"
    aid = account["id"]
    # This bridge is exclusively for the already authenticated operator.
    browser.request("/admin/accounts/" + aid + "/browser-session", {}, token=ENV["GATEWAY_ADMIN_TOKEN"])
    secure = BASE.startswith("https://")
    cookie_name = "__Host-tgw_session" if secure else "tgw_session"
    cookies = [c for c in browser.jar if c.name == cookie_name]
    assert len(cookies) == 1 and cookies[0].secure == secure and cookies[0].has_nonstandard_attr("HttpOnly")
    data = browser.request("/account/data")[0]
    assert data["account_id"] == aid
    csrf = data["csrf"]
    browser.request("/admin/accounts", expect=401)
    browser.request("/account/token", {}, expect=403)

    grants = []
    oauth_name = "Temporary public OAuth verification " + secrets.token_hex(5)
    try:
        personal = browser.request("/account/token", {"name": "Temporary public token verification"},
                                   csrf=csrf, expect=201)[0]
        grants.append(personal["client_id"])
        token = personal["token"]
        assert browser.request("/v1/profile", token=token)[0]["data"]["account_id"] == aid
        assert browser.request("/v1/chats?limit=1", token=token)[0]["data"]
        initialized = browser.request("/mcp", {"jsonrpc": "2.0", "id": 1, "method": "initialize",
            "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                       "clientInfo": {"name": "verification", "version": "1.0"}}}, token=token)[0]
        assert initialized["result"]["serverInfo"]
        tools = browser.request("/mcp", {"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, token=token)[0]
        assert len(tools["result"]["tools"]) == 10
        result = browser.request("/mcp", {"jsonrpc": "2.0", "id": 3, "method": "tools/call",
            "params": {"name": "list_chats", "arguments": {"limit": 1}}}, token=token)[0]
        assert not result["result"].get("isError") and result["result"]["structuredContent"]["count"] == 1

        row = subprocess.check_output(["podman", "exec", "tgw-postgres", "psql", "-U", "gateway_owner",
            "-d", "telegram_gateway", "-Atc", "SELECT id::text||'|'||file_size::text||'|'||encode(sha256,'hex') "
            "FROM active_message_media WHERE account_id='" + aid + "'::uuid AND download_status='ready' "
            "AND file_size<=2097152 ORDER BY downloaded_at DESC LIMIT 1"], text=True).strip()
        media_id, size, digest = row.split("|")
        link = browser.request("/v1/media/" + media_id + "/url", token=token)[0]["data"]["url"]
        download_path = link.removeprefix(BASE)
        body = browser.request(download_path)[0]
        if isinstance(body, str):
            body = body.encode()
        assert len(body) == int(size) and hashlib.sha256(body).hexdigest() == digest
        print("Personal token, real REST/MCP reads and private media digest: passed", flush=True)

        client = browser.request("/oauth/register", {"client_name": oauth_name,
            "redirect_uris": ["http://127.0.0.1:44444/callback"], "token_endpoint_auth_method": "none"}, expect=201)[0]
        verifier = secrets.token_urlsafe(48)
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
        query = {"client_id": client["client_id"], "redirect_uri": "http://127.0.0.1:44444/callback",
            "response_type": "code", "code_challenge_method": "S256", "code_challenge": challenge,
            "state": "public-verification", "scope": "profile:read chats:list", "resource": BASE + "/mcp"}
        path = "/oauth/authorize?" + urllib.parse.urlencode(query)
        _, h = anonymous.request(path, expect=303)
        assert h["Location"].startswith("/login?")
        page, _ = browser.request(path)
        assert 'name="password"' not in page and field(page, "name", "account_id") == aid
        form = {k: field(page, "name", k) for k in ["request_id", "csrf", "account_id"]}
        form["decision"] = "allow"
        _, h = browser.request("/oauth/authorize", form, form=True, expect=303)
        callback = urllib.parse.parse_qs(urllib.parse.urlparse(h["Location"]).query)
        assert callback["iss"] == [BASE] and callback["state"] == ["public-verification"]
        tokens = browser.request("/oauth/token", {"grant_type": "authorization_code", "client_id": client["client_id"],
            "code": callback["code"][0], "redirect_uri": query["redirect_uri"], "code_verifier": verifier,
            "resource": BASE + "/mcp"}, form=True)[0]
        assert browser.request("/v1/profile", token=tokens["access_token"])[0]["data"]["account_id"] == aid
        rotated = browser.request("/oauth/token", {"grant_type": "refresh_token", "client_id": client["client_id"],
            "refresh_token": tokens["refresh_token"]}, form=True)[0]
        browser.request("/oauth/token", {"grant_type": "refresh_token", "client_id": client["client_id"],
            "refresh_token": tokens["refresh_token"]}, form=True, expect=400)
        clients = browser.request("/account/data")[0]["clients"]
        oauth_grant = next(c["id"] for c in clients if c["name"] == oauth_name)
        grants.append(oauth_grant)
        for grant in grants:
            browser.request("/account/clients/" + grant + "/revoke", {}, csrf=csrf)
        browser.request("/v1/profile", token=token, expect=401)
        browser.request(download_path, expect=404)
        browser.request("/v1/profile", token=rotated["access_token"], expect=401)
        browser.request("/oauth/token", {"grant_type": "refresh_token", "client_id": client["client_id"],
            "refresh_token": rotated["refresh_token"]}, form=True, expect=400)
        print("User-bound OAuth consent, PKCE, refresh rotation and grant revocation: passed", flush=True)
    finally:
        # Also clean up an OAuth grant if a check failed before its ID was read.
        clients = browser.request("/account/data")[0]["clients"]
        grants.extend(c["id"] for c in clients if c["name"] == oauth_name)
        for grant in set(grants):
            browser.request("/account/clients/" + grant + "/revoke", {}, csrf=csrf)
        browser.request("/account/logout", {}, csrf=csrf)
        browser.request("/account/data", expect=401)
        print("Temporary grants revoked and verification browser signed out", flush=True)

    if "--check-native" in sys.argv:
        page, _ = anonymous.request("/login")
        csrf = field(page, "id", "csrf")
        anonymous.request("/auth/login/start", {"return_to": "/account"}, csrf=csrf)
        for _ in range(15):
            state = anonymous.request("/auth/login/state", csrf=csrf)[0]
            assert not state.get("error"), "native login initialization failed"
            if state["state"] == "authorizationStateWaitPhoneNumber":
                break
            time.sleep(1)
        else:
            raise AssertionError("native login did not become ready for phone/QR")
        print("Public login initializes real TDLib with shared server credentials: passed", flush=True)
    print("Existing account remains connected; mirror counts:", data["stats"], flush=True)


if __name__ == "__main__":
    main()
