#!/usr/bin/env node
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { randomUUID } from "node:crypto";

import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";

import { loadConfigFile, parseConfig } from "./config.js";
import { McpGateway } from "./gateway.js";
import { createLogger } from "./index.js";
import type { AgentShieldConfig, Logger } from "./types.js";

/**
 * Parse `--config <path>` / `--log-level <level>` from argv.
 *
 * Bare CLI args after `--` are treated as the downstream stdio launch command
 * so the gateway can be dropped in front of an existing MCP server invocation:
 *   agentshield-mcp-gateway -- npx -y @modelcontextprotocol/server-filesystem /data
 */
function parseArgs(argv: string[]): {
  configPath?: string;
  logLevel?: "debug" | "info" | "warn" | "error";
  downstreamCmd?: string;
  downstreamArgs: string[];
} {
  const out: {
    configPath?: string;
    logLevel?: "debug" | "info" | "warn" | "error";
    downstreamCmd?: string;
    downstreamArgs: string[];
  } = { downstreamArgs: [] };

  const dashDash = argv.indexOf("--");
  const head = dashDash === -1 ? argv : argv.slice(0, dashDash);
  const tail = dashDash === -1 ? [] : argv.slice(dashDash + 1);

  for (let i = 0; i < head.length; i += 1) {
    const a = head[i];
    if (a === "--config" && i + 1 < head.length) {
      out.configPath = head[(i += 1)];
    } else if (a === "--log-level" && i + 1 < head.length) {
      const lvl = head[(i += 1)];
      if (lvl === "debug" || lvl === "info" || lvl === "warn" || lvl === "error") {
        out.logLevel = lvl;
      }
    }
  }

  if (tail.length > 0) {
    out.downstreamCmd = tail[0];
    out.downstreamArgs = tail.slice(1);
  }

  return out;
}

async function serveStdio(config: AgentShieldConfig, logger: Logger): Promise<void> {
  const gateway = new McpGateway(config, logger);
  const transport = new StdioServerTransport();

  const shutdown = async (): Promise<void> => {
    logger.info("Shutting down MCP gateway");
    await gateway.stop();
    process.exit(0);
  };
  process.on("SIGINT", () => void shutdown());
  process.on("SIGTERM", () => void shutdown());

  await gateway.start(transport);
  logger.info("AgentShield MCP gateway running on stdio");
}

/**
 * Serve over Streamable HTTP. Each session (identified by the
 * `mcp-session-id` header, assigned on initialize) gets its own gateway and
 * downstream connection so multiple agents can be proxied concurrently.
 */
async function serveHttp(config: AgentShieldConfig, logger: Logger): Promise<void> {
  type Session = { gateway: McpGateway; transport: StreamableHTTPServerTransport };
  const sessions = new Map<string, Session>();

  const httpServer = createServer((req: IncomingMessage, res: ServerResponse) => {
    void handleHttp(req, res).catch((err: unknown) => {
      logger.error(`HTTP handler error: ${String(err)}`);
      if (!res.headersSent) {
        res.writeHead(500).end();
      }
    });
  });

  async function handleHttp(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const sessionId = req.headers["mcp-session-id"] as string | undefined;

    if (sessionId && sessions.has(sessionId)) {
      await (sessions.get(sessionId) as Session).transport.handleRequest(req, res);
      return;
    }

    if (req.method === "POST" && !sessionId) {
      // New session: assign an id, build a gateway, and route through it.
      const transport = new StreamableHTTPServerTransport({
        sessionIdGenerator: () => randomUUID(),
        onsessioninitialized: (id: string) => {
          sessions.set(id, { gateway, transport });
        },
      });
      transport.onclose = () => {
        const id = transport.sessionId;
        if (id) {
          sessions.delete(id);
        }
        void gateway.stop();
      };
      const gateway = new McpGateway(config, logger);
      await gateway.start(transport);
      await transport.handleRequest(req, res);
      return;
    }

    res.writeHead(400).end(JSON.stringify({ error: "missing or invalid mcp-session-id" }));
  }

  await new Promise<void>((resolve) => {
    httpServer.listen(config.serve.port, config.serve.host, () => resolve());
  });
  logger.info(
    `AgentShield MCP gateway listening on http://${config.serve.host}:${config.serve.port}`,
  );

  const shutdown = (): void => {
    logger.info("Shutting down MCP gateway");
    for (const s of sessions.values()) {
      void s.gateway.stop();
    }
    httpServer.close();
    process.exit(0);
  };
  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);
}

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2));
  const logger = createLogger(args.logLevel ?? (process.env.AGENTSHIELD_LOG_LEVEL as never) ?? "info");

  const fileCfg = loadConfigFile(args.configPath ?? process.env.MCP_GATEWAY_CONFIG);
  let config = parseConfig(fileCfg);

  // CLI downstream command (after `--`) overrides config for stdio downstream.
  if (args.downstreamCmd) {
    config = {
      ...config,
      downstream: {
        ...config.downstream,
        transport: "stdio",
        command: args.downstreamCmd,
        args: args.downstreamArgs,
      },
    };
  }

  if (!config.enabled) {
    logger.warn("AgentShield gateway disabled by config; downstream will NOT be proxied. Exiting.");
    process.exit(0);
  }

  if (config.serve.transport === "http") {
    await serveHttp(config, logger);
  } else {
    await serveStdio(config, logger);
  }
}

void main().catch((err: unknown) => {
  process.stderr.write(`[agentshield] FATAL ${String(err)}\n`);
  process.exit(1);
});
