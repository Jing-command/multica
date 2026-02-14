import { describe, it, expect } from "vitest";
import { parseDotEnv } from "./dotenv.js";

describe("parseDotEnv", () => {
  it("should parse basic KEY=VALUE pairs", () => {
    const result = parseDotEnv("FOO=bar\nBAZ=qux");
    expect(result).toEqual({ FOO: "bar", BAZ: "qux" });
  });

  it("should handle double-quoted values", () => {
    const result = parseDotEnv('KEY="hello world"');
    expect(result).toEqual({ KEY: "hello world" });
  });

  it("should handle single-quoted values", () => {
    const result = parseDotEnv("KEY='hello world'");
    expect(result).toEqual({ KEY: "hello world" });
  });

  it("should skip comments", () => {
    const result = parseDotEnv("# This is a comment\nKEY=value\n# Another comment");
    expect(result).toEqual({ KEY: "value" });
  });

  it("should skip blank lines", () => {
    const result = parseDotEnv("\n\nKEY=value\n\n");
    expect(result).toEqual({ KEY: "value" });
  });

  it("should handle empty values", () => {
    const result = parseDotEnv("KEY=");
    expect(result).toEqual({ KEY: "" });
  });

  it("should handle equals sign in value", () => {
    const result = parseDotEnv("KEY=foo=bar=baz");
    expect(result).toEqual({ KEY: "foo=bar=baz" });
  });

  it("should handle spaces around key and value", () => {
    const result = parseDotEnv("  KEY  =  value  ");
    expect(result).toEqual({ KEY: "value" });
  });

  it("should skip lines without equals sign", () => {
    const result = parseDotEnv("INVALID_LINE\nKEY=value");
    expect(result).toEqual({ KEY: "value" });
  });

  it("should return empty object for empty content", () => {
    expect(parseDotEnv("")).toEqual({});
  });

  it("should handle CRLF line endings", () => {
    const result = parseDotEnv("A=1\r\nB=2\r\n");
    expect(result).toEqual({ A: "1", B: "2" });
  });
});
