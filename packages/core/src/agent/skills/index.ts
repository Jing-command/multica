/**
 * Skills Module
 *
 * Manages skill loading, eligibility filtering, and system prompt generation
 * Compatible with OpenClaw/AgentSkills specification
 */

import type { Skill, SkillManagerOptions, SkillsConfig, SkillCommandSpec, SkillInvocationResult } from "./types.js";
import { loadAllSkills, getProfileSkillsDir, initializeManagedSkills, getManagedSkillsDir } from "./loader.js";
import {
  filterEligibleSkills,
  checkEligibility,
  type EligibilityContext,
} from "./eligibility.js";
import {
  startSkillsWatcher,
  stopSkillsWatcher,
  getSkillsVersion,
  bumpSkillsVersion,
  onSkillsChange,
  isWatcherActive,
  type SkillsChangeEvent,
  type SkillsChangeListener,
} from "./watcher.js";
import {
  buildSkillCommands,
  resolveSkillInvocation,
  getCommandCompletions,
  isModelInvocable,
} from "./invoke.js";

// Re-export types and utilities
export type {
  Skill,
  SkillFrontmatter,
  SkillMetadata,
  SkillSource,
  SkillManagerOptions,
  SkillsConfig,
  SkillConfig,
  SkillsLoadConfig,
  SkillsInstallConfig,
  SkillInstallSpec,
  SkillRequirements,
  EligibilityResult,
  SkillInvocationPolicy,
  SkillCommandSpec,
  SkillCommandDispatch,
  SkillInvocationResult,
} from "./types.js";

export {
  SKILL_FILE,
  SKILL_SOURCE_PRECEDENCE,
  getSkillKey,
  getSkillConfig,
  normalizeRequirements,
  normalizePlatforms,
} from "./types.js";

export {
  checkEligibility,
  checkEligibilityDetailed,
  filterEligibleSkills,
  binaryExists,
  resolveConfigPath,
  isConfigPathTruthy,
  type EligibilityContext,
  type DiagnosticType,
  type DiagnosticItem,
  type DetailedEligibilityResult,
} from "./eligibility.js";

export { parseFrontmatter, parseSkillFile } from "./parser.js";
export { parseDotEnv } from "./dotenv.js";
export { loadAllSkills, getProfileSkillsDir, initializeManagedSkills, getManagedSkillsDir } from "./loader.js";

// Export install module
export {
  installSkill,
  selectPreferredInstallSpec,
  getInstallOptions,
  type SkillInstallRequest,
  type SkillInstallResult,
} from "./install.js";

// Export watcher module
export {
  startSkillsWatcher,
  stopSkillsWatcher,
  getSkillsVersion,
  bumpSkillsVersion,
  onSkillsChange,
  isWatcherActive,
  type SkillsChangeEvent,
  type SkillsChangeListener,
} from "./watcher.js";

// Export add module
export {
  addSkill,
  removeSkill,
  listInstalledSkills,
  parseSource,
  type SkillAddRequest,
  type SkillAddResult,
} from "./add.js";

// Export invoke module
export {
  resolveInvocationPolicy,
  isUserInvocable,
  isModelInvocable,
  sanitizeCommandName,
  buildSkillCommands,
  findSkillCommand,
  resolveSkillInvocation,
  getCommandCompletions,
} from "./invoke.js";

// Export serialize module
export {
  serialize,
  createSerialized,
  isProcessing,
  getQueueLength,
  getActiveKeys,
  waitForKey,
  waitForAll,
  SerializeKeys,
} from "./serialize.js";

/**
 * SkillManager - Loads and manages skills
 *
 * Provides access to skills from multiple sources with precedence handling
 * and eligibility filtering based on configuration.
 *
 * Supports hot-reload via file watching when enabled.
 */
export class SkillManager {
  private readonly options: SkillManagerOptions;
  private skills: Map<string, Skill> | undefined;
  private eligibleSkills: Map<string, Skill> | undefined;
  private loadedVersion: number = 0;
  private unsubscribeWatcher: (() => void) | undefined;

  constructor(options: SkillManagerOptions = {}) {
    this.options = options;
  }

  /**
   * Get the eligibility context for filtering
   */
  private getEligibilityContext(): EligibilityContext {
    return {
      config: this.options.config,
      platform: this.options.platform,
    };
  }

  /**
   * Ensure skills are loaded (lazy loading)
   * Also checks if reload is needed due to file changes
   */
  private ensureLoaded(): void {
    const currentVersion = getSkillsVersion();

    // Reload if version changed (file watcher triggered)
    if (this.skills && this.loadedVersion !== currentVersion) {
      this.skills = undefined;
      this.eligibleSkills = undefined;
    }

    if (this.skills) return;

    this.skills = loadAllSkills(this.options);
    this.eligibleSkills = filterEligibleSkills(
      this.skills,
      this.getEligibilityContext(),
    );
    this.loadedVersion = currentVersion;
  }

