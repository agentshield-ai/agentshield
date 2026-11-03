# Dashboards, automations and settings

## Dashboards

A **dashboard** is a page of charts you build from your project's data:
counts, latency, tokens, cost or any attribute over time, and scores. Create
one on **Dashboards**, or keep a chart from **Traces** with **+ Add chart to
dashboard**. Dashboards belong to the project, so everyone on it sees them.

## Automations

An **automation** acts when something happens, for example posting to a
webhook when a score drops below a threshold or a monitor raises an alert.
Create one on **Automations**: choose what triggers it and what it does, then
use **Send test** to try it.

Webhooks can reach public addresses only, unless the server operator allows
an internal one.

## Settings

**Settings** holds the project's configuration:

- **Members** and **API keys:** who can see the project and change it, and
  the keys SDKs, plugins and AgentShield use to send data.
- **Secrets & providers:** provider keys for the playground, LLM judges and
  monitors. They are stored encrypted and never shown again.
- **Costs:** model prices, used for every cost figure.
- **Retention**, **Sampling** and **Sensitive data:** how long data is kept,
  how much of it is stored, and redaction of personal data before it is
  stored.
- **Audit log:** who changed what.
