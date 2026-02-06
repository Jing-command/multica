import { existsSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join, dirname } from "node:path";
import { homedir } from "node:os";
import JSON5 from "json5";
import { DATA_DIR } from "../shared/paths.js";

type ProviderConfig = {
  // API Key authentication
  apiKey?: string | undefined;
  // OAuth authentication
  oauthToken?: string | undefined;
  oauthRefreshToken?: string | undefined;
  oauthExpiresAt?: number | undefined;
  // Common
  baseUrl?: string | undefined;
  model?: string | undefined;
};

type ToolConfig = {
  apiKey?: string | undefined;
  baseUrl?: string | undefined;
  model?: string | undefined;
};

export type CredentialsConfig = {
  version?: number | undefined;
  llm?: {
    provider?: string | undefined;
    providers?: Record<string, ProviderConfig> | undefined;
    /** Explicit profile ordering per provider (e.g. { anthropic: ["anthropic", "anthropic:backup"] }) */
    order?: Record<string, string[]> | undefined;
  } | undefined;
  tools?: Record<string, ToolConfig> | undefined;
  /** Channel plugin configs (telegram, discord, etc.) */
  channels?: Record<string, Record<string, Record<string, unknown>> | undefined> | undefined;
};

type SkillsEnvConfig = {
  env?: Record<string, string> | undefined;
};

const DEFAULT_CREDENTIALS_PATH = join(DATA_DIR, "credentials.json5");
const DEFAULT_SKILLS_ENV_PATH = join(DATA_DIR, "skills.env.json5");

function expandHome(value: string): string {
  if (value === "~") return homedir();
  if (value.startsWith("~/")) {
    return join(homedir(), value.slice(2));
  }
  return value;
}

function isTestEnv(): boolean {
  return (
    process.env.NODE_ENV === "test" ||
    process.env.VITEST !== undefined ||
    process.env.VITEST_WORKER_ID !== undefined
  );
}

function isString(value: unknown): value is string {
  return typeof value === "string";
}

function setEnvValue(target: Record<string, string>, key: string, value: unknown): void {
  if (isString(value)) {
    target[key] = value;
  }
}

function applyEnvMap(target: Record<string, string>, env?: Record<string, string>): void {
  if (!env) return;
  for (const [key, value] of Object.entries(env)) {
    setEnvValue(target, key, value);
  }
}

export function getCredentialsPath(): string {
  const raw = process.env.SMC_CREDENTIALS_PATH ?? DEFAULT_CREDENTIALS_PATH;
  return expandHome(raw);
}

export function getSkillsEnvPath(): string {
  const raw = process.env.SMC_SKILLS_ENV_PATH ?? DEFAULT_SKILLS_ENV_PATH;
  return expandHome(raw);
}

export class CredentialManager {
  private corePath: string | null = null;
  private skillsPath: string | null = null;
  private disabledState: boolean | null = null;
  private coreConfig: CredentialsConfig | null = null;
  private skillsConfig: SkillsEnvConfig | null = null;
  private resolvedSkillsEnv: Record<string, string> | null = null;

  private isDisabled(): boolean {
    if (process.env.SMC_CREDENTIALS_DISABLE === "1") return true;
    return isTestEnv();
  }

  private loadCore(): void {
    const path = getCredentialsPath();
    const disabled = this.isDisabled();

    if (this.corePath === path && this.disabledState === disabled && this.coreConfig) {
      return;
    }

    this.corePath = path;
    this.disabledState = disabled;
    this.coreConfig = null;

    if (disabled) return;
    if (!existsSync(path)) return;

    const raw = readFileSync(path, "utf8");
    try {
      this.coreConfig = JSON5.parse(raw) as CredentialsConfig;
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      throw new Error(`Failed to parse credentials file (${path}): ${message}`);
    }
  }

  private loadSkillsEnv(): void {
    const path = getSkillsEnvPath();
    const disabled = this.isDisabled();

    if (this.skillsPath === path && this.disabledState === disabled && this.resolvedSkillsEnv) {
      return;
    }

    this.skillsPath = path;
    this.disabledState = disabled;
    this.skillsConfig = null;
    this.resolvedSkillsEnv = null;

    if (disabled) return;
    if (!existsSync(path)) return;

    const raw = readFileSync(path, "utf8");
    try {
      this.skillsConfig = JSON5.parse(raw) as SkillsEnvConfig;
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      throw new Error(`Failed to parse skills env file (${path}): ${message}`);
    }
  }

  private buildSkillsEnv(): Record<string, string> {
    const env: Record<string, string> = {};
    if (!this.skillsConfig) return env;

    applyEnvMap(env, this.skillsConfig.env);

    return env;
  }

  private getResolvedSkillsEnv(): Record<string, string> {
    this.loadSkillsEnv();
    if (!this.resolvedSkillsEnv) {
      this.resolvedSkillsEnv = this.buildSkillsEnv();
    }
    return this.resolvedSkillsEnv;
  }

  getLlmProvider(): string | undefined {
    this.loadCore();
    return this.coreConfig?.llm?.provider;
  }

  getLlmProviderConfig(provider: string): ProviderConfig | undefined {
    this.loadCore();
    return this.coreConfig?.llm?.providers?.[provider];
  }

