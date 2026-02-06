/**
 * Channel configuration loader.
 *
 * Reads the `channels` section from ~/.super-multica/credentials.json5.
 */

import { credentialManager } from "../agent/credentials.js";
import type { ChannelsConfig } from "./types.js";

/** Load channels config from credentials.json5 `channels` section */
export function loadChannelsConfig(): ChannelsConfig {
  const channels = credentialManager.getChannelsConfig();
  const keys = Object.keys(channels);
  if (keys.length === 0) {
    console.log("[Channels] No channels configured in credentials.json5, skipping");
    return {};
  }
  console.log(`[Channels] Loaded config for: ${keys.join(", ")}`);
  return channels;
}
