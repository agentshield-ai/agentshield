// Project settings (DESIGN.md §7, §7.1 rules 1 and 11, §7.2): a settings app.
// A list of sections on the left (a menu on a phone), and for the chosen one
// a sentence that answers it ("3 API keys; 1 has not been used in 90
// days."), then grouped lists of labelled rows with their controls on the
// right. The section is ?tab= (the earlier tab ids all keep working).
// Deletions that can be put back are undone from a toast; revoking a key,
// purging and deleting the project ask first.
import { useMemo, useState } from "react";
import { isOfflineModel, modelDisplayName } from "../../lib/modelWords";
import { Link, useSearchParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, getApiKey, setApiKey } from "../../api/client";
import { parseRef } from "../../api/refs";
import type { PriceEntry, SecretEntry, WeftObject } from "../../api/types";
import { iamApi } from "../../api/iam";
import {
  CopyButton,
  ErrorBox,
  Loading,
  Spinner,
} from "../../components/common";
import {
  annotationSpecNames,
  useObjects,
} from "../../components/objects/objectUtils";
import {
  SettingsEmpty,
  SettingsRow,
  SettingsSection,
  SettingsSentence,
  StatusWords,
  useUndoDelete,
} from "../../components/settings/SettingsList";
import { CopyText } from "../../components/ui/Inspector";
import { Icon } from "../../components/ui/Icons";
import { PageHeader } from "../../components/ui/PageHeader";
import { PlainTable } from "../../components/ui/PlainTable";
import { SearchField } from "../../components/ui/SearchField";
import { Segmented, segmentedTabId } from "../../components/ui/Segmented";
import { Sentence } from "../../components/ui/Sentence";
import { useProjectId } from "../../hooks/useProject";
import { fmtBytes, fmtCost, fmtDate, fmtNumber } from "../../lib/format";
import { relativeTime } from "../../lib/summaries";
import {
  annotationsSentence,
  costsSentence,
  generalSentence,
  secretsSentence,
} from "../../lib/summaries/project";
import { objectUrl, settingsUrl } from "../../lib/urls";
import {
  ApiKeysTab,
  DeleteProjectTab,
  RetentionTab,
  SamplingTab,
  SensitiveDataTab,
} from "./AdminTabs";
import { AuditLogTab, MembersTab } from "./IamTabs";
import { BucketsTab } from "./BucketsTab";
import {
  AnnotationSpecModal,
  CostModal,
  describeSchema,
  ProviderModal,
  ProviderModelModal,
  SecretModal,
} from "./SettingsModals";

type Tab =
  | "general"
  | "costs"
  | "secrets"
  | "buckets"
  | "annotations"
  | "integrations"
  | "apikeys"
  | "members"
  | "audit"
  | "retention"
  | "sampling"
  | "sensitive"
  | "delete";
const GROUPS: { label: string; tabs: { id: Tab; label: string }[] }[] = [
  {
    label: "Project",
    tabs: [
      { id: "general", label: "General" },
      { id: "integrations", label: "Send data" },
      { id: "costs", label: "Costs" },
      { id: "annotations", label: "Annotations" },
    ],
  },
  {
    label: "Models and media",
    tabs: [
      { id: "secrets", label: "Secrets & providers" },
      { id: "buckets", label: "Buckets" },
    ],
  },
  {
    label: "Access",
    tabs: [
      { id: "apikeys", label: "API keys" },
      { id: "members", label: "Members" },
      { id: "audit", label: "Audit log" },
    ],
  },
  {
    label: "Data",
    tabs: [
      { id: "retention", label: "Retention" },
      { id: "sampling", label: "Sampling" },
      { id: "sensitive", label: "Sensitive data" },
      { id: "delete", label: "Delete project" },
    ],
  },
];
const TABS = GROUPS.flatMap((g) => g.tabs);
/** Earlier tab ids. */
const LEGACY: Record<string, Tab> = { apikey: "apikeys" };

