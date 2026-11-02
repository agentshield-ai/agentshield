/**
 * Send OpenTelemetry spans to Weft (the TypeScript twin of `weft.otel`).
 *
 * `otlpEndpoint()` / `otlpHeaders()` give the exporter settings for any OTel
 * setup; `configure()` builds a NodeTracerProvider with an OTLP/HTTP exporter
 * when `@opentelemetry/sdk-trace-node` and
 * `@opentelemetry/exporter-trace-otlp-proto` (or `-http`) are installed.
 *
 * ```ts
 * import { otel } from 'weft-sdk';
 * const provider = await otel.configure({ project: 'my-team/my-app' });
 * ```
 */
import { DEFAULT_SERVER_URL } from './client.js';
import { splitProject } from './refs.js';

/** `<server>/otel/v1/traces` (server from the argument, WEFT_SERVER_URL or the default). */
export function otlpEndpoint(serverUrl?: string): string {
  return (serverUrl ?? process.env.WEFT_SERVER_URL ?? DEFAULT_SERVER_URL).replace(/\/+$/, '') + '/otel/v1/traces';
}

/** Headers carrying the project (and API key, from the argument or WEFT_API_KEY). */
export function otlpHeaders(project: string, apiKey?: string): Record<string, string> {
  const [entity, proj] = splitProject(project);
  const h: Record<string, string> = { project_id: `${entity}/${proj}` };
  const key = apiKey ?? process.env.WEFT_API_KEY;
  if (key) h.Authorization = `Bearer ${key}`;
  return h;
}

const dynamicImport = (m: string): Promise<any> => import(m);

async function firstModule(names: string[]): Promise<any> {
  let last: unknown;
  for (const n of names) {
    try {
      return await dynamicImport(n);
    } catch (e) {
      last = e;
    }
  }
  throw new Error(`weft otel.configure needs one of ${names.join(', ')} (npm install it): ${(last as Error)?.message}`);
}

/**
 * Create (and by default register) a tracer provider exporting OTLP/HTTP to Weft.
 * Resolves to the provider.
 */
export async function configure(
  opts: { project?: string; serverUrl?: string; apiKey?: string; serviceName?: string; register?: boolean; batch?: boolean } = {},
): Promise<any> {
  const project = opts.project ?? process.env.WEFT_PROJECT;
  if (!project) throw new Error('otel.configure requires a project (or WEFT_PROJECT)');
  const sdk = await firstModule(['@opentelemetry/sdk-trace-node', '@opentelemetry/sdk-trace-base']);
  const exp = await firstModule(['@opentelemetry/exporter-trace-otlp-proto', '@opentelemetry/exporter-trace-otlp-http']);
  const exporter = new exp.OTLPTraceExporter({ url: otlpEndpoint(opts.serverUrl), headers: otlpHeaders(project, opts.apiKey) });
  const Processor = opts.batch === false ? sdk.SimpleSpanProcessor : sdk.BatchSpanProcessor;
  const Provider = sdk.NodeTracerProvider ?? sdk.BasicTracerProvider;
  let resource: unknown;
  try {
    const res = await dynamicImport('@opentelemetry/resources');
    const attrs = { 'service.name': opts.serviceName ?? 'weft-app', 'weft.project': otlpHeaders(project).project_id };
    resource = res.resourceFromAttributes ? res.resourceFromAttributes(attrs) : new res.Resource(attrs);
  } catch {
    resource = undefined;
  }
  const processor = new Processor(exporter);
  let provider: any;
  if (typeof Provider.prototype.addSpanProcessor === 'function') {
    provider = new Provider(resource ? { resource } : {}); // OTel JS 1.x
    provider.addSpanProcessor(processor);
  } else {
    provider = new Provider({ ...(resource ? { resource } : {}), spanProcessors: [processor] }); // 2.x
  }
  if (opts.register !== false && typeof provider.register === 'function') provider.register();
  return provider;
}
