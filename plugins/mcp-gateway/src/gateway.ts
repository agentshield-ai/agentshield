import { randomUUID } from "node:crypto";

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import type { Transport } from "@modelcontextprotocol/sdk/shared/transport.js";
import {
  CallToolRequestSchema,
  CallToolResultSchema,
  ListToolsRequestSchema,
  type ServerCapabilities,
} from "@modelcontextprotocol/sdk/types.js";

import { AgentShieldClient } from "./client.js";
import { GatewayPipeline } from "./pipeline.js";
import type { AgentShieldConfig, Logger } from "./types.js";

const GATEWAY_VERSION = "1.0.0";

/**
 * Build the downstream MCP client transport from configuration.
 *
 * stdio launches a child process; http connects to a Streamable HTTP endpoint.
 * The child process environment is the gateway's own environment merged with
 * any explicit overrides — we never silently leak unexpected variables beyond
 * the inherited set the SDK already filters.
 */
function buildDownstreamTransport(config: AgentShieldConfig, logger: Logger): Transport {
  const d = config.downstream;
  if (d.transport === "http") {
    if (!d.url) {
      throw new Error("downstream.transport is 'http' but no downstream.url was configured");
    }
    logger.info(`Connecting to downstream MCP server over HTTP: ${d.url}`);
    return new StreamableHTTPClientTransport(new URL(d.url));
  }

  if (!d.command) {
    throw new Error("downstream.transport is 'stdio' but no downstream.command was configured");
  }
  logger.info(`Launching downstream MCP server: ${d.command} ${d.args.join(" ")}`.trim());
  return new StdioClientTransport({
    command: d.command,
    args: d.args,
    env: { ...(process.env as Record<string, string>), ...d.env },
    stderr: "inherit",
  });
}

/**
 * AgentShield MCP security gateway.
 *
 * A transparent proxy: it exposes an MCP {@link Server} to the upstream agent
 * and holds a {@link Client} to one downstream MCP server. `tools/list` is
 * forwarded verbatim; `tools/call` is evaluated by the {@link GatewayPipeline}
 * and either blocked (returned as an isError tool result carrying the block
 * reason) or forwarded. `initialize`/shutdown are mapped to lifecycle events.
 */
export class McpGateway {
  private readonly config: AgentShieldConfig;
  private readonly logger: Logger;
  private readonly pipeline: GatewayPipeline;
  private readonly downstream: Client;
  private server: Server | null = null;
  private sessionId: string | null = null;
  private agentId: string | null = null;
  private lifecycleStarted = false;

  constructor(config: AgentShieldConfig, logger: Logger) {
    this.config = config;
    this.logger = logger;
    const httpClient = new AgentShieldClient(config, logger);
    this.pipeline = new GatewayPipeline(config, httpClient, logger);
    this.downstream = new Client(
      { name: "agentshield-mcp-gateway", version: GATEWAY_VERSION },
      { capabilities: {} },
    );
  }

  /**
   * Connect to the downstream server, mirror its identity/capabilities onto the
   * upstream-facing server, wire request handlers, and run the startup health
   * check (non-blocking).
   */
  async start(upstreamTransport: Transport): Promise<void> {
    const downstreamTransport = buildDownstreamTransport(this.config, this.logger);
    await this.downstream.connect(downstreamTransport);

    const downstreamInfo = this.downstream.getServerVersion();
    const downstreamCaps = this.downstream.getServerCapabilities() ?? {};
    const instructions = this.downstream.getInstructions();

    this.server = new Server(
      {
        name: downstreamInfo?.name ?? "agentshield-mcp-gateway",
        version: downstreamInfo?.version ?? GATEWAY_VERSION,
      },
      {
        // Advertise exactly what the downstream advertises so the agent sees a
        // faithful passthrough; we only intervene on tools/call.
        capabilities: downstreamCaps as ServerCapabilities,
        ...(instructions ? { instructions } : {}),
      },
    );

    this.registerHandlers(this.server);

    // Map MCP initialize -> lifecycle session_start (contract Section 1.3).
    this.server.oninitialized = () => {
      const clientInfo = this.server?.getClientVersion();
      this.agentId = clientInfo?.name ?? null;
      this.sessionId = `mcp-${Date.now()}-${randomUUID().slice(0, 8)}`;
      this.lifecycleStarted = true;
      this.pipeline.recordLifecycle("session_start", this.ctx());
      this.logger.info(
        `MCP session started (agent=${this.agentId ?? "unknown"}, session=${this.sessionId})`,
      );
    };

    // Map transport close (shutdown) -> lifecycle session_end.
    this.server.onclose = () => {
      void this.emitSessionEnd();
    };

    await this.server.connect(upstreamTransport);

    // Step 10: non-blocking startup health check.
    void this.pipeline.startupHealthCheck();
  }

