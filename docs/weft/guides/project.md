# Dashboards, automations and settings

## Dashboards

A **dashboard** is a page of charts you build from your project's data:
counts, latency, tokens, cost or any attribute over time, and scores. Create
one on **Dashboards**, or keep a chart from **Traces** with **+ Add chart to
dashboard**. Dashboards belong to the project, so everyone on it sees them.

## Automations

An **automation** acts when something happens, for example posting to a
webhook when a score drops below a threshold or a monitor raises an alert.
Create one with **New automation**: choose what triggers it and what it does,
then use **Send test** in the editor to try it. On the list, each automation
has a switch to turn it on or off and a menu (•••) with **Send a test**,
**Show its runs**, **Edit** and **Delete**. Deleting can be undone for a few
seconds from the message that appears; the automation's run history is kept.

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

Each section of Settings opens with a sentence that answers it, for example
"3 API keys; 1 has not been used in 90 days."

### Costs

Every model has a built-in price per million tokens. Choose a model's name to
set this project's own price for it; a project price replaces the built-in one
for every cost figure in the project. Delete it (the bin icon) to go back to
the built-in price. Cached input tokens are billed at the cache price when
there is one, else at the input price.

### Members

A person's role on a project is the stronger of their role in the project's
team (the first part of the project id) and their role on the project
itself:

- **Viewers** read.
- **Members** also write traces, assets and feedback.
- **Admins** also manage members, keys and settings.

Server admins are admins everywhere, and a person's API keys carry their
roles. Changing members needs the admin role on the project or its team.

### API keys

Keys are stored hashed: a key's secret is shown once, when it is created. A
key can be limited to some projects and made read-only; admin keys also
manage keys, secrets and settings. A key of a registered user carries that
user's team and project roles; without admin rights you see and create only
your own keys.

While the server is open (no keys yet), anyone who can reach it can use it.
The first key you create must be an admin key; creating it turns sign-in on,
and the key is saved in your browser so you stay signed in.

### Buckets

Media that already lives in your own S3 or Cloud Storage bucket can be logged
by its URI: put `s3://bucket/key.png` or `gs://bucket/key.mp4` in any input,
output or tool result. Once the bucket is registered, for the project's team
or for one project, images, video, audio and PDFs show inline through
short-lived presigned URLs; other files show as a download link. The bytes
never pass through Weft.

The bucket's CORS rules must allow `GET` and `HEAD` from the address you open
Weft at. **Test** checks them and reads the bucket's test object. Registering,
testing and removing buckets needs a server admin.

### Audit log

Every write, admin action, sign-in and refused request is recorded: who, when,
the endpoint, the project, the assets and steps it touched, and the result.
Request bodies and secrets are never recorded. Search by who made the request,
or type a path such as `/api/v1/spans` to search by endpoint; **Filter** narrows
by method, result and time (and, for server admins, to all projects). **Export
CSV** downloads what the filters match.

### Retention and sampling

**Retention** deletes spans and their feedback once they are older than the
number of days you set; a background job runs hourly, and **Purge now**
deletes them at once. **Keep forever** turns retention off.

**Sampling** keeps a share of traces. The choice is made per trace from its
id, so a trace's spans are kept or dropped together. With **Dry run** on,
every trace is kept, and the spans sampling would drop carry the attribute
`weft_ingest_sampling` (`{"would_drop": true, "sample_rate": …}`), so you can
filter for them on **Traces** before turning sampling on.

### Sensitive data

With **Redact sensitive data as it arrives** on, matches in inputs, outputs,
errors and attributes become placeholders such as `<EMAIL_ADDRESS>` before
anything is stored, for SDK spans and OpenTelemetry spans alike. Choose which
detectors apply, and add your own patterns (RE2 regular expressions; a match
becomes `<NAME>`). The detectors apply only while redaction is on. Data stored
earlier is not changed.

## Users and teams

**Users & teams** (from the Projects page) is where server admins add users
and teams.

- A **user** owns API keys and browser sessions; their id is recorded as the
  author of what they write. People who sign in with SSO are added
  automatically, and an account added here with the same email is linked on
  their first sign-in.
- A **team** is the first part of a project id (`my-team/support-bot` belongs
  to `my-team`). Its members' roles apply to every project of the team, new
  ones too. Memberships from SSO come from identity-provider groups and are
  refreshed at each sign-in.
