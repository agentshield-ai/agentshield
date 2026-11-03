"""Integration settings, NER PII redaction and local model scorers.

    pip install -e sdk/python                       # base install: no heavy deps
    pip install -e 'sdk/python[scorers]'           # optional: run the local model scorers for real
    WEFT_SERVER_URL=http://localhost:6345 python examples/weft/python/integrations_and_scorers.py

Runs offline: if transformers/presidio are not installed, the scorer and the
PII analyzer below fall back to small stand-ins so the example still shows
what gets recorded.
"""

import asyncio
import importlib.util

import weft
from weft import local_scorers
from weft.integrations import OpSettings


def hide_messages(inputs: dict) -> dict:
    """Record the model and the number of messages, not their content."""
    out = dict(inputs)
    if "messages" in out:
        out["messages"] = f"<{len(out['messages'])} messages>"
    return out


class TinyAnalyzer:
    """Presidio-style analyzer used when presidio-analyzer is not installed."""

    NAMES = {"Ada Lovelace": "PERSON", "London": "LOCATION", "Analytical Engines Ltd": "ORGANIZATION"}

    def analyze(self, text, entities=None, language="en"):
        hits = []
        for word, ent in self.NAMES.items():
            i = text.find(word)
            if i >= 0 and (entities is None or ent in entities):
                hits.append(type("R", (), {"entity_type": ent, "start": i, "end": i + len(word), "score": 0.9})())
        return hits


settings = {"redact_pii": True}
if importlib.util.find_spec("presidio_analyzer") is None:
    settings["redact_pii_analyzer"] = TinyAnalyzer()  # otherwise Presidio is used automatically

client = weft.init(
    "integrations-demo",
    settings=settings,
    autopatch_settings={
        # every OpenAI call: inputs scrubbed, a fixed display name and a team attribute
        "openai": {"op_settings": {"postprocess_inputs": hide_messages, "call_display_name": "chat",
                                   "attributes": {"team": "research"}}},
        "anthropic": {"op_settings": OpSettings(color="purple")},
        "crewai": {"enabled": False},  # or WEFT_DISABLED_INTEGRATIONS=crewai
    },
)


@weft.op
def write_bio(name: str) -> str:
    # PERSON, LOCATION and ORGANIZATION are redacted by the NER engine; e-mail by regex
    return f"{name} worked in London with Analytical Engines Ltd (ada@example.com)."


if importlib.util.find_spec("transformers") is None:
    # Stand-in for the Celadon model so the example runs without torch.
    local_scorers.load_sequence_classifier = lambda *a, **k: (lambda text: [2, 0, 0, 0, 0] if "idiot" in text else [0, 0, 0, 0, 0])

dataset = weft.Dataset(name="bios", rows=[{"name": "Ada Lovelace"}, {"name": "an idiot"}])
evaluation = weft.Evaluation(dataset=dataset, scorers=[local_scorers.ToxicityScorer()])

print(write_bio("Ada Lovelace"))
print(asyncio.run(evaluation.evaluate(write_bio)))
weft.flush()
