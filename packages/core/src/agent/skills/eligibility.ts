/**
 * Skill Eligibility Checker
 *
 * Filter skills based on platform, binaries, environment, and configuration
 * Compatible with OpenClaw eligibility rules
 *
 * Enhanced with detailed diagnostics and actionable hints
 */

import { execSync } from "node:child_process";
import type {
  Skill,
  SkillsConfig,
  EligibilityResult,
} from "./types.js";
import {
  getSkillKey,
  getSkillConfig,
  normalizeRequirements,
  normalizePlatforms,
} from "./types.js";
import { dirname, join } from "node:path";

// ============================================================================
// Diagnostic Types
// ============================================================================

export type DiagnosticType =
  | "disabled"
  | "not_in_allowlist"
  | "platform"
  | "binary"
  | "any_binary"
  | "env"
  | "config";

export interface DiagnosticItem {
  /** Type of diagnostic issue */
  type: DiagnosticType;
  /** Human-readable message */
  message: string;
  /** Actionable hint to resolve the issue */
  hint?: string | undefined;
  /** Related values (e.g., missing binary names) */
  values?: string[] | undefined;
}

export interface DetailedEligibilityResult extends EligibilityResult {
  /** Detailed diagnostics for each issue */
  diagnostics?: DiagnosticItem[] | undefined;
}

// ============================================================================
// Binary and Environment Checks
// ============================================================================

/**
 * Check if a binary exists in PATH
 *
 * @param binary - Binary name to check
 * @returns True if binary exists
 */
