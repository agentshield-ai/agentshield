"""Module entry-point: ``python -m agentshield_gemini``.

Delegates to the hook dispatcher so the connector can be wired into the
Gemini CLI either as ``python -m agentshield_gemini`` or
``python -m agentshield_gemini.hook``.
"""

from __future__ import annotations

from .hook import main

if __name__ == "__main__":
    raise SystemExit(main())