  /**
   * Start file watching for hot reload
   *
   * @returns Promise that resolves when watcher is started
   */
  async startWatching(): Promise<void> {
    // Don't start if watching is disabled in config
    if (this.options.config?.load?.watch === false) {
      return;
    }

    // Subscribe to changes for automatic reload
    this.unsubscribeWatcher = onSkillsChange(() => {
      // Just invalidate cache, reload happens on next access
      this.skills = undefined;
      this.eligibleSkills = undefined;
    });

    // Start the watcher (enabled by default unless explicitly set to false)
    const watchEnabled = this.options.config?.load?.watch ?? true;
    await startSkillsWatcher({
      profileId: this.options.profileId,
      profileBaseDir: this.options.profileBaseDir,
      debounceMs: this.options.config?.load?.watchDebounceMs,
      enabled: watchEnabled,
    });
  }

  /**
   * Stop file watching
   */
  async stopWatching(): Promise<void> {
    if (this.unsubscribeWatcher) {
      this.unsubscribeWatcher();
      this.unsubscribeWatcher = undefined;
    }
    await stopSkillsWatcher();
  }

  /**
   * Check if file watching is active
   */
  isWatching(): boolean {
    return isWatcherActive();
  }

  /**
   * Get all loaded skills (including ineligible)
   */
  getAllSkills(): Map<string, Skill> {
    this.ensureLoaded();
    return this.skills!;
  }

  /**
   * Get only eligible skills
   */
  getEligibleSkills(): Map<string, Skill> {
    this.ensureLoaded();
    return this.eligibleSkills!;
  }

  /**
   * Get a specific skill by ID (only from eligible skills)
   *
   * @param skillId - Skill identifier
   * @returns Skill or undefined if not found or ineligible
   */
  getSkill(skillId: string): Skill | undefined {
    this.ensureLoaded();
    return this.eligibleSkills!.get(skillId);
  }

  /**
   * Get skill by ID from all skills (including ineligible)
   *
   * @param skillId - Skill identifier
   * @returns Skill or undefined if not found
   */
  getSkillFromAll(skillId: string): Skill | undefined {
    this.ensureLoaded();
    return this.skills!.get(skillId);
  }

  /**
   * Check eligibility for a specific skill
   *
   * @param skillId - Skill identifier
   * @returns Eligibility result or undefined if skill not found
   */
  checkSkillEligibility(skillId: string): { eligible: boolean; reasons?: string[] | undefined } | undefined {
    const skill = this.getSkillFromAll(skillId);
    if (!skill) return undefined;
    return checkEligibility(skill, this.getEligibilityContext());
  }

  /**
   * Reload skills from disk
   * Clears cache and reloads on next access
   */
  reload(): void {
    this.skills = undefined;
    this.eligibleSkills = undefined;
    bumpSkillsVersion("manual");
  }

  /**
   * Update configuration and reload
   *
   * @param config - New skills configuration
   */
  updateConfig(config: SkillsConfig): void {
    (this.options as { config?: SkillsConfig }).config = config;
    this.reload();
  }

  /**
   * Get the current configuration
   */
  getConfig(): SkillsConfig | undefined {
    return this.options.config;
  }

  /**
   * Build skills section for system prompt
   *
   * Generates formatted documentation of all eligible skills
   * for inclusion in the agent's system prompt.
   *
   * @returns Formatted skill documentation or empty string if no skills
   */
  buildSkillsPrompt(): string {
    this.ensureLoaded();

    if (this.eligibleSkills!.size === 0) {
      return "";
    }

    const parts: string[] = [];
    parts.push("# Available Skills\n");
    parts.push("You have access to the following skills:\n");

    for (const [id, skill] of this.eligibleSkills!) {
      const emoji = skill.frontmatter.metadata?.emoji ?? "🔧";
      const name = skill.frontmatter.name;
      const desc = skill.frontmatter.description ?? "No description provided";

      parts.push(`## ${emoji} ${name} (${id})`);
      parts.push(`${desc}\n`);

      // Include full instructions
      if (skill.instructions) {
        parts.push(skill.instructions);
        parts.push("");
      }
    }

    return parts.join("\n");
  }

  /**
   * Get skill instructions for a specific skill
   *
   * @param skillId - Skill identifier
   * @returns Instructions markdown or undefined if not found
   */
  getSkillInstructions(skillId: string): string | undefined {
    const skill = this.getSkill(skillId);
    return skill?.instructions;
  }

  /**
   * List skill IDs with their display info
   *
   * @returns Array of skill info for display
   */
  listSkills(): Array<{
    id: string;
    name: string;
    emoji: string;
    description: string;
    source: string;
  }> {
    this.ensureLoaded();

    const result: Array<{
      id: string;
      name: string;
      emoji: string;
      description: string;
      source: string;
    }> = [];

    for (const [id, skill] of this.eligibleSkills!) {
      result.push({
        id,
        name: skill.frontmatter.name,
        emoji: skill.frontmatter.metadata?.emoji ?? "🔧",
        description: skill.frontmatter.description ?? "No description",
        source: skill.source,
      });
    }

    return result;
  }