  /** Disconnect from both transports and emit session_end if needed. */
  async stop(): Promise<void> {
    await this.emitSessionEnd();
    try {
      await this.server?.close();
    } catch (err: unknown) {
      this.logger.debug(`Error closing upstream server: ${String(err)}`);
    }
    try {
      await this.downstream.close();
    } catch (err: unknown) {
      this.logger.debug(`Error closing downstream client: ${String(err)}`);
    }
  }

  private async emitSessionEnd(): Promise<void> {
    if (this.lifecycleStarted) {
      this.lifecycleStarted = false;
      this.pipeline.recordLifecycle("session_end", this.ctx());
    }
  }

  private ctx(): { agentId: string | null; sessionId: string | null } {
    return { agentId: this.agentId, sessionId: this.sessionId };
  }

  private registerHandlers(server: Server): void {
    // tools/list -> transparent passthrough (contract: list passes through).
    server.setRequestHandler(ListToolsRequestSchema, async (request) => {
      return this.downstream.listTools(request.params);
    });

    // tools/call -> evaluate, then block or forward.
    server.setRequestHandler(CallToolRequestSchema, async (request) => {
      const toolName = request.params.name;
      const args = (request.params.arguments ?? {}) as Record<string, unknown>;

      const decision = await this.pipeline.evaluateToolCall(toolName, args, this.ctx());

      if (!decision.allowed) {
        // Return a tool result with isError=true so the agent receives the
        // block reason in-band rather than a protocol error it might retry.
        const prefix = decision.overridable
          ? "AgentShield requires approval"
          : "AgentShield blocked this tool call";
        this.logger.warn(`Denied ${toolName}: ${decision.reason ?? prefix}`);
        return {
          content: [
            {
              type: "text" as const,
              text: `${prefix}: ${decision.reason ?? "policy violation"}`,
            },
          ],
          isError: true,
        };
      }

      // Forward to the downstream server and time it for the audit report.
      const startedAt = Date.now();
      try {
        const result = await this.downstream.callTool(request.params, CallToolResultSchema);
        const durationMs = Date.now() - startedAt;
        const isError = result.isError === true;
        this.pipeline.recordResult(toolName, args, this.ctx(), result.content, {
          isError,
          errorMessage: isError ? this.extractText(result.content) : null,
          durationMs,
        });
        return result;
      } catch (err: unknown) {
        const durationMs = Date.now() - startedAt;
        const message = err instanceof Error ? err.message : String(err);
        this.pipeline.recordResult(toolName, args, this.ctx(), null, {
          isError: true,
          errorMessage: message,
          durationMs,
        });
        // Surface the downstream error to the agent unchanged.
        throw err;
      }
    });
  }

  private extractText(content: unknown): string | null {
    if (!Array.isArray(content)) {
      return null;
    }
    const texts = content
      .filter(
        (c): c is { type: string; text: string } =>
          typeof c === "object" && c !== null && (c as { type?: string }).type === "text",
      )
      .map((c) => c.text);
    return texts.length > 0 ? texts.join("\n") : null;
  }
}