export function SettingsPage() {
  const projectId = useProjectId();
  const [params, setParams] = useSearchParams();
  const raw = params.get("tab") ?? "";
  const tab = (LEGACY[raw] ??
    (TABS.some((t) => t.id === raw) ? raw : "general")) as Tab;
  const label = TABS.find((t) => t.id === tab)!.label;
  const go = (t: Tab) =>
    setParams(t === "general" ? {} : { tab: t }, { replace: true });
  return (
    <div className="page-frame detail">
      <PageHeader title="Settings" help="project.md#settings" />
      <div className="settings-layout">
        <nav className="settings-nav" aria-label="Settings sections">
          {GROUPS.map((g) => (
            <div key={g.label} className="settings-nav-group">
              <h2 className="settings-nav-h">{g.label}</h2>
              {g.tabs.map((t) => (
                <Link
                  key={t.id}
                  to={settingsUrl(
                    projectId,
                    t.id === "general" ? undefined : t.id,
                  )}
                  replace
                  aria-current={t.id === tab ? "page" : undefined}
                >
                  {t.label}
                </Link>
              ))}
            </div>
          ))}
        </nav>
        <label className="settings-pick">
          <span className="sr-only">Settings section</span>
          <select
            className="input"
            value={tab}
            onChange={(e) => go(e.target.value as Tab)}
          >
            {GROUPS.map((g) => (
              <optgroup key={g.label} label={g.label}>
                {g.tabs.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.label}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </label>
        <div className="settings-body">
          <h2 className="settings-tab-title">{label}</h2>
          {tab === "general" && <GeneralTab projectId={projectId} />}
          {tab === "costs" && <CostsTab projectId={projectId} />}
          {tab === "secrets" && <SecretsTab projectId={projectId} />}
          {tab === "buckets" && <BucketsTab projectId={projectId} />}
          {tab === "annotations" && <AnnotationsTab projectId={projectId} />}
          {tab === "integrations" && <IntegrationsTab projectId={projectId} />}
          {tab === "apikeys" && (
            <ApiKeysTab projectId={projectId} browserKey={<BrowserKey />} />
          )}
          {tab === "members" && <MembersTab projectId={projectId} />}
          {tab === "audit" && <AuditLogTab projectId={projectId} />}
          {tab === "retention" && <RetentionTab projectId={projectId} />}
          {tab === "sampling" && <SamplingTab projectId={projectId} />}
          {tab === "sensitive" && <SensitiveDataTab projectId={projectId} />}
          {tab === "delete" && <DeleteProjectTab projectId={projectId} />}
        </div>
      </div>
    </div>
  );
}

// ================================================================ general

function GeneralTab({ projectId }: { projectId: string }) {
  const stats = useQuery({
    queryKey: ["project-stats", projectId],
    queryFn: () => api.projectStats(projectId),
  });
  const info = useQuery({ queryKey: ["server-info"], queryFn: api.server });
  const s = stats.data;
  const otel = `${window.location.origin}${info.data?.otel_endpoint ?? "/otel/v1/traces"}`;
  return (
    <>
      <ErrorBox error={stats.error ?? info.error} />
      {stats.isLoading ? (
        <Spinner />
      ) : s ? (
        <SettingsSentence>
          {generalSentence(projectId, s, fmtBytes)}
        </SettingsSentence>
      ) : null}
      <SettingsSection title="This server">
        <SettingsRow
          label="Version"
          control={<span className="mono">{info.data?.version ?? "…"}</span>}
        />
        <SettingsRow
          label="Sign-in"
          detail={
            info.data?.auth_required
              ? "Every request needs an API key or an SSO session."
              : "No API keys are configured, so anyone who can reach the server can use it."
          }
          control={
            info.data ? (
              <StatusWords tone={info.data.auth_required ? "ok" : "warn"}>
                {info.data.auth_required ? "Required" : "Not required"}
              </StatusWords>
            ) : null
          }
        />
        <SettingsRow
          label="Traces endpoint (OTLP)"
          detail={<CopyText text={otel} label="endpoint" />}
        />
        <SettingsRow
          label="Project id"
          detail={<CopyText text={projectId} label="project id" />}
        />
      </SettingsSection>
      {s && (
        <SettingsSection
          title="Also stored"
          note="Dataset tables hold the rows of every dataset version; files are uploads and recorded media."
        >
          <SettingsRow
            label="Dataset tables"
            control={fmtNumber(s.table_count)}
          />
          <SettingsRow label="Files" control={fmtNumber(s.file_count)} />
        </SettingsSection>
      )}
    </>
  );
}

// ================================================================ costs

const PER_M = 1_000_000;
const fmtPerM = (perToken: number) => fmtCost(perToken * PER_M);

function CostsTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient();
  const costs = useQuery({
    queryKey: ["costs", projectId],
    queryFn: () => api.pricesQuery(projectId, true).then((r) => r.items),
  });
  const [search, setSearch] = useState("");
  const [level, setLevel] = useState<"" | "project" | "default">("");
  const [editing, setEditing] = useState<{
    entry?: PriceEntry;
    llmId?: string;
  } | null>(null);
  const { hidden, remove } = useUndoDelete();
  const all = useMemo(
    () => (costs.data ?? []).filter((c) => !hidden.has(c.id)),
    [costs.data, hidden],
  );
  const rows = useMemo(() => {
    const q = search.toLowerCase();
    return all
      .filter(
        (c) =>
          (!q ||
            c.llm_id.toLowerCase().includes(q) ||
            c.provider_id?.toLowerCase().includes(q)) &&
          (!level || c.pricing_level === level),
      )
      .sort((a, b) =>
        a.pricing_level === b.pricing_level
          ? a.llm_id.localeCompare(b.llm_id)
          : a.pricing_level === "project"
            ? -1
            : 1,
      );
  }, [all, search, level]);
  const overridden = new Set(
    all.filter((c) => c.pricing_level === "project").map((c) => c.llm_id),
  );
  const cell = (v: number | null) =>
    v == null ? (
      <span
        className="quiet-note"
        style={{ display: "inline" }}
        title="Not priced separately: billed at the input price"
      >
        as input
      </span>
    ) : (
      fmtPerM(v)
    );
  return (
    <>
      {costs.data && <SettingsSentence>{costsSentence(all)}</SettingsSentence>}
      <SettingsSection
        plain
        title="Prices per million tokens, in US dollars"
        action={
          <>
            <SearchField
              label="Search models"
              value={search}
              onChange={setSearch}
              width={200}
              placeholder="Search models"
            />
            <label>
              <span className="sr-only">Which prices</span>
              <select
                className="input"
                value={level}
                onChange={(e) => setLevel(e.target.value as typeof level)}
                style={{ height: 34 }}
              >
                <option value="">All prices</option>
                <option value="project">This project's</option>
                <option value="default">Built in</option>
              </select>
            </label>
            <button
              type="button"
              className="qbtn primary"
              onClick={() => setEditing({})}
            >
              Add a price
            </button>
          </>
        }
        note="Choose a model to set this project's price for it; a project price replaces the built-in one."
      >
        <ErrorBox error={costs.error} />
        {costs.isLoading ? (
          <Loading />
        ) : rows.length === 0 ? (
          <p className="quiet-note">No prices match.</p>
        ) : (
          <div className="s-table-box">
            <PlainTable caption="Model prices">
              <thead>
                <tr>
                  <th scope="col">Model</th>
                  <th scope="col" className="num">
                    Input
                  </th>
                  <th scope="col" className="num">
                    Output
                  </th>
                  <th
                    scope="col"
                    className="num"
                    title="Cache hits (cached input tokens)"
                  >
                    Cache read
                  </th>
                  <th
                    scope="col"
                    className="num"
                    title="Tokens written to the prompt cache"
                  >
                    Cache write
                  </th>
                  <th scope="col">Set by</th>
                  {overridden.size > 0 && (
                    <th scope="col">
                      <span className="sr-only">Delete</span>
                    </th>
                  )}
                </tr>
              </thead>
              <tbody>
                {rows.map((c) => {
                  const replaced =
                    c.pricing_level === "default" && overridden.has(c.llm_id);
                  return (
                    <tr
                      key={c.id}
                      className={replaced ? "muted-row" : undefined}
                    >
                      <td style={{ minWidth: 170, overflowWrap: "normal" }}>
                        <button
                          type="button"
                          className={`linkish cost-name${isOfflineModel(c.llm_id) ? "" : " mono"}`}
                          title={
                            c.pricing_level === "project"
                              ? `Edit the project price for ${c.llm_id}`
                              : `Set a price for ${c.llm_id} in this project`
                          }
                          aria-label={
                            c.pricing_level === "project"
                              ? `Edit the project price for ${c.llm_id}`
                              : `Set a project price for ${c.llm_id}`
                          }
                          onClick={() =>
                            setEditing(
                              c.pricing_level === "project"
                                ? { entry: c }
                                : { entry: c, llmId: c.llm_id },
                            )
                          }
                        >
                          {isOfflineModel(c.llm_id)
                            ? capFirstWord(modelDisplayName(c.llm_id))
                            : c.llm_id}
                        </button>
                        <div className="quiet-note">
                          {isOfflineModel(c.llm_id)
                            ? "offline, for tests"
                            : c.provider_id || "any provider"}
                          {c.effective_date
                            ? ` · from ${new Date(c.effective_date).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })}`
                            : ""}
                        </div>
                      </td>
                      <td className="num">{fmtPerM(c.prompt_token_cost)}</td>
                      <td className="num">
                        {fmtPerM(c.completion_token_cost)}
                      </td>
                      <td className="num">
                        {cell(c.cache_read_input_token_cost)}
                      </td>
                      <td className="num">
                        {cell(c.cache_creation_input_token_cost)}
                      </td>
                      <td style={{ whiteSpace: "nowrap" }}>
                        {c.pricing_level === "project"
                          ? "This project"
                          : replaced
                            ? "Built in (replaced)"
                            : "Built in"}
                      </td>
                      {overridden.size > 0 && (
                        <td>
                          {c.pricing_level === "project" && (
                            <div className="row-actions">
                              <button
                                type="button"
                                className="qbtn sm plain danger"
                                aria-label={`Delete the project price for ${c.llm_id}`}
                                title="Delete this project price; the built-in one applies again"
                                onClick={() =>
                                  remove(
                                    c.id,
                                    `Deleted the project price for ${c.llm_id}`,
                                    async () => {
                                      await api.pricesDelete(projectId, [c.id]);
                                      await qc.invalidateQueries({
                                        queryKey: ["costs", projectId],
                                      });
                                    },
                                  )
                                }
                              >
                                <Icon name="trash" size={14} />
                              </button>
                            </div>
                          )}
                        </td>
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </PlainTable>
          </div>
        )}
      </SettingsSection>
      {editing && (
        <CostModal
          projectId={projectId}
          entry={editing.entry}
          override={!!editing.llmId}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void qc.invalidateQueries({ queryKey: ["costs", projectId] });
          }}
        />
      )}
    </>
  );
}

// ================================================================ secrets & providers

const KNOWN_SECRETS = [
  { name: "OPENAI_API_KEY", label: "OpenAI" },
  { name: "ANTHROPIC_API_KEY", label: "Anthropic" },
  { name: "GEMINI_API_KEY", label: "Google Gemini" },
  { name: "MISTRAL_API_KEY", label: "Mistral" },
  { name: "GROQ_API_KEY", label: "Groq" },
];

/** "project" / "team acme" / "server" / "environment". */
function secretScopeWords(s: SecretEntry): string {
  if (s.source === "env") return "the server's environment";
  if (s.scope === "team") return `team ${s.scope_id}`;
  return s.scope === "project" ? "this project" : "the server";
}

function SecretsTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient();
  const secrets = useQuery({
    queryKey: ["secrets", projectId],
    queryFn: () => api.secretsList(projectId).then((r) => r.secrets),
  });
  const me = useQuery({ queryKey: ["auth-me"], queryFn: iamApi.me, retry: false });
  const team = projectId.split("/")[0];
  const serverAdmin = !!me.data?.server_admin;
  const teamAdmin = serverAdmin || me.data?.roles.entities[team] === "admin";
  const canManage = (s: SecretEntry) =>
    s.source === "db" && (s.scope === "project" || (s.scope === "team" ? teamAdmin : serverAdmin));
  const secretKey = (s: SecretEntry) => `secret:${s.scope}:${s.scope_id}:${s.name}`;
  const providers = useObjects(projectId, { kinds: ["provider"] });
  const providerModels = useObjects(projectId, { kinds: ["provider_model"] });
  const [setting, setSetting] = useState<string | null>(null);
  const [providerEdit, setProviderEdit] = useState<{
    initial?: WeftObject;
  } | null>(null);
  const [pmEdit, setPmEdit] = useState<{ initial?: WeftObject } | null>(null);
  const { hidden, remove } = useUndoDelete();
  const all = (secrets.data ?? []).filter((s) => !hidden.has(secretKey(s)));
  // The list is ordered narrowest scope first, which is the one used.
  const set = all.filter((s, i) => all.findIndex((x) => x.name === s.name) === i);
  const byName = new Map(set.map((s) => [s.name, s]));
  const provs = (providers.data ?? []).filter(
    (p) => !hidden.has(`provider:${p.name}`),
  );
  const providerKeyNames = provs
    .map((p) => p.value?.api_key_name)
    .filter(Boolean) as string[];
  const names = [
    ...new Set([
      ...KNOWN_SECRETS.map((k) => k.name),
      ...providerKeyNames,
      ...set.map((s) => s.name),
    ]),
  ];
  const refresh = () =>
    qc
      .invalidateQueries({ queryKey: ["secrets"] })
      .then(() => qc.invalidateQueries({ queryKey: ["llm-models"] }));
  const qcObjs = () =>
    qc.invalidateQueries({ queryKey: ["objects", projectId] });
  return (
    <>
      {secrets.data && providers.data && (
        <SettingsSentence>
          {secretsSentence(names, set, provs.length)}
        </SettingsSentence>
      )}
      <SettingsSection
        title="Provider keys"
        action={
          <button
            type="button"
            className="qbtn primary"
            onClick={() => setSetting("")}
          >
            Add a secret
          </button>
        }
        note={
          <>
            Used by the playground, LLM judges, scorers, automations and server-side evaluations. A value cannot be read back.
            A project secret is used only by this project. A team secret (every project of the team) and a server secret (every
            project) are used by the built-in providers; this project&rsquo;s own providers, scorers and automations can use them only
            toward the allowed hosts their admin sets on them.
          </>
        }
      >
        {secrets.error ? (
          <SettingsEmpty>
            <ErrorBox error={secrets.error} />
          </SettingsEmpty>
        ) : secrets.isLoading ? (
          <SettingsEmpty>
            <Spinner />
          </SettingsEmpty>
        ) : (
          names.map((n) => {
            const s = byName.get(n);
            const known = KNOWN_SECRETS.find((k) => k.name === n);
            const others = all.filter((x) => x.name === n && x !== s);
            const kindWords =
              known?.label ??
              (providerKeyNames.includes(n)
                ? "For a custom provider"
                : "Custom secret");
            return (
              <SettingsRow
                key={n}
                label={<span className="mono">{n}</span>}
                detail={
                  <>
                    {kindWords}
                    {s && s.allowed_hosts.length > 0 && (
                      <>
                        {" · only to "}
                        <span className="mono">{s.allowed_hosts.join(", ")}</span>
                      </>
                    )}
                    {s && s.source === "db" && (s.scope === "team" || s.scope === "server") && s.allowed_hosts.length === 0 && (
                      <span title="Projects' own providers, scorers, OAuth configurations and automations can use a team or server secret only toward its allowed hosts.">
                        {" · built-in providers only: no allowed hosts"}
                      </span>
                    )}
                    {others.length > 0 && (
                      <> · overrides the one in {others.map(secretScopeWords).join(" and ")}</>
                    )}
                  </>
                }
                control={
                  <>
                    {s ? (
                      <StatusWords tone="ok">
                        {s.source === "env"
                          ? "Set in the environment"
                          : `Set in ${secretScopeWords(s)}`}
                      </StatusWords>
                    ) : (
                      <StatusWords tone="muted">Not set</StatusWords>
                    )}
                    <button
                      type="button"
                      className="qbtn sm"
                      onClick={() => setSetting(n)}
                    >
                      {s ? "Replace" : "Set"}
                    </button>
                    {s && canManage(s) && (
                      <button
                        type="button"
                        className="qbtn sm plain danger"
                        aria-label={`Delete ${n} from ${secretScopeWords(s)}`}
                        title={`Delete ${n} from ${secretScopeWords(s)}`}
                        onClick={() =>
                          remove(secretKey(s), `Deleted ${n}`, async () => {
                            await api.secretsDelete({
                              name: n,
                              scope: s.scope,
                              project_id: s.scope === "server" ? undefined : projectId,
                              team: s.scope === "team" ? s.scope_id : undefined,
                            });
                            await refresh();
                          })
                        }
                      >
                        <Icon name="trash" size={14} />
                      </button>
                    )}
                  </>
                }
              />
            );
          })
        )}
      </SettingsSection>

      <SettingsSection
        title="Custom providers"
        action={
          <>
            {provs.length > 0 && (
              <button
                type="button"
                className="qbtn sm"
                onClick={() => setPmEdit({})}
              >
                Add a provider model
              </button>
            )}
            <button
              type="button"
              className="qbtn sm"
              onClick={() => setProviderEdit({})}
            >
              Add a provider
            </button>
          </>
        }
        note={
          <>
            Any OpenAI-compatible endpoint (vLLM, Ollama, LM Studio, a gateway).
            Its models are named <span className="mono">provider/model</span>.
          </>
        }
      >
        {providers.error ? (
          <SettingsEmpty>
            <ErrorBox error={providers.error} />
          </SettingsEmpty>
        ) : providers.isLoading ? (
          <SettingsEmpty>
            <Spinner />
          </SettingsEmpty>
        ) : !provs.length ? (
          <SettingsEmpty>
            Add a provider to use your own OpenAI-compatible endpoint.
          </SettingsEmpty>
        ) : (
          provs.map((p) => {
            const pms = (providerModels.data ?? []).filter(
              (m) => parseRef(m.value?.provider ?? "")?.name === p.name,
            );
            const key = p.value?.api_key_name as string | undefined;
            return (
              <SettingsRow
                key={p.name}
                label={
                  <Link to={objectUrl(projectId, p.name, p.digest)}>
                    {p.name}
                  </Link>
                }
                detail={
                  <>
                    <span className="mono">{p.value?.base_url}</span>
                    {" · "}
                    {key ? (
                      <>
                        key in <span className="mono">{key}</span>
                        {!byName.has(key) && <> (not set)</>}
                      </>
                    ) : (
                      "no key"
                    )}
                  </>
                }
                control={
                  <>
                    <button
                      type="button"
                      className="qbtn sm"
                      onClick={() => setProviderEdit({ initial: p })}
                    >
                      Edit
                    </button>
                    <button
                      type="button"
                      className="qbtn sm plain danger"
                      aria-label={`Delete provider ${p.name}`}
                      title={`Delete provider ${p.name}`}
                      onClick={() =>
                        remove(
                          `provider:${p.name}`,
                          `Deleted provider ${p.name}`,
                          async () => {
                            await api.objectDelete(projectId, p.name);
                            await qcObjs();
                          },
                        )
                      }
                    >
                      <Icon name="trash" size={14} />
                    </button>
                  </>
                }
              >
                {pms.length > 0 && (
                  <span className="row wrap" style={{ gap: 6 }}>
                    <span className="quiet-note" style={{ display: "inline" }}>
                      Models:
                    </span>
                    {pms.map((m) => (
                      <button
                        key={m.name}
                        type="button"
                        className="qbtn sm"
                        onClick={() => setPmEdit({ initial: m })}
                        title="Edit this provider model"
                      >
                        {p.name}/{m.value?.name ?? m.name}
                      </button>
                    ))}
                  </span>
                )}
              </SettingsRow>
            );
          })
        )}
      </SettingsSection>

      {setting !== null && (
        <SecretModal
          name={setting}
          projectId={projectId}
          serverAdmin={serverAdmin}
          teamAdmin={teamAdmin}
          current={setting ? byName.get(setting) : undefined}
          onClose={() => setSetting(null)}
          onSaved={() => {
            setSetting(null);
            void refresh();
          }}
        />
      )}
      {providerEdit && (
        <ProviderModal
          projectId={projectId}
          initial={providerEdit.initial}
          onClose={() => setProviderEdit(null)}
          onSaved={() => setProviderEdit(null)}
        />
      )}
      {pmEdit && (
        <ProviderModelModal
          projectId={projectId}
          providers={provs}
          initial={pmEdit.initial}
          onClose={() => setPmEdit(null)}
          onSaved={() => setPmEdit(null)}
        />
      )}
    </>
  );
}

// ================================================================ annotations

function AnnotationsTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient();
  const specs = useObjects(projectId, { kinds: ["annotation_spec"] });
  const [edit, setEdit] = useState<{ initial?: WeftObject } | null>(null);
  const { hidden, remove } = useUndoDelete();
  const list = (specs.data ?? []).filter((s) => !hidden.has(s.name));
  return (
    <>
      {specs.data && (
        <SettingsSentence>
          {annotationsSentence(list.map((s) => s.value?.name ?? s.name))}
        </SettingsSentence>
      )}
      <SettingsSection
        title="Fields reviewers fill in"
        action={
          <button
            type="button"
            className="qbtn primary"
            onClick={() => setEdit({})}
          >
            New annotation field
          </button>
        }
        note="Reviewers see these fields beside each step and in review queues."
      >
        {specs.error ? (
          <SettingsEmpty>
            <ErrorBox error={specs.error} />
          </SettingsEmpty>
        ) : specs.isLoading ? (
          <SettingsEmpty>
            <Spinner />
          </SettingsEmpty>
        ) : !list.length ? (
          <SettingsEmpty>
            No fields yet. Try a 1–5 quality score, or a “correct / incorrect”
            label.
          </SettingsEmpty>
        ) : (
          list.map((s) => {
            const scope = annotationSpecNames(s.value);
            return (
              <SettingsRow
                key={s.name}
                label={
                  <Link to={objectUrl(projectId, s.name, s.digest)}>
                    {s.value?.name ?? s.name}
                  </Link>
                }
                detail={
                  <>
                    {describeSchema(s.value?.field_schema)}
                    {s.value?.description ? ` · ${s.value.description}` : ""}
                    {" · "}
                    {scope.length
                      ? `only on ${scope.join(", ")}`
                      : "on every step"}
                    {s.value?.unique_among_creators
                      ? " · one per reviewer"
                      : ""}
                    {" · changed "}
                    <span title={fmtDate(s.created_at)}>
                      {relativeTime(s.created_at)}
                    </span>
                  </>
                }
                control={
                  <>
                    <button
                      type="button"
                      className="qbtn sm"
                      onClick={() => setEdit({ initial: s })}
                    >
                      Edit
                    </button>
                    <button
                      type="button"
                      className="qbtn sm plain danger"
                      aria-label={`Delete ${s.name}`}
                      title="Delete; existing annotations are kept, the field is no longer offered."
                      onClick={() =>
                        remove(
                          s.name,
                          `Deleted annotation field ${s.name}`,
                          async () => {
                            await api.objectDelete(projectId, s.name);
                            await qc.invalidateQueries({
                              queryKey: ["objects", projectId],
                            });
                          },
                        )
                      }
                    >
                      <Icon name="trash" size={14} />
                    </button>
                  </>
                }
              />
            );
          })
        )}
      </SettingsSection>
      {edit && (
        <AnnotationSpecModal
          projectId={projectId}
          initial={edit.initial}
          onClose={() => setEdit(null)}
        />
      )}
    </>
  );
}

// ================================================================ integrations

function IntegrationsTab({ projectId }: { projectId: string }) {
  const info = useQuery({ queryKey: ["server-info"], queryFn: api.server });
  const base = window.location.origin;
  const otel = `${base}${info.data?.otel_endpoint ?? "/otel/v1/traces"}`;
  const auth = info.data?.auth_required;
  const keyLine = auth ? '\nexport WEFT_API_KEY="<your api key>"' : "";
  const [lang, setLang] = useState<
    "python" | "typescript" | "otel-py" | "otel-node" | "env" | "curl"
  >("python");
  const snippets: Record<
    typeof lang,
    { code: string; language: string; title: string }
  > = {
    python: {
      language: "python",
      title: "pip install weft",
      code: `# export WEFT_BASE_URL="${base}"${keyLine.replace("\n", "\n# ")}
import weft

weft.init("${projectId}")

@weft.trace
def answer(question: str) -> str:
    return llm(question)   # nested calls and LLM usage are traced

answer("What is OpenTelemetry?")
`,
    },
    typescript: {
      language: "typescript",
      title: "npm install weft",
      code: `// WEFT_BASE_URL=${base}${auth ? "\n// WEFT_API_KEY=<your api key>" : ""}
import * as weft from 'weft'

await weft.init('${projectId}')

const answer = weft.trace(async (question: string) => {
  return await llm(question)
}, { name: 'answer' })

await answer('What is OpenTelemetry?')
`,
    },
    "otel-py": {
      language: "python",
      title:
        "pip install opentelemetry-sdk opentelemetry-exporter-otlp-proto-http",
      code: `from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter

exporter = OTLPSpanExporter(
    endpoint="${otel}",
    headers={"project_id": "${projectId}"${auth ? ', "Authorization": "Bearer <your api key>"' : ""}},
)
provider = TracerProvider()
provider.add_span_processor(BatchSpanProcessor(exporter))
trace.set_tracer_provider(provider)

# Any OpenInference / OpenLLMetry / gen_ai.* instrumentation now reports to Weft.
`,
    },
    "otel-node": {
      language: "typescript",
      title:
        "npm install @opentelemetry/sdk-node @opentelemetry/exporter-trace-otlp-proto",
      code: `import { NodeSDK } from '@opentelemetry/sdk-node'
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-proto'

const sdk = new NodeSDK({
  traceExporter: new OTLPTraceExporter({
    url: '${otel}',
    headers: { project_id: '${projectId}'${auth ? ", Authorization: 'Bearer <your api key>'" : ""} },
  }),
})
sdk.start()
`,
    },
    env: {
      language: "bash",
      title: "Standard OTLP environment variables (any language)",
      code: `export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="${otel}"
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL="http/protobuf"
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="project_id=${projectId}${auth ? ",Authorization=Bearer%20<your api key>" : ""}"
`,
    },
    curl: {
      language: "bash",
      title: "Record a span with the HTTP API",
      code: `curl -s ${base}/api/v1/spans/start \\
  -H 'Content-Type: application/json'${auth ? " \\\n  -H 'Authorization: Bearer <your api key>'" : ""} \\
  -d '{"project_id": "${projectId}", "name": "hello", "inputs": {"name": "world"}}'
# => {"id": "<span id>", "trace_id": "..."}

curl -s ${base}/api/v1/spans/end \\
  -H 'Content-Type: application/json'${auth ? " \\\n  -H 'Authorization: Bearer <your api key>'" : ""} \\
  -d '{"project_id": "${projectId}", "id": "<span id>", "output": "Hello, world"}'
`,
    },
  };
  const s = snippets[lang];
  const LANGS: { id: typeof lang; label: string }[] = [
    { id: "python", label: "Python SDK" },
    { id: "typescript", label: "TypeScript SDK" },
    { id: "otel-py", label: "OTLP Python" },
    { id: "otel-node", label: "OTLP Node" },
    { id: "env", label: "OTLP env vars" },
    { id: "curl", label: "HTTP" },
  ];
  return (
    <>
      <Sentence
        size="detail"
        parts={[
          `Send traces to ${projectId} with the Weft SDKs or any OpenTelemetry exporter, using the snippets below. Agents and coding tools are set up from `,
          { text: "the setup guide", to: "/help/setup.md" },
          ".",
        ]}
      />
      <Segmented
        label="How to send data"
        role="tabs"
        panelId="send-snippet"
        value={lang}
        onChange={setLang}
        options={LANGS}
        size="sm"
      />
      <div
        className="s-code-box"
        role="tabpanel"
        id="send-snippet"
        aria-labelledby={segmentedTabId("send-snippet", lang)}
      >
        <div className="s-code-head">
          <span className="mono">{s.title}</span>
          <CopyButton
            text={s.code}
            label={`Copy the ${LANGS.find((l) => l.id === lang)!.label} snippet`}
          />
        </div>
        <pre className="s-code" tabIndex={0}>
          {s.code}
        </pre>
      </div>
      <SettingsSection title="Addresses">
        <SettingsRow
          label="Server URL"
          detail={<CopyText text={base} label="server URL" />}
        />
        <SettingsRow
          label="OTLP traces"
          detail={<CopyText text={otel} label="OTLP endpoint" />}
        />
        <SettingsRow
          label="Project"
          detail={<CopyText text={projectId} label="project id" />}
        />
      </SettingsSection>
    </>
  );
}

// ================================================================ the browser's API key

function BrowserKey() {
  const qc = useQueryClient();
  const [stored, setStored] = useState<string | null>(getApiKey());
  const [value, setValue] = useState("");
  const info = useQuery({ queryKey: ["server-info"], queryFn: api.server });
  const check = useQuery({
    queryKey: ["auth-check-settings", stored],
    queryFn: api.authCheck,
    retry: false,
  });
  const ok = check.isSuccess;
  const unauthorized =
    check.error instanceof ApiError && check.error.status === 401;
  const mask = (k: string) =>
    k.length <= 8 ? "••••••••" : `${k.slice(0, 4)}••••••••${k.slice(-4)}`;
  return (
    <SettingsSection
      title="This browser"
      note="The key is kept in this browser only and sent with every request the UI makes."
    >
      <SettingsRow
        label="Stored key"
        detail={
          stored ? (
            <span className="mono">{mask(stored)}</span>
          ) : info.data && !info.data.auth_required ? (
            "None needed."
          ) : (
            "None"
          )
        }
        control={
          <>
            {check.isLoading ? (
              <Spinner />
            ) : ok ? (
              <StatusWords tone="ok">Authorized</StatusWords>
            ) : unauthorized ? (
              <StatusWords tone="error">Not authorized</StatusWords>
            ) : (
              <StatusWords tone="muted">Unknown</StatusWords>
            )}
            {stored && (
              <button
                type="button"
                className="qbtn sm"
                onClick={() => {
                  setApiKey(null);
                  setStored(null);
                  void qc.invalidateQueries();
                }}
              >
                Clear
              </button>
            )}
          </>
        }
      />
      <SettingsRow
        label="Use another key"
        labelFor="browser-key"
        control={
          <form
            className="row wrap"
            onSubmit={(e) => {
              e.preventDefault();
              if (!value.trim()) return;
              setApiKey(value.trim());
              setStored(value.trim());
              setValue("");
              void qc.invalidateQueries();
            }}
          >
            <input
              id="browser-key"
              className="input mono"
              type="password"
              autoComplete="off"
              placeholder="Paste an API key"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              style={{ width: 220, maxWidth: "100%" }}
            />
            <button className="qbtn sm" type="submit" disabled={!value.trim()}>
              Save
            </button>
          </form>
        }
      />
    </SettingsSection>
  );
}

/** "offline echo model" → "Offline echo model". */
function capFirstWord(t: string): string {
  return t ? t[0].toUpperCase() + t.slice(1) : t;
}