  /**
   * List all skills with eligibility status
   *
   * @returns Array of skill info with eligibility status
   */
  listAllSkillsWithStatus(): Array<{
    id: string;
    name: string;
    emoji: string;
    description: string;
    source: string;
    eligible: boolean;
    reasons?: string[] | undefined;
  }> {
    this.ensureLoaded();

    const result: Array<{
      id: string;
      name: string;
      emoji: string;
      description: string;
      source: string;
      eligible: boolean;
      reasons?: string[] | undefined;
    }> = [];

    for (const [id, skill] of this.skills!) {
      const eligibility = checkEligibility(skill, this.getEligibilityContext());
      result.push({
        id,
        name: skill.frontmatter.name,
        emoji: skill.frontmatter.metadata?.emoji ?? "🔧",
        description: skill.frontmatter.description ?? "No description",
        source: skill.source,
        eligible: eligibility.eligible,
        reasons: eligibility.reasons,
      });
    }

    return result;
  }

  // ============================================================================
  // Invocation Methods
  // ============================================================================

  private cachedCommands: SkillCommandSpec[] | undefined;
  private cachedCommandsVersion: number = 0;

  /**
   * Get user-invocable skill commands
   *
   * @param options - Optional reserved names to avoid
   * @returns Array of command specifications
   */
  getSkillCommands(options?: { reservedNames?: Set<string> }): SkillCommandSpec[] {
    this.ensureLoaded();

    const currentVersion = getSkillsVersion();
    if (this.cachedCommands && this.cachedCommandsVersion === currentVersion) {
      return this.cachedCommands;
    }

    this.cachedCommands = buildSkillCommands(this.eligibleSkills!, options);
    this.cachedCommandsVersion = currentVersion;
    return this.cachedCommands;
  }

  /**
   * Resolve a user command to a skill invocation
   *
   * @param input - User input (e.g., "/pdf edit file.pdf")
   * @returns Invocation result or null if not a skill command
   */
  resolveCommand(input: string): SkillInvocationResult | null {
    this.ensureLoaded();
    const commands = this.getSkillCommands();
    return resolveSkillInvocation(input, commands, this.eligibleSkills!);
  }

  /**
   * Get command completions for a prefix
   *
   * @param prefix - Input prefix (e.g., "/p" or "p")
   * @returns Matching command names with leading /
   */
  getCompletions(prefix: string): string[] {
    const commands = this.getSkillCommands();
    return getCommandCompletions(prefix, commands);
  }

  /**
   * Build skills prompt excluding user-only skills (metadata only)
   *
   * Only includes skills that are model-invocable (disableModelInvocation !== true)
   * Uses progressive loading: only outputs name + description, not full instructions
   * Full instructions are loaded on-demand via getSkillInstructions() when a skill is triggered
   *
   * @returns Formatted skill metadata for AI system prompt
   */
  buildModelSkillsPrompt(): string {
    this.ensureLoaded();

    const modelSkills = new Map<string, Skill>();
    for (const [id, skill] of this.eligibleSkills!) {
      if (isModelInvocable(skill)) {
        modelSkills.set(id, skill);
      }
    }

    if (modelSkills.size === 0) {
      return "";
    }

    const parts: string[] = [];
    parts.push("# Available Skills\n");
    parts.push("You have access to the following skills. When you need to use a skill, the full instructions will be provided.\n");

    for (const [id, skill] of modelSkills) {
      const emoji = skill.frontmatter.metadata?.emoji ?? "🔧";
      const name = skill.frontmatter.name;
      const desc = skill.frontmatter.description ?? "No description provided";

      // Progressive loading: only output metadata, not full instructions
      parts.push(`- ${emoji} **${name}** (\`${id}\`): ${desc}`);
    }

    parts.push("");
    return parts.join("\n");
  }

  /**
   * Build full skill prompt for injection when a skill is triggered
   *
   * This is used for on-demand loading - when a skill is invoked,
   * this method provides the complete instructions to inject into context
   *
   * @param skillId - Skill identifier
   * @returns Formatted skill prompt with full instructions, or undefined if not found
   */
  buildSkillPromptForInjection(skillId: string): string | undefined {
    const skill = this.getSkill(skillId);
    if (!skill) return undefined;

    const emoji = skill.frontmatter.metadata?.emoji ?? "🔧";
    const name = skill.frontmatter.name;
    const desc = skill.frontmatter.description ?? "No description provided";

    const parts: string[] = [];
    parts.push(`# Skill: ${emoji} ${name}\n`);
    parts.push(`${desc}\n`);

    if (skill.instructions) {
      parts.push("## Instructions\n");
      parts.push(skill.instructions);
    }

    return parts.join("\n");
  }
}
