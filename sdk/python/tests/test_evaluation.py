import asyncio

import pytest

import weft
from weft import scorers


ROWS = [
    {"question": "1+1", "expected": "2"},
    {"question": "2+2", "expected": "4"},
    {"question": "3+3", "expected": "6"},
    {"question": "capital of France", "expected": "Paris"},
]


class Calc(weft.Model):
    offset: int = 0

    @weft.trace
    async def predict(self, question: str) -> str:
        if "+" in question:
            a, b = question.split("+")
            return str(int(a) + int(b) + self.offset)
        return "Lyon"


@weft.trace
def exact(expected: str, output: str) -> bool:
    return expected == output


@weft.trace
def detail(output: str, expected: str) -> dict:
    return {"len_diff": abs(len(output) - len(expected)), "same": output == expected}


class LenScorer(weft.Scorer):
    @weft.trace
    def score(self, *, output, answer):
        return len(output) if answer else 0


def test_auto_summarize_shapes():
    s = weft.auto_summarize
    assert s([True, False, True, None]) == {"true_count": 2, "true_fraction": 2 / 3}
    assert s([1, 2, 3.0]) == {"mean": 2.0}
    assert s([{"a": True, "b": 1}, {"a": False, "b": 3}]) == {"a": {"true_count": 1, "true_fraction": 0.5}, "b": {"mean": 2.0}}
    assert s(["x", "y"]) is None
    assert s([]) is None


def test_evaluation_call_structure_and_summary(client, fake):
    ds = weft.Dataset(name="arith", rows=ROWS)
    ev = weft.Evaluation(
        name="arith-eval",
        dataset=ds,
        scorers=[exact, detail, scorers.ExactMatchScorer(name="em"), LenScorer(name="len", column_map={"answer": "expected"})],
    )
    summary = asyncio.run(ev.evaluate(Calc(name="calc")))
    assert summary["exact"] == {"true_count": 3, "true_fraction": 0.75}
    assert summary["em"] == {"true_count": 3, "true_fraction": 0.75}
    assert summary["detail"]["same"] == {"true_count": 3, "true_fraction": 0.75}
    assert summary["detail"]["len_diff"] == {"mean": pytest.approx(0.25)}
    assert summary["len"] == {"mean": pytest.approx((1 + 1 + 1 + 4) / 4)}
    assert set(summary["model_latency"]) == {"mean"}

    client.flush()
    [root] = fake.calls_by_op("Evaluation.evaluate")
    eval_val = fake.objs["arith-eval"][0]
    model_val = fake.objs["calc"][0]
    assert root["parent_id"] is None
    assert root["inputs"]["self"] == f"weft:///test-entity/test-proj/object/arith-eval:{eval_val['digest']}"
    assert root["inputs"]["model"] == f"weft:///test-entity/test-proj/object/calc:{model_val['digest']}"
    assert root["output"] == summary
    # Evaluation object follows the documented value convention
    v = eval_val["value"]
    assert v["_type"] == "Evaluation" and v["_bases"] == ["Object", "BaseModel"]
    assert v["dataset"].startswith("weft:///test-entity/test-proj/object/arith:")
    assert v["trials"] == 1 and len(v["scorers"]) == 4
    assert v["scorers"][0].startswith("weft:///test-entity/test-proj/function/exact:")
    assert v["scorers"][2].startswith("weft:///test-entity/test-proj/object/em:")
    assert "preprocess_model_input" not in v

    kids = fake.children(root["id"])
    pas = [k for k in kids if fake.op_base(k) == "Evaluation.predict_and_score"]
    summ = [k for k in kids if fake.op_base(k) == "Evaluation.summarize"]
    assert len(pas) == 4 and len(summ) == 1
    assert summ[0]["output"] == summary
    assert summ[0]["inputs"] == {"self": root["inputs"]["self"]}
    ds_ref = v["dataset"]
    tdigest = fake.objs["arith"][0]["value"]["rows"].rsplit("/", 1)[1]
    row_digests = {r["digest"]: r["value"] for r in fake.tables[tdigest]}
    for p in pas:
        assert p["inputs"]["self"] == root["inputs"]["self"]
        assert p["inputs"]["model"] == root["inputs"]["model"]
        ex = p["inputs"]["example"]
        assert ex.startswith(ds_ref + "/attr/rows/id/")
        assert ex.rsplit("/", 1)[1] in row_digests
        out = p["output"]
        assert set(out) == {"output", "scores", "model_latency"}
        assert set(out["scores"]) == {"exact", "detail", "em", "len"}
        gk = fake.children(p["id"])
        names = [fake.op_base(g) for g in gk]
        assert names[0] == "Calc.predict"
        assert sorted(names[1:]) == sorted(["exact", "detail", "ExactMatchScorer.score", "LenScorer.score"])
        pred = gk[0]
        assert pred["inputs"]["self"] == root["inputs"]["model"]
        assert pred["inputs"]["question"] == row_digests[ex.rsplit("/", 1)[1]]["question"]
        assert pred["output"] == out["output"]


