"""Dataset + Evaluation with built-in and custom scorers, plus EvaluationLogger.

    python examples/weft/python/evaluation.py
"""

import asyncio

import weft
from weft import scorers

weft.init("eval-demo")

dataset = weft.Dataset(
    name="capitals",
    rows=[
        {"country": "France", "expected": "Paris"},
        {"country": "Japan", "expected": "Tokyo"},
        {"country": "Australia", "expected": "Canberra"},
        {"country": "Canada", "expected": "Ottawa"},
    ],
)


class CapitalModel(weft.Model):
    """A deliberately imperfect model."""

    knowledge: dict = {"France": "Paris", "Japan": "Tokyo", "Australia": "Sydney", "Canada": "Ottawa"}

    @weft.trace
    async def predict(self, country: str) -> str:
        return self.knowledge.get(country, "unknown")


@weft.trace
def exact(expected: str, output: str) -> bool:
    """Plain function scorer: argument names are matched to dataset columns."""
    return expected == output


class LengthScorer(weft.Scorer):
    """Class scorer with a column_map: score(target=...) reads the 'expected' column."""

    @weft.trace
    def score(self, *, output: str, target: str) -> dict:
        return {"length_delta": abs(len(output) - len(target)), "non_empty": bool(output)}


evaluation = weft.Evaluation(
    name="capitals-eval",
    dataset=dataset,
    scorers=[
        exact,
        LengthScorer(name="length", column_map={"target": "expected"}),
        scorers.ExactMatchScorer(name="exact_builtin", target_column="expected"),
        scorers.LevenshteinScorer(name="similarity", target_column="expected"),
        # Runs through the server's /completions/create; mock/json works offline.
        scorers.LLMAsAJudgeScorer(
            name="judge",
            model="mock/json",
            scoring_prompt="Is {output} the capital of {country}? Reply JSON {{\"correct\": bool}}",
        ),
    ],
)

if __name__ == "__main__":
    summary = asyncio.run(evaluation.evaluate(CapitalModel(name="capital-model")))
    print("summary:", summary)

    # Server-runnable model: same Evaluation, model executed via the server's LLM proxy.
    llm_model = weft.LLMStructuredCompletionModel(
        name="echo-model",
        llm_model_id="mock/echo",
        default_params={"messages_template": [{"role": "user", "content": "{country}"}], "temperature": 0},
    )
    print("llm model summary:", asyncio.run(evaluation.evaluate(llm_model)))

    # Imperative logging when you run predictions yourself.
    ev = weft.EvaluationLogger(model="hand-rolled", dataset="capitals-inline")
    for row in dataset.rows:
        output = row["expected"] if row["country"] != "Australia" else "Sydney"
        pred = ev.log_prediction(inputs={"country": row["country"]}, output=output)
        pred.log_score("correct", output == row["expected"])
        pred.finish()
    print("logger summary:", ev.log_summary())
    weft.flush()
