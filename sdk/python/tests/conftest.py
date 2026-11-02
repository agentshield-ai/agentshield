import os
import sys

import pytest

sys.path.insert(0, os.path.dirname(__file__))

import weft  # noqa: E402
from fake_server import FakeWeft  # noqa: E402


@pytest.fixture
def fake():
    srv = FakeWeft()
    yield srv
    srv.close()


@pytest.fixture
def client(fake, monkeypatch):
    monkeypatch.delenv("WEFT_DISABLED", raising=False)
    c = weft.init("test-entity/test-proj", server_url=fake.url, autopatch=False)
    c._sender.flush_interval = 0.01
    yield c
    weft.flush(5)
    weft.finish()
    from weft.integrations import unpatch_all

    unpatch_all()
