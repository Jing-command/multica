/**
 * Skills Module Types
 *
 * Type definitions for the skills system
 * Compatible with OpenClaw/AgentSkills specification
 */

// ============================================================================
// Install Specification Types
// ============================================================================

/**
 * Skill install specification
 * Defines how to install dependencies for a skill
 */
export interface SkillInstallSpec {
  /** Unique identifier for this install option */
  id?: string | undefined;
  /** Install method type */
  kind: "brew" | "node" | "go" | "uv" | "download";
  /** Human-readable label for UI */
  label?: string | undefined;
  /** Binaries that will be installed */
  bins?: string[] | undefined;
  /** Platforms this install option supports */
  os?: string[] | undefined;
  /** Homebrew formula name (for kind: "brew") */
  formula?: string | undefined;
  /** Package name (for kind: "node" or "uv") */
  package?: string | undefined;
  /** Go module path (for kind: "go") */
  module?: string | undefined;
  /** Download URL (for kind: "download") */
  url?: string | undefined;
  /** Archive type: "tar.gz" | "tar.bz2" | "zip" (for kind: "download") */
  archive?: string | undefined;
  /** Whether to extract the archive (for kind: "download") */
  extract?: boolean | undefined;
  /** Strip N leading path components when extracting (for kind: "download") */
  stripComponents?: number | undefined;
  /** Target directory for download (defaults to ~/.super-multica/tools/<skillKey>) */
  targetDir?: string | undefined;
}

/**
 * Skill requirements specification
 * Defines what must be present for a skill to be eligible
 */
export interface SkillRequirements {
  /** All listed binaries must exist in PATH */
  bins?: string[] | undefined;
  /** At least one of listed binaries must exist in PATH */
  anyBins?: string[] | undefined;
  /** All listed environment variables must be set (or provided via config) */
  env?: string[] | undefined;
  /** All listed config paths must be truthy */
  config?: string[] | undefined;
}

/**
 * Skill metadata for eligibility and display
 * Compatible with OpenClaw spec (metadata.openclaw or metadata.multica)
 */
export interface SkillMetadata {
  /** Always include this skill (skip eligibility checks except explicit disable) */
  always?: boolean | undefined;
  /** Custom key for config lookup (defaults to skill id) */
  skillKey?: string | undefined;
  /** Emoji for display (e.g., "📝") */
  emoji?: string | undefined;
  /** Homepage URL for documentation */
  homepage?: string | undefined;
  /** Supported platforms (darwin, linux, win32) */
  os?: string[] | undefined;
  /** Skill requirements */
  requires?: SkillRequirements | undefined;
  /** Install specifications */
  install?: SkillInstallSpec[] | undefined;
  /** Skill tags for categorization */
  tags?: string[] | undefined;

  // Legacy fields (for backward compatibility with existing skills)
  /** @deprecated Use requires.env instead */
  requiresEnv?: string[] | undefined;
  /** @deprecated Use requires.bins instead */
  requiresBinaries?: string[] | undefined;
  /** @deprecated Use os instead */
  platforms?: string[] | undefined;
}

/**
 * SKILL.md frontmatter schema
 */
export interface SkillFrontmatter {
  /** Skill name (required) */
  name: string;
  /** Human-readable description */
  description?: string | undefined;
  /** Skill version */
  version?: string | undefined;
  /** Author information */
  author?: string | undefined;
  /** Homepage/documentation URL */
  homepage?: string | undefined;
  /** Skill-specific metadata */
  metadata?: SkillMetadata | undefined;

  // Invocation control fields
  /** Whether users can invoke via /command (default: true) */
  userInvocable?: boolean | undefined;
  /** Whether to exclude from AI system prompt (default: false) */
  disableModelInvocation?: boolean | undefined;

  // Command dispatch fields
  /** Command dispatch mode (e.g., "tool") */
  commandDispatch?: string | undefined;
  /** Tool name for dispatch (when commandDispatch: "tool") */
  commandTool?: string | undefined;
  /** Argument mode for dispatch (default: "raw") */
  commandArgMode?: string | undefined;
}

/**
 * Skill source type with precedence (lower value = lower priority)
 */
export type SkillSource = "bundled" | "profile";

/**
 * Skill source precedence values
 */
export const SKILL_SOURCE_PRECEDENCE: Record<SkillSource, number> = {
  bundled: 0,
  profile: 1,
};

/**
 * Parsed skill entry
 */
export interface Skill {
  /** Unique skill identifier (directory name) */
  id: string;
  /** Parsed frontmatter */
  frontmatter: SkillFrontmatter;
  /** Skill instructions (markdown body after frontmatter) */
  instructions: string;
  /** Source type */
  source: SkillSource;
  /** Full path to SKILL.md */
  filePath: string;
  /** Environment variables loaded from the skill's .env file */
  env: Record<string, string>;
}

