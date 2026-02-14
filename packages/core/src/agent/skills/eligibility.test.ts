import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { checkEligibility, filterEligibleSkills, type EligibilityContext } from "./eligibility.js";
import type { Skill, SkillFrontmatter } from "./types.js";

// Helper to create a skill for testing
function createSkill(
  id: string,
  frontmatter: Partial<SkillFrontmatter> & { name: string },
  source: "bundled" | "profile" = "bundled",
  env: Record<string, string> = {},
): Skill {
  return {
    id,
    frontmatter: frontmatter as SkillFrontmatter,
    instructions: "Test instructions",
    source,
    filePath: `/path/to/${id}/SKILL.md`,
    env,
  };
}

// Helper to create context
function ctx(platform: NodeJS.Platform): EligibilityContext {
  return { platform };
}

describe("eligibility", () => {
  describe("checkEligibility", () => {
    describe("platform requirements", () => {
      it("should be eligible when no platform requirement specified", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
        expect(result.reasons).toBeUndefined();
      });

      it("should be eligible when current platform matches (legacy platforms field)", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            platforms: ["darwin", "linux"],
          },
        });

        expect(checkEligibility(skill, ctx("darwin")).eligible).toBe(true);
        expect(checkEligibility(skill, ctx("linux")).eligible).toBe(true);
      });

      it("should be eligible when current platform matches (new os field)", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            os: ["darwin", "linux"],
          },
        });

        expect(checkEligibility(skill, ctx("darwin")).eligible).toBe(true);
        expect(checkEligibility(skill, ctx("linux")).eligible).toBe(true);
      });

      it("should be ineligible when platform does not match", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            platforms: ["darwin"],
          },
        });

        const result = checkEligibility(skill, ctx("win32"));
        expect(result.eligible).toBe(false);
        expect(result.reasons).toContain(
          "Platform 'win32' not supported (requires: darwin)",
        );
      });

      it("should handle empty platforms array as no requirement", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            platforms: [],
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
      });
    });

    describe("binary requirements", () => {
      it("should be eligible when required binary exists (legacy requiresBinaries)", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requiresBinaries: ["node"],
          },
        });

        // node should exist in the test environment
        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
      });

      it("should be eligible when required binary exists (new requires.bins)", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requires: {
              bins: ["node"],
            },
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
      });

      it("should be ineligible when required binary does not exist", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requiresBinaries: ["nonexistent-binary-xyz-123"],
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(false);
        expect(result.reasons).toContainEqual(
          expect.stringContaining("Required binary not found: nonexistent-binary-xyz-123"),
        );
      });

      it("should check all binaries and report all missing", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requiresBinaries: ["node", "missing-bin-1", "missing-bin-2"],
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(false);
        expect(result.reasons?.length).toBe(2);
        expect(result.reasons).toContainEqual(
          expect.stringContaining("missing-bin-1"),
        );
        expect(result.reasons).toContainEqual(
          expect.stringContaining("missing-bin-2"),
        );
      });

      it("should handle empty binaries array", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requiresBinaries: [],
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
      });
    });

    describe("anyBins requirements", () => {
      it("should be eligible when at least one binary exists", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requires: {
              anyBins: ["nonexistent-1", "node", "nonexistent-2"],
            },
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
      });

      it("should be ineligible when none of anyBins exist", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requires: {
              anyBins: ["nonexistent-1", "nonexistent-2"],
            },
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(false);
        expect(result.reasons).toContainEqual(
          expect.stringContaining("None of required binaries found"),
        );
      });
    });

    describe("environment variable requirements", () => {
      const originalEnv = process.env;

      beforeEach(() => {
        process.env = { ...originalEnv };
      });

      afterEach(() => {
        process.env = originalEnv;
      });

      it("should be eligible when required env vars exist (legacy requiresEnv)", () => {
        process.env.TEST_VAR = "value";

        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requiresEnv: ["TEST_VAR"],
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
      });

      it("should be eligible when required env vars exist (new requires.env)", () => {
        process.env.TEST_VAR = "value";

        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requires: {
              env: ["TEST_VAR"],
            },
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
      });

      it("should be eligible when env var is in skill .env file", () => {
        delete process.env.SKILL_API_KEY;

        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requires: {
              env: ["SKILL_API_KEY"],
            },
          },
        }, "bundled", { SKILL_API_KEY: "test-value" });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
      });

      it("should be ineligible when env var not in skill .env or process.env", () => {
        delete process.env.MISSING_KEY;

        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requires: {
              env: ["MISSING_KEY"],
            },
          },
        }, "bundled", {});

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(false);
      });

      it("should be eligible even if env var is empty string", () => {
        process.env.EMPTY_VAR = "";

        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requiresEnv: ["EMPTY_VAR"],
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
      });

      it("should be ineligible when required env var does not exist", () => {
        delete process.env.MISSING_VAR;

        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requiresEnv: ["MISSING_VAR"],
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(false);
        expect(result.reasons).toContainEqual(
          expect.stringContaining("Required environment variable not set: MISSING_VAR"),
        );
      });

      it("should check all env vars and report all missing", () => {
        process.env.EXISTS = "yes";
        delete process.env.MISSING_1;
        delete process.env.MISSING_2;

        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            requiresEnv: ["EXISTS", "MISSING_1", "MISSING_2"],
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(false);
        expect(result.reasons?.length).toBe(2);
      });

    });

    describe("always flag", () => {
      it("should be eligible when always is true regardless of other checks", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            always: true,
            requiresBinaries: ["nonexistent-binary"],
            requiresEnv: ["NONEXISTENT_VAR"],
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
      });
    });

    describe("config disabled", () => {
      it("should be ineligible when explicitly disabled in config", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
        });

        const result = checkEligibility(skill, {
          platform: "darwin",
          config: {
            entries: {
              test: {
                enabled: false,
              },
            },
          },
        });
        expect(result.eligible).toBe(false);
        expect(result.reasons).toContain("Skill disabled in configuration");
      });
    });

    describe("bundled allowlist", () => {
      it("should be ineligible when bundled skill not in allowlist", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
        }, "bundled");

        const result = checkEligibility(skill, {
          platform: "darwin",
          config: {
            allowBundled: ["other-skill"],
          },
        });
        expect(result.eligible).toBe(false);
        expect(result.reasons).toContain("Bundled skill not in allowlist");
      });

      it("should be eligible when bundled skill in allowlist", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
        }, "bundled");

        const result = checkEligibility(skill, {
          platform: "darwin",
          config: {
            allowBundled: ["test", "other-skill"],
          },
        });
        expect(result.eligible).toBe(true);
      });

      it("should allow all bundled skills when allowlist is empty", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
        }, "bundled");

        const result = checkEligibility(skill, {
          platform: "darwin",
          config: {
            allowBundled: [],
          },
        });
        expect(result.eligible).toBe(true);
      });

      it("should not affect profile skills", () => {
        const skill = createSkill("test", {
          name: "Test Skill",
        }, "profile");

        const result = checkEligibility(skill, {
          platform: "darwin",
          config: {
            allowBundled: ["other-skill"],
          },
        });
        expect(result.eligible).toBe(true);
      });
    });

    describe("combined requirements", () => {
      const originalEnv = process.env;

      beforeEach(() => {
        process.env = { ...originalEnv };
      });

      afterEach(() => {
        process.env = originalEnv;
      });

      it("should collect all failure reasons", () => {
        delete process.env.MISSING_VAR;

        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            platforms: ["win32"],
            requiresBinaries: ["missing-binary"],
            requiresEnv: ["MISSING_VAR"],
          },
        });

        // Note: platform check fails first and returns early
        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(false);
        // Platform check returns early
        expect(result.reasons?.length).toBe(1);
      });

      it("should be eligible when all requirements met", () => {
        process.env.REQUIRED_VAR = "value";

        const skill = createSkill("test", {
          name: "Test Skill",
          metadata: {
            platforms: ["darwin", "linux"],
            requiresBinaries: ["node"],
            requiresEnv: ["REQUIRED_VAR"],
          },
        });

        const result = checkEligibility(skill, ctx("darwin"));
        expect(result.eligible).toBe(true);
        expect(result.reasons).toBeUndefined();
      });
    });

    it("should use process.platform by default", () => {
      const skill = createSkill("test", {
        name: "Test Skill",
        metadata: {
          platforms: [process.platform],
        },
      });

      // Call without context
      const result = checkEligibility(skill);
      expect(result.eligible).toBe(true);
    });
  });

  describe("filterEligibleSkills", () => {
    const originalEnv = process.env;

    beforeEach(() => {
      process.env = { ...originalEnv };
    });

    afterEach(() => {
      process.env = originalEnv;
    });

    it("should return only eligible skills", () => {
      const skills = new Map<string, Skill>([
        ["darwin-only", createSkill("darwin-only", {
          name: "Darwin Only",
          metadata: { platforms: ["darwin"] },
        })],
        ["linux-only", createSkill("linux-only", {
          name: "Linux Only",
          metadata: { platforms: ["linux"] },
        })],
        ["all-platforms", createSkill("all-platforms", {
          name: "All Platforms",
        })],
      ]);

      const eligible = filterEligibleSkills(skills, ctx("darwin"));

      expect(eligible.size).toBe(2);
      expect(eligible.has("darwin-only")).toBe(true);
      expect(eligible.has("all-platforms")).toBe(true);
      expect(eligible.has("linux-only")).toBe(false);
    });

    it("should return empty map when no skills are eligible", () => {
      const skills = new Map<string, Skill>([
        ["win-only", createSkill("win-only", {
          name: "Windows Only",
          metadata: { platforms: ["win32"] },
        })],
      ]);

      const eligible = filterEligibleSkills(skills, ctx("darwin"));

      expect(eligible.size).toBe(0);
    });

    it("should return all skills when all are eligible", () => {
      const skills = new Map<string, Skill>([
        ["skill-1", createSkill("skill-1", { name: "Skill 1" })],
        ["skill-2", createSkill("skill-2", { name: "Skill 2" })],
        ["skill-3", createSkill("skill-3", { name: "Skill 3" })],
      ]);

      const eligible = filterEligibleSkills(skills, ctx("darwin"));

      expect(eligible.size).toBe(3);
    });

    it("should handle empty input map", () => {
      const skills = new Map<string, Skill>();
      const eligible = filterEligibleSkills(skills, ctx("darwin"));
      expect(eligible.size).toBe(0);
    });

    it("should respect config when filtering", () => {
      const skills = new Map<string, Skill>([
        ["enabled-skill", createSkill("enabled-skill", { name: "Enabled" })],
        ["disabled-skill", createSkill("disabled-skill", { name: "Disabled" })],
      ]);

      const eligible = filterEligibleSkills(skills, {
        platform: "darwin",
        config: {
          entries: {
            "disabled-skill": { enabled: false },
          },
        },
      });

      expect(eligible.size).toBe(1);
      expect(eligible.has("enabled-skill")).toBe(true);
      expect(eligible.has("disabled-skill")).toBe(false);
    });
  });
});
