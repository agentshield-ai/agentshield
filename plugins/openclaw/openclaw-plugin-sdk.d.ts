declare module "openclaw/plugin-sdk" {
  export interface OpenClawPluginApi {
    pluginConfig: unknown;
    logger: {
      debug?: (message: string) => void;
      info: (message: string) => void;
      warn: (message: string) => void;
      error: (message: string) => void;
    };
    on: (event: string, handler: (...args: any[]) => any, options?: { priority?: number }) => void;
    runtime: {
      system: {
        enqueueSystemEvent: (text: string, options: { sessionKey: string; contextKey?: string | null }) => boolean;
      };
    };
  }
}