export function binaryExists(binary: string): boolean {
  try {
    // Use 'which' on Unix, 'where' on Windows
    const cmd = process.platform === "win32" ? `where ${binary}` : `which ${binary}`;
    execSync(cmd, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

/**
 * Check if an environment variable is set
 *
 * Checks the skill's own .env first, then falls back to process.env.
 *
 * @param envVar - Environment variable name
 * @param skill - Skill to check against
 * @returns True if set (even if empty string)
 */
function envExists(envVar: string, skill: Skill): boolean {
  if (Object.prototype.hasOwnProperty.call(skill.env, envVar)) {
    return true;
  }
  return envVar in process.env;
}

// ============================================================================
// Config Path Resolution
// ============================================================================

/**
 * Resolve a dot-separated config path
 *
 * @param config - Config object
 * @param pathStr - Dot-separated path (e.g., "browser.enabled")
 * @returns The value at the path, or undefined
 */
export function resolveConfigPath(
  config: Record<string, unknown> | undefined,
  pathStr: string,
): unknown {
  if (!config) return undefined;

  const parts = pathStr.split(".").filter(Boolean);
  let current: unknown = config;

  for (const part of parts) {
    if (typeof current !== "object" || current === null) {
      return undefined;
    }
    current = (current as Record<string, unknown>)[part];
  }

  return current;
}

/**
 * Check if a config path is truthy
 *
 * @param config - Config object
 * @param pathStr - Dot-separated path
 * @returns True if the value at path is truthy
 */
export function isConfigPathTruthy(
  config: Record<string, unknown> | undefined,
  pathStr: string,
): boolean {
  const value = resolveConfigPath(config, pathStr);
  if (value === undefined || value === null) return false;
  if (typeof value === "boolean") return value;
  if (typeof value === "number") return value !== 0;
  if (typeof value === "string") return value.trim().length > 0;
  return true;
}

// ============================================================================
// Bundled Skills Allowlist
// ============================================================================

const BUNDLED_SOURCES = new Set(["bundled"]);

/**
 * Check if a skill is from bundled source
 */
function isBundledSkill(skill: Skill): boolean {
  return BUNDLED_SOURCES.has(skill.source);
}

/**
 * Check if a bundled skill is allowed by the allowlist
 *
 * @param skill - Skill to check
 * @param allowlist - List of allowed skill keys (undefined = allow all)
 * @returns True if allowed
 */
function isBundledSkillAllowed(skill: Skill, allowlist?: string[]): boolean {
  // No allowlist = allow all
  if (!allowlist || allowlist.length === 0) return true;
  // Non-bundled skills are always allowed
  if (!isBundledSkill(skill)) return true;
  // Check if skill key or id is in allowlist
  const key = getSkillKey(skill);
  return allowlist.includes(key) || allowlist.includes(skill.id);
}

// ============================================================================
// Main Eligibility Check
// ============================================================================

export interface EligibilityContext {
  /** Skills configuration */
  config?: SkillsConfig | undefined;
  /** Platform to check against (defaults to current) */
  platform?: NodeJS.Platform | undefined;
  /** Custom config object for config path checks */
  customConfig?: Record<string, unknown> | undefined;
}

/**
 * Check if a skill is eligible based on its requirements and configuration
 *
 * Eligibility rules (in order):
 * 1. If explicitly disabled in config → not eligible
 * 2. If bundled and not in allowlist → not eligible
 * 3. If platform not supported → not eligible
 * 4. If metadata.always is true → eligible (skip remaining checks)
 * 5. All required binaries must exist
 * 6. At least one of anyBins must exist (if specified)
 * 7. All required env vars must be set (or provided via config)
 * 8. All required config paths must be truthy
 *
 * @param skill - Skill to check
 * @param context - Eligibility context
 * @returns Eligibility result with reasons if ineligible
 */
export function checkEligibility(
  skill: Skill,
  context: EligibilityContext = {},
): EligibilityResult {
  const result = checkEligibilityDetailed(skill, context);
  // Return simple result for backward compatibility
  return {
    eligible: result.eligible,
    reasons: result.reasons,
  };
}

/**
 * Check eligibility with detailed diagnostics
 *
 * Same as checkEligibility but returns detailed diagnostics with hints
 *
 * @param skill - Skill to check
 * @param context - Eligibility context
 * @returns Detailed eligibility result with diagnostics
 */
export function checkEligibilityDetailed(
  skill: Skill,
  context: EligibilityContext = {},
): DetailedEligibilityResult {
  const { config, platform = process.platform, customConfig } = context;
  const reasons: string[] = [];
  const diagnostics: DiagnosticItem[] = [];
  const metadata = skill.frontmatter.metadata;
  const skillConfig = getSkillConfig(skill, config);

  // 1. Check if explicitly disabled in config
  if (skillConfig?.enabled === false) {
    const msg = "Skill disabled in configuration";
    reasons.push(msg);
    diagnostics.push({
      type: "disabled",
      message: msg,
      hint: `Enable by setting skills.${getSkillKey(skill)}.enabled: true in config`,
    });
    return { eligible: false, reasons, diagnostics };
  }

  // 2. Check bundled allowlist
  if (!isBundledSkillAllowed(skill, config?.allowBundled)) {
    const msg = "Bundled skill not in allowlist";
    reasons.push(msg);
    diagnostics.push({
      type: "not_in_allowlist",
      message: msg,
      hint: `Add '${getSkillKey(skill)}' to config.allowBundled array`,
    });
    return { eligible: false, reasons, diagnostics };
  }

  // 3. Platform check
  const platforms = normalizePlatforms(metadata);
  if (platforms.length > 0 && !platforms.includes(platform)) {
    const msg = `Platform '${platform}' not supported (requires: ${platforms.join(", ")})`;
    reasons.push(msg);
    diagnostics.push({
      type: "platform",
      message: msg,
      hint: `This skill only works on: ${platforms.join(", ")}`,
      values: platforms,
    });
    return { eligible: false, reasons, diagnostics };
  }

  // 4. Always flag - skip remaining checks
  if (metadata?.always === true) {
    return { eligible: true };
  }

  // Get normalized requirements
  const requirements = normalizeRequirements(metadata);

  // 5. Required binaries check (all must exist)
  if (requirements.bins && requirements.bins.length > 0) {
    const missingBins: string[] = [];
    for (const bin of requirements.bins) {
      if (!binaryExists(bin)) {
        missingBins.push(bin);
        reasons.push(`Required binary not found: ${bin}`);
      }
    }
    if (missingBins.length > 0) {
      diagnostics.push({
        type: "binary",
        message: `Missing required binaries: ${missingBins.join(", ")}`,
        hint: generateBinaryInstallHint(missingBins, skill),
        values: missingBins,
      });
    }
  }

  // 6. Any binaries check (at least one must exist)
  if (requirements.anyBins && requirements.anyBins.length > 0) {
    const anyFound = requirements.anyBins.some((bin) => binaryExists(bin));
    if (!anyFound) {
      const msg = `None of required binaries found: ${requirements.anyBins.join(", ")}`;
      reasons.push(msg);
      diagnostics.push({
        type: "any_binary",
        message: msg,
        hint: `Install any one of: ${requirements.anyBins.join(", ")}`,
        values: requirements.anyBins,
      });
    }
  }

  // 7. Environment variable check
  const missingEnvVars: string[] = [];
  if (requirements.env && requirements.env.length > 0) {
    for (const envVar of requirements.env) {
      // Check if env var exists
      if (envExists(envVar, skill)) continue;

      missingEnvVars.push(envVar);
      reasons.push(`Required environment variable not set: ${envVar}`);
    }
  }
  if (missingEnvVars.length > 0) {
    diagnostics.push({
      type: "env",
      message: `Missing environment variables: ${missingEnvVars.join(", ")}`,
      hint: generateEnvHint(missingEnvVars, skill),
      values: missingEnvVars,
    });
  }

  // 8. Config path check
  const missingConfigs: string[] = [];
  if (requirements.config && requirements.config.length > 0) {
    for (const configPath of requirements.config) {
      if (!isConfigPathTruthy(customConfig, configPath)) {
        missingConfigs.push(configPath);
        reasons.push(`Required config path not truthy: ${configPath}`);
      }
    }
  }
  if (missingConfigs.length > 0) {
    diagnostics.push({
      type: "config",
      message: `Missing config values: ${missingConfigs.join(", ")}`,
      hint: `Set the following config paths: ${missingConfigs.join(", ")}`,
      values: missingConfigs,
    });
  }

  return {
    eligible: reasons.length === 0,
    reasons: reasons.length > 0 ? reasons : undefined,
    diagnostics: diagnostics.length > 0 ? diagnostics : undefined,
  };
}

// ============================================================================
// Hint Generation
// ============================================================================

/**
 * Generate installation hints for missing binaries
 */
function generateBinaryInstallHint(binaries: string[], skill: Skill): string {
  const hints: string[] = [];

  // Check if skill has install specs for these binaries
  const installSpecs = skill.frontmatter.metadata?.install;
  if (installSpecs && installSpecs.length > 0) {
    hints.push(`Run: pnpm skills:cli install ${skill.id}`);
  }

  // Generate platform-specific hints
  const platform = process.platform;

  for (const bin of binaries) {
    const installHint = getBinaryInstallHint(bin, platform);
    if (installHint && !hints.includes(installHint)) {
      hints.push(installHint);
    }
  }

  if (hints.length === 0) {
    hints.push(`Install: ${binaries.join(", ")}`);
  }

  return hints.join(" OR ");
}

/**
 * Get platform-specific install hint for a binary
 */
function getBinaryInstallHint(binary: string, platform: NodeJS.Platform): string | null {
  const commonBinaries: Record<string, Record<string, string>> = {
    // Package managers
    brew: { darwin: "Install Homebrew: /bin/bash -c \"$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\"" },
    npm: { darwin: "brew install node", linux: "apt install nodejs", win32: "Download from nodejs.org" },
    pnpm: { "*": "npm install -g pnpm" },
    yarn: { "*": "npm install -g yarn" },
    bun: { darwin: "brew install bun", linux: "curl -fsSL https://bun.sh/install | bash" },

    // Common tools
    git: { darwin: "brew install git", linux: "apt install git", win32: "Download from git-scm.com" },
    python: { darwin: "brew install python", linux: "apt install python3", win32: "Download from python.org" },
    python3: { darwin: "brew install python", linux: "apt install python3" },
    pip: { "*": "python -m ensurepip" },
    uv: { darwin: "brew install uv", linux: "curl -LsSf https://astral.sh/uv/install.sh | sh" },

    // Development tools
    go: { darwin: "brew install go", linux: "apt install golang-go" },
    rustc: { "*": "curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh" },
    cargo: { "*": "curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh" },
    java: { darwin: "brew install openjdk", linux: "apt install default-jdk" },

    // PDF tools
    pdftk: { darwin: "brew install pdftk-java", linux: "apt install pdftk" },
    qpdf: { darwin: "brew install qpdf", linux: "apt install qpdf" },
    gs: { darwin: "brew install ghostscript", linux: "apt install ghostscript" },
    magick: { darwin: "brew install imagemagick", linux: "apt install imagemagick" },

    // Other common
    ffmpeg: { darwin: "brew install ffmpeg", linux: "apt install ffmpeg" },
    jq: { darwin: "brew install jq", linux: "apt install jq" },
    curl: { darwin: "brew install curl", linux: "apt install curl" },
    wget: { darwin: "brew install wget", linux: "apt install wget" },
  };

  const hints = commonBinaries[binary];
  if (!hints) return null;

  // Check for platform-specific hint
  if (hints[platform]) {
    return hints[platform]!;
  }

  // Check for wildcard hint
  if (hints["*"]) {
    return hints["*"];
  }

  return null;
}

/**
 * Generate hints for missing environment variables
 */
function generateEnvHint(envVars: string[], skill: Skill): string {
  const hints: string[] = [];
  const envPath = join(dirname(skill.filePath), ".env");

  for (const envVar of envVars) {
    // Check for well-known API key patterns
    if (envVar.endsWith("_API_KEY") || envVar.endsWith("_KEY")) {
      hints.push(`Set ${envVar} in ${envPath} or your environment`);

      // Add provider-specific hints
      const providerHint = getApiKeyHint(envVar);
      if (providerHint) {
        hints.push(providerHint);
      }
    } else {
      hints.push(`Set ${envVar} in ${envPath} or export ${envVar}=<value>`);
    }
  }

  return hints.slice(0, 3).join(" OR ");
}

/**
 * Get hint for obtaining API keys
 */
function getApiKeyHint(envVar: string): string | null {
  const keyHints: Record<string, string> = {
    OPENAI_API_KEY: "Get from: platform.openai.com/api-keys",
    ANTHROPIC_API_KEY: "Get from: console.anthropic.com",
    GOOGLE_API_KEY: "Get from: console.cloud.google.com",
    PERPLEXITY_API_KEY: "Get from: perplexity.ai/settings/api",
    DEEPSEEK_API_KEY: "Get from: platform.deepseek.com",
    GROQ_API_KEY: "Get from: console.groq.com",
    MISTRAL_API_KEY: "Get from: console.mistral.ai",
    TOGETHER_API_KEY: "Get from: api.together.xyz",
  };

  return keyHints[envVar] ?? null;
}

/**
 * Filter skills by eligibility
 *
 * @param skills - Map of skills to filter
 * @param context - Eligibility context
 * @returns Map containing only eligible skills
 */
export function filterEligibleSkills(
  skills: Map<string, Skill>,
  context: EligibilityContext = {},
): Map<string, Skill> {
  const eligible = new Map<string, Skill>();

  for (const [id, skill] of skills) {
    const result = checkEligibility(skill, context);
    if (result.eligible) {
      eligible.set(id, skill);
    }
  }

  return eligible;
}

// ============================================================================
// Legacy Compatibility
// ============================================================================

/**
 * @deprecated Use checkEligibility with context instead
 */
export function checkEligibilityLegacy(
  skill: Skill,
  platform: NodeJS.Platform = process.platform,
): EligibilityResult {
  return checkEligibility(skill, { platform });
}
