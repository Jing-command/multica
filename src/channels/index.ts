/**
 * Channel system bootstrap and exports.
 */

export { ChannelManager } from "./manager.js";
export { registerChannel, getChannel, listChannels } from "./registry.js";
export { loadChannelsConfig } from "./config.js";
export type {
  ChannelPlugin,
  ChannelMessage,
  DeliveryContext,
  ChannelAccountState,
  ChannelsConfig,
} from "./types.js";

// Built-in channel plugins
import { registerChannel } from "./registry.js";
import { telegramChannel } from "./plugins/telegram.js";

/** Register all built-in channel plugins. Call once at startup. */
export function initChannels(): void {
  registerChannel(telegramChannel);
  // Future: registerChannel(discordChannel);
  // Future: registerChannel(feishuChannel);
}