// ============================================================================
// Configuration Types
// ============================================================================

/**
 * Per-skill configuration
 * Applied via skills.entries.<skillKey>
 */
export interface SkillConfig {
  /** Explicitly enable/disable this skill */
  enabled?: boolean | undefined;
  /** Custom per-skill configuration */
  config?: Record<string, unknown> | undefined;
}

/**
 * Skills loading configuration
 */
export interface SkillsLoadConfig {
  /** Enable file watching for hot reload (default: true) */
  watch?: boolean | undefined;
  /** Watch debounce delay in ms (default: 250) */
  watchDebounceMs?: number | undefined;
}

/**
 * Skills install preferences
 */
export interface SkillsInstallConfig {
  /** Prefer brew over other installers when available */
  preferBrew?: boolean | undefined;
  /** Node package manager to use */
  nodeManager?: "npm" | "pnpm" | "yarn" | "bun" | undefined;
}

/**
 * Complete skills configuration
 */
export interface SkillsConfig {
  /** Allowlist for bundled skills (if set, only these bundled skills are eligible) */
  allowBundled?: string[] | undefined;
  /** Loading configuration */
  load?: SkillsLoadConfig | undefined;
  /** Install preferences */
  install?: SkillsInstallConfig | undefined;
  /** Per-skill configuration entries */
  entries?: Record<string, SkillConfig> | undefined;
}

// ============================================================================
// Manager Types
// ============================================================================

/**
 * Skill Manager options
 */
export interface SkillManagerOptions {
  /** Agent profile ID (for profile-specific skills) */
  profileId?: string | undefined;
  /** Profile base directory, defaults to ~/.super-multica/agent-profiles */
  profileBaseDir?: string | undefined;
  /** Platform override (for testing) */
  platform?: NodeJS.Platform | undefined;
  /** Skills configuration */
  config?: SkillsConfig | undefined;
}

/**
 * Skill eligibility check result
 */
export interface EligibilityResult {
  /** Whether the skill is eligible */
  eligible: boolean;
  /** Reasons for ineligibility */
  reasons?: string[] | undefined;
}

// ============================================================================
// Invocation Types
// ============================================================================

/**
 * Skill invocation policy
 * Controls how a skill can be invoked
 */
export interface SkillInvocationPolicy {
  /** Whether users can invoke this skill via /command (default: true) */
  userInvocable: boolean;
  /** Whether to exclude from AI's system prompt (default: false) */
  disableModelInvocation: boolean;
}

/**
 * Command dispatch specification
 * For skills that dispatch directly to a tool
 */
export interface SkillCommandDispatch {
  /** Dispatch type */
  kind: "tool";
  /** Tool name to invoke */
  toolName: string;
  /** How to pass arguments (default: "raw") */
  argMode?: "raw" | undefined;
}

/**
 * Skill command specification
 * Represents a user-invocable skill command
 */
export interface SkillCommandSpec {
  /** Normalized command name (e.g., "pdf" for /pdf) */
  name: string;
  /** Original skill name/ID */
  skillId: string;
  /** Command description */
  description: string;
  /** Optional dispatch behavior */
  dispatch?: SkillCommandDispatch | undefined;
}

/**
 * Skill invocation result
 */
export interface SkillInvocationResult {
  /** The matched command */
  command: SkillCommandSpec;
  /** Arguments passed to the command */
  args?: string | undefined;
  /** The skill instructions to inject */
  instructions: string;
}

/**
 * Filename constant for skill definition file
 */
export const SKILL_FILE = "SKILL.md";

// ============================================================================
// Utility Functions
// ============================================================================

/**
 * Get the config key for a skill
 * Uses metadata.skillKey if defined, otherwise falls back to skill id
 */
export function getSkillKey(skill: Skill): string {
  return skill.frontmatter.metadata?.skillKey ?? skill.id;
}

/**
 * Get the skill config for a specific skill
 */
export function getSkillConfig(
  skill: Skill,
  config?: SkillsConfig,
): SkillConfig | undefined {
  if (!config?.entries) return undefined;
  const key = getSkillKey(skill);
  return config.entries[key];
}

/**
 * Normalize requirements from both new and legacy metadata formats
 */
export function normalizeRequirements(metadata?: SkillMetadata): SkillRequirements {
  if (!metadata) return {};

  return {
    bins: metadata.requires?.bins ?? metadata.requiresBinaries ?? [],
    anyBins: metadata.requires?.anyBins ?? [],
    env: metadata.requires?.env ?? metadata.requiresEnv ?? [],
    config: metadata.requires?.config ?? [],
  };
}

/**
 * Normalize platforms from both new and legacy metadata formats
 */
export function normalizePlatforms(metadata?: SkillMetadata): string[] {
  if (!metadata) return [];
  return metadata.os ?? metadata.platforms ?? [];
}
