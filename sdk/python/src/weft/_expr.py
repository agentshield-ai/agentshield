"""Local evaluator for the Mongo-style operation language (API.md), used by RuleScorer."""

from __future__ import annotations

from typing import Any, Dict


def get_path(doc: Any, path: str) -> Any:
    cur = doc
    for part in path.split("."):
        if isinstance(cur, dict):
            cur = cur.get(part)
        elif isinstance(cur, list):
            try:
                cur = cur[int(part)]
            except (ValueError, IndexError):
                return None
        else:
            return None
    return cur


def _convert(v: Any, to: str) -> Any:
    try:
        if to == "double":
            return float(v)
        if to == "int":
            return int(float(v))
        if to == "string":
            return v if isinstance(v, str) else ("" if v is None else str(v))
        if to == "bool":
            return bool(v)
        if to == "exists":
            return v is not None
    except (TypeError, ValueError):
        return None
    raise ValueError(f"unknown $convert target {to!r}")


def _cmp(op: str, a: Any, b: Any) -> bool:
    if op == "$eq":
        return a == b
    if a is None or b is None:
        return False
    try:
        if op == "$gt":
            return a > b
        if op == "$gte":
            return a >= b
        if op == "$lt":
            return a < b
        if op == "$lte":
            return a <= b
    except TypeError:
        return False
    raise ValueError(op)


def evaluate(expr: Any, doc: Dict[str, Any]) -> Any:
    """Evaluate an operation (optionally wrapped in ``{"$expr": ...}``) against ``doc``."""
    if isinstance(expr, dict) and set(expr) == {"$expr"}:
        expr = expr["$expr"]
    if not isinstance(expr, dict) or len(expr) != 1:
        return expr
    (op, arg), = expr.items()
    if op == "$literal":
        return arg
    if op == "$getField":
        return get_path(doc, arg)
    if op == "$and":
        return all(bool(evaluate(x, doc)) for x in arg)
    if op == "$or":
        return any(bool(evaluate(x, doc)) for x in arg)
    if op == "$not":
        return not bool(evaluate(arg[0] if isinstance(arg, list) else arg, doc))
    if op in ("$eq", "$gt", "$gte", "$lt", "$lte"):
        return _cmp(op, evaluate(arg[0], doc), evaluate(arg[1], doc))
    if op == "$in":
        v = evaluate(arg[0], doc)
        return v in [evaluate(x, doc) for x in arg[1]]
    if op == "$contains":
        hay = evaluate(arg["input"], doc)
        needle = evaluate(arg["substr"], doc)
        hay = "" if hay is None else str(hay)
        needle = "" if needle is None else str(needle)
        if arg.get("case_insensitive"):
            return needle.lower() in hay.lower()
        return needle in hay
    if op == "$convert":
        return _convert(evaluate(arg["input"], doc), arg["to"])
    raise ValueError(f"unsupported operator {op!r}")
