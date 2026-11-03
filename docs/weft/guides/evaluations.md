# Evaluations and scores

Scores tell you how well a session, an agent or a hand-off went. Evaluations
tell you which version of your model or agent does better on a fixed set of
examples.

## Scores

A **score** is a number from 0 to 1 given by a **scorer**: an LLM judge (for
example "did the session achieve the user's goal?"), a rule, or a person.
Each score shows its scorer and the reason it gave.

- On a session's **Scores** tab, choose **Run scorers** to score that session.
- The **Scores** page lists every score in the project; filter it by scorer
  or by what was scored (session, agent or hand-off).
- The **Low scores** card on the Sessions page lists sessions with any score
  below 0.5.

## Monitors

A **monitor** runs scorers automatically on new data as it arrives, so you
see quality change without running anything by hand. Create one on the
**Monitors** page: choose the scorers, which spans they apply to, and what
share of new data to score.

## Evaluations

An **evaluation** runs a model or agent over a **dataset** of examples and
scores every answer.

1. Create a dataset on the **Datasets** page, from a file or from recorded
   spans (select rows on Traces, then **Add to dataset**).
2. On **Evaluations**, choose **New evaluation**, pick the dataset, the model
   and the scorers, and run it. You can also run evaluations from code with
   the SDKs.
3. Select two or more runs and choose **Compare** to see, example by example,
   where one version does better.

## Leaderboards

A **leaderboard** ranks models or agents by their latest evaluation scores,
one column per evaluation and scorer.

## Review queues

A **review queue** is a list of spans for people to rate one by one, with the
rating and note fields you choose. Add spans from **Traces** (select rows,
then **Add to queue**) or from a span's page.

## Playground

The **Playground** tries a prompt against several models side by side. Save
what works as a prompt or a model to use it in evaluations.

## Datasets, models, prompts and scorers

These are versioned: each change keeps the previous version, so you can
compare versions and go back. **Functions** lists the traced functions of
your code, and **All objects** everything versioned in the project.
