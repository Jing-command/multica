/**
 * Skills Loader
 *
 * Two-source loading with precedence handling:
 * 1. managed - ~/.super-multica/skills/ (global skills)
 * 2. profile - ~/.super-multica/agent-profiles/<id>/skills/ (profile-specific)
 */

import { existsSync, readdirSync, readFileSync, statSync, mkdirSync, cpSync, rmSync, writeFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import type { Skill, SkillSource, SkillManagerOptions } from "./types.js";
import { SKILL_FILE, SKILL_SOURCE_PRECEDENCE } from "./types.js";
import { parseSkillFile } from "./parser.js";
import { DATA_DIR } from "@multica/utils";

/**
 * Compare two semver version strings
 * Returns: 1 if a > b, -1 if a < b, 0 if equal
 */
function compareVersions(a: string | undefined, b: string | undefined): number {
  if (!a && !b) return 0;
  if (!a) return -1;
  if (!b) return 1;

  const partsA = a.split(".").map((n) => parseInt(n, 10) || 0);
  const partsB = b.split(".").map((n) => parseInt(n, 10) || 0);

  const maxLen = Math.max(partsA.length, partsB.length);
  for (let i = 0; i < maxLen; i++) {
    const numA = partsA[i] ?? 0;
    const numB = partsB[i] ?? 0;
    if (numA > numB) return 1;
    if (numA < numB) return -1;
  }
  return 0;
}

const __dirname = dirname(fileURLToPath(import.meta.url));

/** Default profile base directory */
const DEFAULT_PROFILE_BASE_DIR = join(DATA_DIR, "agent-profiles");

/** Bundled skills directory (relative to package, used for initialization) */
const BUNDLED_DIR = join(__dirname, "../../../skills");

/** Managed skills directory (global user skills) */
const MANAGED_DIR = join(DATA_DIR, "skills");

/** Manifest file tracking which skills were synced from the bundle */
const BUNDLED_MANIFEST = join(MANAGED_DIR, ".bundled-manifest.json");

/**
 * Read the bundled skills manifest
 * Returns a set of skill IDs that were last synced from the bundle
 */
function readBundledManifest(): Set<string> {
  try {
    if (!existsSync(BUNDLED_MANIFEST)) return new Set();
    const data = JSON.parse(readFileSync(BUNDLED_MANIFEST, "utf-8"));
    if (Array.isArray(data)) return new Set(data as string[]);
    return new Set();
  } catch {
    return new Set();
  }
}

/**
 * Write the bundled skills manifest
 */
function writeBundledManifest(skillIds: Set<string>): void {
  writeFileSync(BUNDLED_MANIFEST, JSON.stringify([...skillIds].sort(), null, 2) + "\n");
}

/**
 * Discover skill directories in a given base path
 * A valid skill directory contains a SKILL.md file
 * Searches up to maxDepth levels deep
 *
 * @param baseDir - Base directory to search
 * @param maxDepth - Maximum depth to search (default: 3)
 * @returns Array of absolute paths to skill directories
 */
function discoverSkillDirs(baseDir: string, maxDepth: number = 3): string[] {
  if (!existsSync(baseDir)) {
    return [];
  }

  const results: string[] = [];

  function scan(dir: string, depth: number): void {
    if (depth > maxDepth) return;

    try {
      const entries = readdirSync(dir);

      for (const name of entries) {
        // Skip hidden directories
        if (name.startsWith(".")) continue;

        const fullPath = join(dir, name);

        try {
          if (!statSync(fullPath).isDirectory()) continue;

          // Check if this directory has SKILL.md
          if (existsSync(join(fullPath, SKILL_FILE))) {
            results.push(fullPath);
          } else {
            // Recurse into subdirectory
            scan(fullPath, depth + 1);
          }
        } catch {
          // Skip inaccessible directories
        }
      }
    } catch {
      // Skip inaccessible directories
    }
  }

  scan(baseDir, 0);
  return results;
}

/**
 * Load all skills from a source directory
 *
 * @param baseDir - Base directory containing skill subdirectories
 * @param source - Source type for loaded skills
 * @returns Array of loaded skills
 */
function loadSkillsFromSource(baseDir: string, source: SkillSource): Skill[] {
  const skillDirs = discoverSkillDirs(baseDir);
  const skills: Skill[] = [];

  for (const dir of skillDirs) {
    const skillId = dir.split("/").pop();
    if (!skillId) continue;

    const filePath = join(dir, SKILL_FILE);
    const skill = parseSkillFile(filePath, skillId, source);
    if (skill) {
      skills.push(skill);
    }
  }

  return skills;
}

/**
 * Get profile skills directory path
 *
 * @param profileId - Agent profile ID
 * @param profileBaseDir - Profile base directory
 * @returns Path to profile skills directory
 */
export function getProfileSkillsDir(profileId: string, profileBaseDir?: string): string {
  const baseDir = profileBaseDir ?? DEFAULT_PROFILE_BASE_DIR;
  return join(baseDir, profileId, "skills");
}

/**
 * Initialize managed skills directory with bundled skills
 * Copies bundled skills to ~/.super-multica/skills/ if not already present
 * Updates existing skills if bundled version is higher
 *
 * This should be called once during application startup.
 */
export function initializeManagedSkills(): void {
  // Create managed dir if not exists
  if (!existsSync(MANAGED_DIR)) {
    mkdirSync(MANAGED_DIR, { recursive: true });
  }

  // Skip if bundled dir doesn't exist (e.g., in production builds)
  if (!existsSync(BUNDLED_DIR)) {
    return;
  }

  const previouslyBundled = readBundledManifest();
  const currentlyBundled = new Set<string>();

  // Sync each bundled skill to managed directory
  try {
    const entries = readdirSync(BUNDLED_DIR);
    for (const skillName of entries) {
      // Skip hidden directories and shared/internal directories
      if (skillName.startsWith(".") || skillName.startsWith("_")) continue;

      const src = join(BUNDLED_DIR, skillName);
      const dest = join(MANAGED_DIR, skillName);

      // Skip if not a directory
      if (!statSync(src).isDirectory()) continue;

      currentlyBundled.add(skillName);

      // Check if skill exists in managed
      if (!existsSync(dest)) {
        // Skill doesn't exist, copy it (skip .env files - those are user-specific)
        cpSync(src, dest, { recursive: true, dereference: true, filter: (s) => !s.endsWith("/.env") });
        continue;
      }

      // Skill exists, check versions
      const bundledSkill = parseSkillFile(join(src, SKILL_FILE), skillName, "bundled");
      const managedSkill = parseSkillFile(join(dest, SKILL_FILE), skillName, "bundled");

      if (!bundledSkill) continue; // Invalid bundled skill, skip

      const bundledVersion = bundledSkill.frontmatter.version;
      const managedVersion = managedSkill?.frontmatter.version;

      // Update if bundled version is higher
      if (compareVersions(bundledVersion, managedVersion) > 0) {
        // Preserve user's .env file across upgrades
        const envPath = join(dest, ".env");
        let savedEnv: string | null = null;
        if (existsSync(envPath)) {
          try { savedEnv = readFileSync(envPath, "utf-8"); } catch { /* ignore */ }
        }

        // Remove old and copy new (skip .env from bundle)
        rmSync(dest, { recursive: true });
        cpSync(src, dest, { recursive: true, dereference: true, filter: (s) => !s.endsWith("/.env") });

        // Restore user's .env
        if (savedEnv !== null) {
          writeFileSync(envPath, savedEnv, "utf-8");
        }
      }
    }

    // Remove managed skills that were previously bundled but no longer in the bundle
    for (const skillName of previouslyBundled) {
      if (!currentlyBundled.has(skillName)) {
        const dest = join(MANAGED_DIR, skillName);
        if (existsSync(dest)) {
          rmSync(dest, { recursive: true });
        }
      }
    }

    // Persist updated manifest
    writeBundledManifest(currentlyBundled);
  } catch {
    // Ignore errors during initialization
  }
}

/**
 * Get path to managed skills directory
 */
export function getManagedSkillsDir(): string {
  return MANAGED_DIR;
}

/**
 * Load all skills from all sources, applying precedence
 * Higher precedence sources override skills with the same ID
 *
 * Loading order (lowest to highest precedence):
 * 1. managed - ~/.super-multica/skills/ (global skills)
 * 2. profile - ~/.super-multica/agent-profiles/<profileId>/skills/
 *
 * @param options - Loader options
 * @returns Map of skill ID to Skill
 */
export function loadAllSkills(options: SkillManagerOptions = {}): Map<string, Skill> {
  // Initialize managed skills on first load (copies bundled skills if needed)
  initializeManagedSkills();

  const skillMap = new Map<string, Skill>();

  // 1. Load managed skills (lower precedence)
  const managedSkills = loadSkillsFromSource(MANAGED_DIR, "bundled");
  for (const skill of managedSkills) {
    skillMap.set(skill.id, skill);
  }

  // 2. Load profile skills if profileId is provided (higher precedence)
  if (options.profileId) {
    const profileSkillsDir = getProfileSkillsDir(options.profileId, options.profileBaseDir);
    const profileSkills = loadSkillsFromSource(profileSkillsDir, "profile");
    for (const skill of profileSkills) {
      skillMap.set(skill.id, skill); // Override managed
    }
  }

  return skillMap;
}