def test_evaluation_with_function_model_trials_and_preprocess(client, fake):
    @weft.trace
    def model(q: str) -> str:
        return q.upper()

    ev = weft.Evaluation(
        dataset=[{"text": "a", "expected": "A"}, {"text": "b", "expected": "x"}],
        scorers=[exact],
        trials=3,
        preprocess_model_input=lambda row: {"q": row["text"]},
    )
    summary = asyncio.run(ev.evaluate(model))
    assert summary["exact"] == {"true_count": 3, "true_fraction": 0.5}
    client.flush()
    [root] = fake.calls_by_op("Evaluation.evaluate")
    assert root["inputs"]["model"].startswith("weft:///test-entity/test-proj/function/model:")
    assert len(fake.calls_by_op("Evaluation.predict_and_score")) == 6
    assert len(fake.calls_by_op("model")) == 6
    assert fake.objs["Evaluation"][0]["value"]["trials"] == 3


def test_evaluation_model_errors_are_recorded(client, fake):
    @weft.trace
    def flaky(question: str) -> str:
        if question == "2+2":
            raise ValueError("nope")
        return "2"

    ev = weft.Evaluation(dataset=ROWS[:2], scorers=[exact])
    summary = asyncio.run(ev.evaluate(flaky))
    assert summary["exact"] == {"true_count": 1, "true_fraction": 0.5}
    client.flush()
    errs = [c for c in fake.calls_by_op("flaky") if c.get("error")]
    assert len(errs) == 1 and errs[0]["error"].startswith("ValueError")


def test_evaluation_parallelism(client, fake, monkeypatch):
    monkeypatch.setenv("WEFT_PARALLELISM", "2")
    running = {"now": 0, "max": 0}

    @weft.trace
    async def slow(question: str) -> str:
        running["now"] += 1
        running["max"] = max(running["max"], running["now"])
        await asyncio.sleep(0.02)
        running["now"] -= 1
        return question

    asyncio.run(weft.Evaluation(dataset=ROWS * 3, scorers=[]).evaluate(slow))
    assert running["max"] == 2


def test_evaluation_with_llm_judge_and_server_model(client, fake):
    model = weft.LLMStructuredCompletionModel(
        name="echo", llm_model_id="mock/echo",
        default_params={"messages_template": [{"role": "user", "content": "{question}"}]},
    )
    judge = scorers.LLMAsAJudgeScorer(name="judge", model="mock/json", scoring_prompt="{question} -> {output}")
    ev = weft.Evaluation(name="llm-eval", dataset=ROWS[:2], scorers=[judge, scorers.ContainsScorer(name="has_q", target_column="question")])
    summary = asyncio.run(ev.evaluate(model))
    assert summary["judge"]["correct"] == {"true_count": 2, "true_fraction": 1.0}
    assert summary["has_q"] == {"true_count": 2, "true_fraction": 1.0}
    client.flush()
    for c in fake.calls_by_op("LLMStructuredCompletionModel.predict"):
        assert c["summary"]["usage"]["mock/echo"]["total_tokens"] == 10
    [root] = fake.calls_by_op("Evaluation.evaluate")
    assert "usage" not in root.get("summary", {})  # roll-up is the server's job


def test_evaluation_without_client_runs_locally():
    weft.finish()
    ev = weft.Evaluation(dataset=ROWS, scorers=[exact])
    summary = asyncio.run(ev.evaluate(Calc()))
    assert summary["exact"]["true_count"] == 3


def test_evaluation_logger_structure(client, fake):
    ev = weft.EvaluationLogger(model="my-model", dataset="my-ds", name="imperative")
    for q, out, ok in [("a", "A", True), ("b", "B", False)]:
        pred = ev.log_prediction(inputs={"q": q}, output=out, model_latency=0.5)
        pred.log_score("correct", ok)
        pred.log_score("quality", {"stars": 4 if ok else 2})
        pred.finish()
    with ev.log_prediction({"q": "c"}, "C") as pred:
        pred.log_score("correct", True)
    summary = ev.log_summary({"note": "manual"})
    assert summary["correct"] == {"true_count": 2, "true_fraction": 2 / 3}
    assert summary["quality"] == {"stars": {"mean": 3.0}}
    assert summary["model_latency"] == {"mean": 0.5}
    assert summary["note"] == "manual"

    client.flush()
    [root] = fake.calls_by_op("Evaluation.evaluate")
    assert root["display_name"] == "imperative"
    assert root["inputs"]["self"].startswith("weft:///test-entity/test-proj/object/imperative:")
    assert root["inputs"]["model"].startswith("weft:///test-entity/test-proj/object/my-model:")
    assert root["output"] == summary
    kids = fake.children(root["id"])
    assert [fake.op_base(k) for k in kids] == ["Evaluation.predict_and_score"] * 3 + ["Evaluation.summarize"]
    p0 = kids[0]
    assert p0["inputs"]["example"] == {"q": "a"}
    assert p0["output"] == {"output": "A", "scores": {"correct": True, "quality": {"stars": 4}}, "model_latency": 0.5}
    gk = fake.children(p0["id"])
    assert [fake.op_base(g) for g in gk] == ["my-model.predict", "correct", "quality"]
    assert gk[0]["output"] == "A" and gk[1]["output"] is True
    model_val = fake.objs["my-model"][0]["value"]
    assert model_val["_class_name"] == "Model" and model_val["_bases"] == ["Object", "BaseModel"]
    ds_val = fake.objs["my-ds"][0]["value"]
    assert ds_val["_class_name"] == "Dataset"
