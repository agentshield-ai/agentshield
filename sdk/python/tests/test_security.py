"""Security behaviour of the client: redirects, plain-http keys, secret redaction."""

import http.server
import logging
import threading

import pytest

import weft
from weft._http import HTTPClient, WeftHTTPError, warn_plain_http_key
from weft._redact import REDACTED, Redactor, is_secret_key, redact_secret_values


class _Recorder:
    """An HTTP server that records every request; optionally redirects all of them."""

    def __init__(self, status=None, location=None):
        self.requests = []
        rec = self

        class H(http.server.BaseHTTPRequestHandler):
            def _handle(self):
                n = int(self.headers.get("content-length") or 0)
                body = self.rfile.read(n) if n else b""
                rec.requests.append((self.command, self.path, self.headers.get("authorization"), body))
                if status:
                    self.send_response(status)
                    self.send_header("Location", location + self.path)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", "2")
                self.end_headers()
                self.wfile.write(b"{}")

            do_GET = do_POST = do_PUT = do_DELETE = _handle

            def log_message(self, *a):
                pass

        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.url = f"http://127.0.0.1:{self.srv.server_address[1]}"
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def close(self):
        self.srv.shutdown()
        self.srv.server_close()


@pytest.fixture
def target():
    t = _Recorder()
    yield t
    t.close()


# ---------------------------------------------------------------- F4 redirects
@pytest.mark.parametrize("status", [301, 302, 303, 307, 308])
def test_redirect_is_refused_and_target_sees_nothing(target, status):
    redir = _Recorder(status, target.url)
    try:
        http_client = HTTPClient(redir.url, api_key="SECRET-KEY")
        with pytest.raises(WeftHTTPError) as ei:
            http_client.post("/api/v1/spans/batch", {"batch": [{"secret": "body"}]})
        assert ei.value.status == status and "redirect" in ei.value.reason and not ei.value.retryable
        with pytest.raises(WeftHTTPError):
            http_client.get("/api/v1/server")
        assert len(redir.requests) == 2
        assert target.requests == []
    finally:
        redir.close()


@pytest.mark.parametrize("status", [302, 307])
def test_redirect_refused_through_the_client(target, monkeypatch, status):
    monkeypatch.delenv("WEFT_DISABLED", raising=False)
    redir = _Recorder(status, target.url)
    try:
        c = weft.init("e/p", server_url=redir.url, api_key="SECRET-KEY", autopatch=False)

        @weft.trace
        def f(x):
            return x

        f("payload")
        c.flush(2)
        weft.finish()
        assert redir.requests  # the client did talk to its server
        assert target.requests == []
    finally:
        redir.close()


# ---------------------------------------------------------------- plain http
def test_plain_http_key_warning(caplog):
    with caplog.at_level(logging.WARNING, logger="weft"):
        assert warn_plain_http_key("http://weft.example.com:6345", "KEY-VALUE-123") is True
        assert warn_plain_http_key("http://weft.example.com:6345", "KEY-VALUE-123") is True  # warned once
        assert warn_plain_http_key("https://weft.example.com", "k") is False
        assert warn_plain_http_key("http://127.0.0.1:6345", "k") is False
        assert warn_plain_http_key("http://localhost:6345", "k") is False
        assert warn_plain_http_key("http://[::1]:6345", "k") is False
        assert warn_plain_http_key("http://weft.example.com:6345", None) is False
    msgs = [r.getMessage() for r in caplog.records if "plain http" in r.getMessage()]
    assert len(msgs) == 1 and "weft.example.com" in msgs[0] and "KEY-VALUE-123" not in msgs[0]


def test_content_refuses_non_http_urls(client, monkeypatch, tmp_path):
    secret = tmp_path / "secret.txt"
    secret.write_text("local file")
    # a hostile server resolving a reference to a local file
    monkeypatch.setattr(client, "resolve_url", lambda url, *a, **k: secret.as_uri())
    c = weft.Content.from_uri("s3://bucket/key")
    with pytest.raises(ValueError, match="not http"):
        c.data


# ---------------------------------------------------------------- F6 redaction
SECRET_KEYS = [
    "api_key", "API_KEY", "apiKey", "api-key", "X-Api-Key", "x_api_key", "openai_api_key", "OPENAI_API_KEY",
    "anthropicApiKey", "client_secret", "clientSecret", "secret", "aws_secret_access_key", "refresh_token",
    "access_token", "id_token", "bearer_token", "github-token", "token", "private_key", "privateKey", "passwd",
    "password", "db_password", "pwd", "credentials", "credential", "Cookie", "Set-Cookie", "Authorization",
    "Proxy-Authorization", "auth", "x-auth", "secret_key", "signing_key", "session_key",
]
NOT_SECRET_KEYS = [
    "max_tokens", "prompt_tokens", "total_tokens", "tokens", "tokenizer", "key", "sort_key", "primary_key",
    "public_key_id", "author", "name", "monkey", "model", "keyword", "access_key_id",
]


