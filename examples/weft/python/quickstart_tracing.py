"""Quickstart: trace functions with @weft.op.

    pip install -e sdk/python
    WEFT_SERVER_URL=http://localhost:6345 python examples/weft/python/quickstart_tracing.py
"""

import asyncio

import weft

client = weft.init("quickstart")


@weft.op
def tokenize(text: str) -> list:
    return text.lower().split()


@weft.op
def count_words(text: str) -> dict:
    counts: dict = {}
    for tok in tokenize(text):
        counts[tok] = counts.get(tok, 0) + 1
    return counts


@weft.op
async def summarize(texts: list) -> dict:
    # asyncio tasks inherit the trace context automatically
    results = await asyncio.gather(*[asyncio.to_thread(count_words, t) for t in texts])
    return {"documents": len(texts), "unique_words": len(set().union(*results))}


@weft.op
def stream_numbers(n: int):
    """Generators are traced too: the yielded items become the call output."""
    for i in range(n):
        yield i * i


if __name__ == "__main__":
    print(count_words("the cat and the hat"))
    print(asyncio.run(summarize(["a b c", "b c d"])))
    print(list(stream_numbers(4)))

    result, call = count_words.call("one two two")
    print("call id:", call.id, "trace:", call.trace_id)

    with weft.attributes({"env": "dev", "user_id": "u-123"}):
        count_words("tagged with attributes")

    weft.flush()
    for c in client.get_calls(filter={"op_names": ["count_words"]}, limit=5):
        print(c.op_base_name, c.inputs, "->", c.output)
