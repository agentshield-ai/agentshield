"""Send OpenTelemetry spans (OpenInference / GenAI conventions) to Weft.

    pip install -e 'sdk/python[otel]'
    python examples/weft/python/otel_manual_spans.py

Any OTel instrumentation (OpenInference, OpenLLMetry, ...) works the same way
once the tracer provider from ``weft.otel.configure`` is installed.
"""

import json

import weft
import weft.otel
from opentelemetry import trace

provider = weft.otel.configure(project="otel-demo", service_name="otel-example")
tracer = trace.get_tracer("otel-example")


def retrieve(query: str) -> list:
    with tracer.start_as_current_span("retrieve") as span:
        span.set_attribute("openinference.span.kind", "RETRIEVER")
        span.set_attribute("input.value", query)
        docs = ["Paris is the capital of France.", "France is in Europe."]
        span.set_attribute("output.value", json.dumps(docs))
        return docs


def generate(query: str, docs: list) -> str:
    with tracer.start_as_current_span("chat gpt-4o-mini") as span:
        span.set_attribute("openinference.span.kind", "LLM")
        span.set_attribute("llm.model_name", "gpt-4o-mini")
        span.set_attribute("llm.input_messages.0.message.role", "user")
        span.set_attribute("llm.input_messages.0.message.content", f"{query}\n\n{docs}")
        answer = "Paris"
        span.set_attribute("llm.output_messages.0.message.role", "assistant")
        span.set_attribute("llm.output_messages.0.message.content", answer)
        span.set_attribute("llm.token_count.prompt", 42)
        span.set_attribute("llm.token_count.completion", 1)
        return answer


def rag(query: str) -> str:
    with tracer.start_as_current_span("rag_pipeline") as span:
        span.set_attribute("session.id", "otel-session-1")  # becomes the session_id
        weft.otel.set_inputs(span, {"query": query})
        answer = generate(query, retrieve(query))
        weft.otel.set_output(span, {"answer": answer})
        return answer


if __name__ == "__main__":
    print(rag("What is the capital of France?"))
    provider.force_flush()
    provider.shutdown()
    print("spans exported to", weft.otel.otlp_endpoint())