@pytest.mark.parametrize("k", SECRET_KEYS)
def test_secret_key_names_are_redacted(k):
    assert is_secret_key(k)
    assert Redactor(enabled=True)({k: "v", "other": 1}) == {k: REDACTED, "other": 1}


@pytest.mark.parametrize("k", NOT_SECRET_KEYS)
def test_ordinary_key_names_are_kept(k):
    assert not is_secret_key(k)
    assert Redactor(enabled=True)({k: "v"}) == {k: "v"}


SECRET_VALUES = [
    ("Authorization: Bearer abcdefgh12345678", "Authorization: Bearer " + REDACTED),
    ("bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig", "bearer " + REDACTED),
    ("Basic dXNlcjpwYXNzd29yZA==", "Basic " + REDACTED),
    ("key sk-proj-ABCDEFGHIJKLMNOPQRSTUVWX0123 end", f"key {REDACTED} end"),
    ("sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ", REDACTED),
    ("id AKIAIOSFODNN7EXAMPLE.", f"id {REDACTED}."),
    ("ghp_" + "a" * 36, REDACTED),
    ("xoxb-1234567890-abcdefghij", REDACTED),
    ("DATABASE_URL=postgres://user:pw-OOO@db/x", f"DATABASE_URL=postgres://user:{REDACTED}@db/x"),
    ("-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----", REDACTED),
]


@pytest.mark.parametrize("raw,want", SECRET_VALUES)
def test_secret_values_are_redacted(raw, want):
    assert redact_secret_values(raw) == want
    assert Redactor(enabled=True)({"text": raw}) == {"text": want}


@pytest.mark.parametrize("raw", ["a normal sentence", "sk-short", "https://example.com/a:b@c", "Bearer x", "token count 12"])
def test_ordinary_values_are_kept(raw):
    assert redact_secret_values(raw) == raw


def test_redaction_off_and_custom_lists(monkeypatch):
    monkeypatch.setenv("WEFT_REDACT_KEYS", "0")
    r = Redactor()
    assert not r.active
    assert r({"client_secret": "x", "t": "Bearer abcdefgh12345678"}) == {"client_secret": "x", "t": "Bearer abcdefgh12345678"}
    monkeypatch.delenv("WEFT_REDACT_KEYS")
    custom = Redactor(keys=["SSN"])  # exact list (normalised), no patterns
    assert custom({"ssn": 1, "s-s-n": 2, "client_secret": 3}) == {"ssn": REDACTED, "s-s-n": REDACTED, "client_secret": 3}


def test_reviewer_repro_through_a_traced_call(client, fake):
    @weft.trace
    def call_api(config, headers, env):
        return {"ok": True, "echo_headers": headers}

    config = {"openai_api_key": "sk-openai-AAA", "client_secret": "cs-BBB", "refresh_token": "rt-CCC",
              "aws_secret_access_key": "aws-DDD", "private_key": "-----BEGIN KEY-----EEE", "passwd": "FFF",
              "credentials": "GGG", "api-key": "HHH", "apiKey": "III", "bearer_token": "JJJ"}
    headers = {"X-Api-Key": "xk-KKK", "Cookie": "session=LLL", "Proxy-Authorization": "Basic MMM", "Authorization": "Bearer NNN"}
    call_api(config, headers, "DATABASE_URL=postgres://u:pw-OOO@db/x")
    client.flush()
    [c] = fake.calls_by_op("call_api")
    import json

    blob = json.dumps({"i": c["inputs"], "o": c["output"]})
    for planted in ("AAA", "BBB", "CCC", "DDD", "EEE", "FFF", "GGG", "HHH", "III", "JJJ", "KKK", "LLL", "MMM", "NNN", "OOO"):
        assert planted not in blob, planted


# ---------------------------------------------------------------- WAL file modes
@pytest.mark.skipif(__import__("os").name != "posix", reason="POSIX modes")
def test_wal_is_private(tmp_path):
    import os
    import stat

    from weft._sender import WriteAheadLog

    d = tmp_path / "wal"
    d.mkdir(mode=0o755)
    os.chmod(d, 0o755)  # made by an older version
    wal = WriteAheadLog(str(d / "x.jsonl"))
    wal.append([("post", ("/api/v1/objects/save", {"value": "secret"}))])
    assert stat.S_IMODE(os.stat(d).st_mode) == 0o700
    assert stat.S_IMODE(os.stat(d / "x.jsonl").st_mode) == 0o600
    fresh = WriteAheadLog(str(tmp_path / "new" / "deep" / "y.jsonl"))
    fresh.append([("post", ("/p", {}))])
    assert stat.S_IMODE(os.stat(tmp_path / "new" / "deep").st_mode) == 0o700
    assert stat.S_IMODE(os.stat(tmp_path / "new" / "deep" / "y.jsonl").st_mode) == 0o600