  getToolConfig(toolName: string): ToolConfig | undefined {
    this.loadCore();
    return this.coreConfig?.tools?.[toolName];
  }

  getEnv(name: string): string | undefined {
    const resolved = this.getResolvedSkillsEnv();
    if (Object.prototype.hasOwnProperty.call(resolved, name)) {
      return resolved[name];
    }
    return process.env[name];
  }

  hasEnv(name: string): boolean {
    const resolved = this.getResolvedSkillsEnv();
    if (Object.prototype.hasOwnProperty.call(resolved, name)) {
      return true;
    }
    return name in process.env;
  }

  /**
   * Get explicit profile order for a provider from credentials.json5 `llm.order`.
   * Returns undefined if no explicit order is configured.
   */
  getLlmOrder(provider: string): string[] | undefined {
    this.loadCore();
    return this.coreConfig?.llm?.order?.[provider];
  }

  /**
   * List all profile IDs from `llm.providers` that belong to a given provider.
   * A profile matches if its key equals the provider exactly or starts with "provider:".
   */
  listProfileIdsForProvider(provider: string): string[] {
    this.loadCore();
    const providers = this.coreConfig?.llm?.providers;
    if (!providers) return [];

    const prefix = `${provider}:`;
    return Object.keys(providers).filter(
      (key) => key === provider || key.startsWith(prefix),
    );
  }

  /** Get channel plugin configs from credentials.json5 `channels` section. */
  getChannelsConfig(): Record<string, Record<string, Record<string, unknown>> | undefined> {
    this.loadCore();
    return this.coreConfig?.channels ?? {};
  }

  getResolvedEnvSnapshot(): Record<string, string> {
    return { ...this.getResolvedSkillsEnv() };
  }

  reset(): void {
    this.corePath = null;
    this.skillsPath = null;
    this.disabledState = null;
    this.coreConfig = null;
    this.skillsConfig = null;
    this.resolvedSkillsEnv = null;
  }

  /**
   * Set the API key for a provider and save to credentials.json5.
   * Creates the file if it doesn't exist.
   */
  setLlmProviderApiKey(provider: string, apiKey: string): void {
    const path = getCredentialsPath();

    // Load existing config or create new one
    let config: CredentialsConfig = { version: 1 };
    if (existsSync(path)) {
      try {
        const raw = readFileSync(path, "utf8");
        config = JSON5.parse(raw) as CredentialsConfig;
      } catch {
        // If parse fails, start fresh
        config = { version: 1 };
      }
    }

    // Ensure structure exists
    if (!config.llm) {
      config.llm = {};
    }
    if (!config.llm.providers) {
      config.llm.providers = {};
    }

    // Set or update the provider config
    const existing = config.llm.providers[provider] ?? {};
    config.llm.providers[provider] = {
      ...existing,
      apiKey,
    };

    // Write back to file
    mkdirSync(dirname(path), { recursive: true });
    const content = JSON.stringify(config, null, 2);
    writeFileSync(path, content, "utf8");

    // Reset cache so next read picks up the change
    this.reset();
  }

  /**
   * Set OAuth token for a provider and save to credentials.json5.
   * Used for OAuth providers like claude-code and openai-codex.
   */
  setLlmProviderOAuthToken(
    provider: string,
    token: string,
    refreshToken?: string,
    expiresAt?: number,
  ): void {
    const path = getCredentialsPath();

    // Load existing config or create new one
    let config: CredentialsConfig = { version: 1 };
    if (existsSync(path)) {
      try {
        const raw = readFileSync(path, "utf8");
        config = JSON5.parse(raw) as CredentialsConfig;
      } catch {
        config = { version: 1 };
      }
    }

    // Ensure structure exists
    if (!config.llm) {
      config.llm = {};
    }
    if (!config.llm.providers) {
      config.llm.providers = {};
    }

    // Set or update the provider config
    const existing = config.llm.providers[provider] ?? {};
    config.llm.providers[provider] = {
      ...existing,
      oauthToken: token,
      oauthRefreshToken: refreshToken,
      oauthExpiresAt: expiresAt,
    };

    // Write back to file
    mkdirSync(dirname(path), { recursive: true });
    const content = JSON.stringify(config, null, 2);
    writeFileSync(path, content, "utf8");

    // Reset cache
    this.reset();
  }

  /**
   * Set the default LLM provider and save to credentials.json5.
   */
  setDefaultLlmProvider(provider: string): void {
    const path = getCredentialsPath();

    // Load existing config or create new one
    let config: CredentialsConfig = { version: 1 };
    if (existsSync(path)) {
      try {
        const raw = readFileSync(path, "utf8");
        config = JSON5.parse(raw) as CredentialsConfig;
      } catch {
        config = { version: 1 };
      }
    }

    // Ensure structure exists
    if (!config.llm) {
      config.llm = {};
    }

    // Set default provider
    config.llm.provider = provider;

    // Write back to file
    mkdirSync(dirname(path), { recursive: true });
    const content = JSON.stringify(config, null, 2);
    writeFileSync(path, content, "utf8");

    // Reset cache
    this.reset();
  }
}

export const credentialManager = new CredentialManager();
