/**
 * SKILL.md Parser
 *
 * Parse skill files with YAML frontmatter and markdown body
 */

import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { parse as parseYaml } from "yaml";
import type { Skill, SkillFrontmatter, SkillSource, SkillInstallSpec } from "./types.js";
import { parseDotEnv } from "./dotenv.js";

/**
 * Parse YAML frontmatter from markdown content
 *
 * @param content - Raw markdown content
 * @returns Tuple of [frontmatter object or null, body content]
 */
export function parseFrontmatter(content: string): [Record<string, unknown> | null, string] {
  // Match frontmatter between --- delimiters at the start
  const match = content.match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n([\s\S]*)$/);
  if (!match) {
    return [null, content.trim()];
  }

  const frontmatterRaw = match[1] ?? "";
  const body = match[2] ?? "";

  if (!frontmatterRaw) {
    return [null, content.trim()];
  }

  try {
    const frontmatter = parseYaml(frontmatterRaw) as Record<string, unknown>;
    return [frontmatter, body.trim()];
  } catch {
    // Invalid YAML, return null frontmatter
    return [null, content.trim()];
  }
}

/**
 * Validate and coerce frontmatter to SkillFrontmatter type
 *
 * @param raw - Raw parsed frontmatter
 * @returns Validated SkillFrontmatter or null if invalid
 */
function validateFrontmatter(raw: Record<string, unknown>): SkillFrontmatter | null {
  // Name is required
  if (typeof raw.name !== "string" || raw.name.trim() === "") {
    return null;
  }

  const frontmatter: SkillFrontmatter = {
    name: raw.name.trim(),
  };

  if (typeof raw.description === "string") {
    frontmatter.description = raw.description;
  }

  if (typeof raw.version === "string") {
    frontmatter.version = raw.version;
  }

  if (typeof raw.author === "string") {
    frontmatter.author = raw.author;
  }

  if (typeof raw.homepage === "string") {
    frontmatter.homepage = raw.homepage;
  }

  // Parse metadata if present
  if (typeof raw.metadata === "object" && raw.metadata !== null) {
    const meta = raw.metadata as Record<string, unknown>;
    const filterStrings = (arr: unknown): string[] | undefined =>
      Array.isArray(arr) ? arr.filter((v): v is string => typeof v === "string") : undefined;

    frontmatter.metadata = {
      emoji: typeof meta.emoji === "string" ? meta.emoji : undefined,
      tags: filterStrings(meta.tags),
      // Legacy fields
      requiresEnv: filterStrings(meta.requiresEnv),
      requiresBinaries: filterStrings(meta.requiresBinaries),
      platforms: filterStrings(meta.platforms),
      // New fields
      always: typeof meta.always === "boolean" ? meta.always : undefined,
      skillKey: typeof meta.skillKey === "string" ? meta.skillKey : undefined,
      os: filterStrings(meta.os),
    };

    // Parse requires nested object
    if (typeof meta.requires === "object" && meta.requires !== null) {
      const req = meta.requires as Record<string, unknown>;
      frontmatter.metadata.requires = {
        bins: filterStrings(req.bins),
        anyBins: filterStrings(req.anyBins),
        env: filterStrings(req.env),
        config: filterStrings(req.config),
      };
    }

    // Parse install array
    if (Array.isArray(meta.install)) {
      frontmatter.metadata.install = meta.install as SkillInstallSpec[];
    }
  }

  // Parse invocation control fields
  // Support both kebab-case and camelCase for compatibility
  const userInvocableRaw =
    raw["user-invocable"] ?? raw["userInvocable"] ?? raw["user_invocable"];
  if (typeof userInvocableRaw === "boolean") {
    frontmatter.userInvocable = userInvocableRaw;
  } else if (typeof userInvocableRaw === "string") {
    frontmatter.userInvocable = parseBooleanString(userInvocableRaw);
  }

  const disableModelRaw =
    raw["disable-model-invocation"] ??
    raw["disableModelInvocation"] ??
    raw["disable_model_invocation"];
  if (typeof disableModelRaw === "boolean") {
    frontmatter.disableModelInvocation = disableModelRaw;
  } else if (typeof disableModelRaw === "string") {
    frontmatter.disableModelInvocation = parseBooleanString(disableModelRaw);
  }

  // Parse command dispatch fields
  const dispatchRaw =
    raw["command-dispatch"] ?? raw["commandDispatch"] ?? raw["command_dispatch"];
  if (typeof dispatchRaw === "string") {
    frontmatter.commandDispatch = dispatchRaw.trim().toLowerCase();
  }

  const toolRaw = raw["command-tool"] ?? raw["commandTool"] ?? raw["command_tool"];
  if (typeof toolRaw === "string") {
    frontmatter.commandTool = toolRaw.trim();
  }

  const argModeRaw =
    raw["command-arg-mode"] ?? raw["commandArgMode"] ?? raw["command_arg_mode"];
  if (typeof argModeRaw === "string") {
    frontmatter.commandArgMode = argModeRaw.trim().toLowerCase();
  }

  return frontmatter;
}

/**
 * Parse boolean from string value
 */
function parseBooleanString(value: string): boolean | undefined {
  const normalized = value.trim().toLowerCase();
  if (normalized === "true" || normalized === "yes" || normalized === "1") {
    return true;
  }
  if (normalized === "false" || normalized === "no" || normalized === "0") {
    return false;
  }
  return undefined;
}

/**
 * Parse a SKILL.md file into a Skill object
 *
 * @param filePath - Full path to SKILL.md file
 * @param skillId - Unique identifier for the skill
 * @param source - Source type of the skill
 * @returns Parsed Skill or null if invalid
 */
export function parseSkillFile(
  filePath: string,
  skillId: string,
  source: SkillSource,
): Skill | null {
  try {
    const content = readFileSync(filePath, "utf-8");
    const [rawFrontmatter, instructions] = parseFrontmatter(content);

    if (!rawFrontmatter) {
      return null;
    }

    const frontmatter = validateFrontmatter(rawFrontmatter);
    if (!frontmatter) {
      return null;
    }

    // Load .env from skill directory
    const skillDir = dirname(filePath);
    const envPath = join(skillDir, ".env");
    let env: Record<string, string> = {};
    if (existsSync(envPath)) {
      try {
        env = parseDotEnv(readFileSync(envPath, "utf-8"));
      } catch {
        // Ignore .env parse errors
      }
    }

    return {
      id: skillId,
      frontmatter,
      instructions,
      source,
      filePath,
      env,
    };
  } catch {
    // File read error or other issues
    return null;
  }
}
