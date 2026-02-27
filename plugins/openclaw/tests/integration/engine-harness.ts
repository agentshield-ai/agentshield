import { execSync, spawn } from "node:child_process";
import type { ChildProcess } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

export type EngineHarnessOptions = {
  /** Path to the agentshield binary. Default: env AGENTSHIELD_BINARY or bin/agentshield at repo root */
  binary?: string;
  /** Path to the rules directory. Default: rules/ at repo root */
  rules?: string;
  /** Port to listen on. Default: 38433 */
  port?: number;
  /** Max seconds to wait for health check. Default: 15 */
  startupTimeoutSec?: number;
};

/**
 * Manages the AgentShield engine lifecycle for integration tests.
 *
 * Supports two modes selected via AGENTSHIELD_ENGINE_MODE env var:
 * - native (default): spawns the Go binary directly
 * - docker: runs the engine in a Docker container
 */
export class EngineHarness {
  readonly port: number;
  readonly token: string;

  private process: ChildProcess | null = null;
  private containerId: string | null = null;
  private tmpDir: string | null = null;
  private mode: "native" | "docker";

  private binary: string;
  private rules: string;
  private startupTimeoutSec: number;

  constructor(opts: EngineHarnessOptions = {}) {
    this.port = opts.port ?? 38433;
    this.token = randomUUID();
    this.mode =
      (process.env.AGENTSHIELD_ENGINE_MODE as "native" | "docker") ?? "native";

    // Resolve paths relative to the plugin root (plugins/openclaw/)
    const pluginRoot = join(import.meta.dirname, "../..");
    const repoRoot = join(pluginRoot, "../..");

    this.binary =
      opts.binary ??
      process.env.AGENTSHIELD_BINARY ??
      join(repoRoot, "bin/agentshield");
    this.rules = opts.rules ?? join(repoRoot, "rules");
    this.startupTimeoutSec = opts.startupTimeoutSec ?? 15;
  }

  async start(): Promise<void> {
    if (this.mode === "docker") {
      this.startDocker();
    } else {
      this.startNative();
    }
    await this.waitForHealth();
  }

  async stop(): Promise<void> {
    if (this.mode === "docker") {
      this.stopDocker();
    } else {
      this.stopNative();
    }
    if (this.tmpDir) {
      rmSync(this.tmpDir, { recursive: true, force: true });
      this.tmpDir = null;
    }
  }

  private startNative(): void {
    this.tmpDir = mkdtempSync(join(tmpdir(), "agentshield-test-"));

    const configPath = join(this.tmpDir, "config.yaml");
    const dbPath = join(this.tmpDir, "agentshield.db");

    const config = [
      "server:",
      '  addr: "127.0.0.1"',
      `  port: ${this.port}`,
      "auth:",
      `  token: "${this.token}"`,
      "rules:",
      `  dir: "${this.rules}"`,
      "  hot_reload: false",
      "store:",
      `  sqlite_path: "${dbPath}"`,
      'evaluation_mode: "enforce"',
      'log_level: "warn"',
    ].join("\n");

    writeFileSync(configPath, config, "utf-8");

    this.process = spawn(this.binary, ["serve", "--config", configPath], {
      stdio: ["ignore", "pipe", "pipe"],
    });

    // Fail fast if the process exits unexpectedly
    this.process.on("exit", (code, signal) => {
      if (code !== null && code !== 0) {
        console.error(`Engine exited with code ${code} (signal: ${signal})`);
      }
    });
  }

  private stopNative(): void {
    if (this.process && !this.process.killed) {
      this.process.kill("SIGTERM");
      this.process = null;
    }
  }

  private startDocker(): void {
    const repoRoot = join(import.meta.dirname, "../../../..");
    const dockerfilePath = join(repoRoot, "docker/engine.Dockerfile");

    // Build the image
    execSync(
      `docker build -t agentshield-engine:test -f "${dockerfilePath}" "${repoRoot}"`,
      { stdio: "inherit" },
    );

    // Create temp dir for config
    this.tmpDir = mkdtempSync(join(tmpdir(), "agentshield-test-"));
    const configPath = join(this.tmpDir, "config.yaml");

    const config = [
      "server:",
      '  addr: "0.0.0.0"',
      "  port: 8433",
      "auth:",
      `  token: "${this.token}"`,
      "rules:",
      '  dir: "/rules"',
      "  hot_reload: false",
      "store:",
      '  sqlite_path: "/tmp/agentshield.db"',
      'evaluation_mode: "enforce"',
      'log_level: "warn"',
    ].join("\n");

    writeFileSync(configPath, config, "utf-8");

    const output = execSync(
      [
        "docker run -d --rm",
        `-p ${this.port}:8433`,
        `-v "${configPath}":/config.yaml:ro`,
        "agentshield-engine:test",
        "serve --config /config.yaml",
      ].join(" "),
      { encoding: "utf-8" },
    ).trim();

    this.containerId = output;
  }

  private stopDocker(): void {
    if (this.containerId) {
      try {
        execSync(`docker stop ${this.containerId}`, { stdio: "ignore" });
      } catch {
        // Container may have already stopped
      }
      this.containerId = null;
    }
  }

  private async waitForHealth(): Promise<void> {
    const url = `http://127.0.0.1:${this.port}/api/v1/health`;
    const deadline = Date.now() + this.startupTimeoutSec * 1000;

    while (Date.now() < deadline) {
      try {
        const resp = await fetch(url, {
          headers: { Authorization: `Bearer ${this.token}` },
          signal: AbortSignal.timeout(1000),
        });
        if (resp.ok) return;
      } catch {
        // Engine not ready yet
      }
      await new Promise((r) => setTimeout(r, 500));
    }

    throw new Error(
      `Engine failed health check on port ${this.port} within ${this.startupTimeoutSec}s`,
    );
  }
}
