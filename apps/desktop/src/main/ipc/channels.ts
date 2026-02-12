/**
 * Channel IPC handlers for Electron main process.
 *
 * Manages channel account configuration, start/stop lifecycle.
 * The Channels page in the renderer uses these to configure
 * Telegram (and future channels) with immediate effect.
 */
import { ipcMain } from 'electron'
import { getCurrentHub } from './hub.js'
import { credentialManager, listChannels } from '@multica/core'

/** Validate that a string is a safe identifier (alphanumeric, dashes, underscores) */
function isValidId(value: unknown): value is string {
  return typeof value === 'string' && /^[a-zA-Z0-9_-]+$/.test(value) && value.length <= 64
}

/**
 * Mask a token string for safe display: show first 5 and last 5 chars.
 * Returns undefined if the input is not a string.
 */
function maskToken(token: unknown): string | undefined {
  if (typeof token !== 'string' || token.length === 0) return undefined
  if (token.length <= 12) return '*'.repeat(token.length)
  return `${token.slice(0, 5)}${'*'.repeat(10)}${token.slice(-5)}`
}

/**
 * Register all Channel-related IPC handlers.
 */
export function registerChannelsIpcHandlers(): void {
  /**
   * List all channel account states (running / stopped / error).
   * Waits for any "starting" status to settle before returning.
   */
  ipcMain.handle('channels:listStates', async () => {
    const hub = getCurrentHub()
    if (!hub) return []

    let states = hub.channelManager.listAccountStates()

    // Wait for "starting" status to settle (max 3.5s, since internal timeout is 3s)
    const start = Date.now()
    while (states.some(s => s.status === 'starting') && Date.now() - start < 3500) {
      await new Promise(r => setTimeout(r, 100))
      states = hub.channelManager.listAccountStates()
    }

    return states
  })

  /**
   * Get the channels config from credentials.json5.
   * Returns a sanitized version with tokens masked (not the raw secret values).
   */
  ipcMain.handle('channels:getConfig', async () => {
    const raw = credentialManager.getChannelsConfig()
    // Mask secret values before sending to renderer
    const masked: Record<string, Record<string, Record<string, unknown>> | undefined> = {}
    for (const [channelId, accounts] of Object.entries(raw)) {
      if (!accounts) continue
      const maskedAccounts: Record<string, Record<string, unknown>> = {}
      for (const [accountId, accountConfig] of Object.entries(accounts)) {
        const maskedConfig = { ...accountConfig }
        if ('botToken' in maskedConfig) {
          maskedConfig.botToken = maskToken(maskedConfig.botToken)
        }
        maskedAccounts[accountId] = maskedConfig
      }
      masked[channelId] = maskedAccounts
    }
    return masked
  })

  /**
   * Save a channel account token and start the bot immediately.
   * Flow: validate → write to credentials.json5 → start the channel account.
   */
  ipcMain.handle(
    'channels:saveToken',
    async (_event, channelId: string, accountId: string, token: string): Promise<{ ok: boolean; error?: string }> => {
      try {
        // Validate inputs
        if (!isValidId(channelId)) return { ok: false, error: 'Invalid channel ID' }
        if (!isValidId(accountId)) return { ok: false, error: 'Invalid account ID' }
        if (typeof token !== 'string' || token.trim().length === 0 || token.length > 256) {
          return { ok: false, error: 'Invalid token' }
        }

        const hub = getCurrentHub()
        if (!hub) return { ok: false, error: 'Hub not initialized' }

        // Find the plugin to validate channelId
        const plugin = listChannels().find((p) => p.id === channelId)
        if (!plugin) return { ok: false, error: `Unknown channel: ${channelId}` }

        // Persist config to credentials.json5
        credentialManager.setChannelAccountConfig(channelId, accountId, { botToken: token })
        console.log(`[IPC] Channel config saved: ${channelId}:${accountId}`)

        // Stop existing account if running (e.g. token update)
        hub.channelManager.stopAccount(channelId, accountId)

        // Start the account with the new config
        await hub.channelManager.startAccount(channelId, accountId, { botToken: token })
        console.log(`[IPC] Channel started: ${channelId}:${accountId}`)

        return { ok: true }
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err)
        console.error(`[IPC] Failed to save channel token: ${message}`)
        return { ok: false, error: message }
      }
    }
  )

  /**
   * Remove a channel account token and stop the bot.
   */
  ipcMain.handle(
    'channels:removeToken',
    async (_event, channelId: string, accountId: string): Promise<{ ok: boolean; error?: string }> => {
      try {
        if (!isValidId(channelId)) return { ok: false, error: 'Invalid channel ID' }
        if (!isValidId(accountId)) return { ok: false, error: 'Invalid account ID' }

        const hub = getCurrentHub()
        if (!hub) return { ok: false, error: 'Hub not initialized' }

        // Stop the account
        hub.channelManager.stopAccount(channelId, accountId)

        // Remove from credentials.json5
        credentialManager.removeChannelAccountConfig(channelId, accountId)
        console.log(`[IPC] Channel config removed: ${channelId}:${accountId}`)

        return { ok: true }
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err)
        console.error(`[IPC] Failed to remove channel token: ${message}`)
        return { ok: false, error: message }
      }
    }
  )

  /**
   * Stop a channel account without removing its config.
   */
  ipcMain.handle(
    'channels:stop',
    async (_event, channelId: string, accountId: string): Promise<{ ok: boolean; error?: string }> => {
      if (!isValidId(channelId)) return { ok: false, error: 'Invalid channel ID' }
      if (!isValidId(accountId)) return { ok: false, error: 'Invalid account ID' }
      const hub = getCurrentHub()
      if (!hub) return { ok: false, error: 'Hub not initialized' }
      hub.channelManager.stopAccount(channelId, accountId)
      return { ok: true }
    }
  )

  /**
   * Start a channel account using its saved config.
   */
  ipcMain.handle(
    'channels:start',
    async (_event, channelId: string, accountId: string): Promise<{ ok: boolean; error?: string }> => {
      try {
        if (!isValidId(channelId)) return { ok: false, error: 'Invalid channel ID' }
        if (!isValidId(accountId)) return { ok: false, error: 'Invalid account ID' }

        const hub = getCurrentHub()
        if (!hub) return { ok: false, error: 'Hub not initialized' }

        // Read config from credentials
        const config = credentialManager.getChannelsConfig()
        const accountConfig = config[channelId]?.[accountId]
        if (!accountConfig) {
          return { ok: false, error: `No config found for ${channelId}:${accountId}` }
        }

        await hub.channelManager.startAccount(channelId, accountId, accountConfig)
        return { ok: true }
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err)
        return { ok: false, error: message }
      }
    }
  )
}
